import json
import logging
import signal
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Optional, Dict, Any

from config import Config, load_config
from modbus import ModbusRTU, ModbusError


logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s %(levelname)s %(message)s',
)


class DriverState:
    def __init__(self, cfg: Config):
        self.cfg = cfg
        self.lock = threading.RLock()
        self.modbus: Optional[ModbusRTU] = None
        self.connected: bool = False
        self.port: Optional[str] = None
        self.slave_id: Optional[int] = None
        self.poll_thread: Optional[threading.Thread] = None
        self.stop_event = threading.Event()
        self.poll_running = False

        self.last_read_at: Optional[float] = None
        self.last_error: Optional[str] = None

        self.sample: Dict[str, Any] = {
            'temperature': None,
            'humidity': None,
            'co2': None,
            'raw': {
                'temperature': None,
                'humidity': None,
                'co2': None,
            },
            'timestamp': None,
        }

    def _open_serial(self, port: str):
        # Device requires 9600 8N1
        m = ModbusRTU(
            port=port,
            baudrate=9600,
            bytesize=8,
            parity='N',
            stopbits=1,
            timeout_s=self.cfg.modbus_timeout_ms / 1000.0,
        )
        m.open()
        return m

    def connect(self, port: Optional[str], slave_id: Optional[int]) -> Dict[str, Any]:
        with self.lock:
            new_port = port or self.cfg.modbus_port
            new_slave = slave_id if slave_id is not None else self.cfg.modbus_slave_id
            if not new_port or new_slave is None:
                raise ValueError('port and slave_id must be provided (via env or request)')

            if self.connected and self.port == new_port and self.slave_id == new_slave:
                logging.info('Already connected to %s (slave %d), no-op', new_port, new_slave)
                return self.status()

            logging.info('Initializing connection to %s (slave %d) at 9600 8N1', new_port, new_slave)

            # Stop polling and close any previous connection
            self._stop_polling_locked()
            if self.modbus is not None:
                try:
                    self.modbus.close()
                except Exception:
                    pass
                self.modbus = None

            # Open new connection
            self.modbus = self._open_serial(new_port)
            self.connected = True
            self.port = new_port
            self.slave_id = new_slave
            self.last_error = None

            # Start polling thread
            self._start_polling_locked()
            return self.status()

    def _start_polling_locked(self):
        if self.poll_thread and self.poll_thread.is_alive():
            return
        self.stop_event.clear()
        self.poll_running = True
        self.poll_thread = threading.Thread(target=self._poll_loop, name='poller', daemon=True)
        self.poll_thread.start()
        logging.info('Started polling thread with interval %d ms', self.cfg.poll_interval_ms)

    def _stop_polling_locked(self):
        if self.poll_thread and self.poll_thread.is_alive():
            self.stop_event.set()
            self.poll_thread.join(timeout=5.0)
            if self.poll_thread.is_alive():
                logging.warning('Polling thread did not stop within timeout')
        self.poll_running = False
        self.stop_event.clear()
        self.poll_thread = None

    def shutdown(self):
        logging.info('Shutting down driver...')
        with self.lock:
            self._stop_polling_locked()
            if self.modbus is not None:
                try:
                    self.modbus.close()
                except Exception:
                    pass
                self.modbus = None
            self.connected = False
            logging.info('Driver shut down complete')

    def _read_register(self, address: int) -> Optional[int]:
        if not (self.modbus and self.connected and self.slave_id is not None):
            return None
        try:
            regs = self.modbus.read_holding_registers(self.slave_id, address, 1)
            if len(regs) != 1:
                raise ModbusError(f'unexpected length {len(regs)}')
            return regs[0]
        except Exception as e:
            raise

    def _poll_once(self) -> bool:
        # Returns True on success; False on failure
        try:
            t_raw = self._read_register(self.cfg.reg_temp_addr)
            h_raw = self._read_register(self.cfg.reg_hum_addr)
            c_raw = self._read_register(self.cfg.reg_co2_addr)
            if t_raw is None or h_raw is None or c_raw is None:
                raise RuntimeError('read returned None')

            # Apply optional scaling factors
            t_val = (t_raw * self.cfg.reg_temp_scale) if self.cfg.reg_temp_scale is not None else t_raw
            h_val = (h_raw * self.cfg.reg_hum_scale) if self.cfg.reg_hum_scale is not None else h_raw
            c_val = (c_raw * self.cfg.reg_co2_scale) if self.cfg.reg_co2_scale is not None else c_raw

            now = time.time()
            with self.lock:
                self.sample = {
                    'temperature': t_val,
                    'humidity': h_val,
                    'co2': c_val,
                    'raw': {
                        'temperature': t_raw,
                        'humidity': h_raw,
                        'co2': c_raw,
                    },
                    'timestamp': time.strftime('%Y-%m-%dT%H:%M:%S%z', time.localtime(now)),
                }
                self.last_read_at = now
                self.last_error = None
            logging.debug('Polled: T=%s H=%s CO2=%s', str(t_val), str(h_val), str(c_val))
            return True
        except Exception as e:
            with self.lock:
                self.last_error = str(e)
            logging.warning('Poll error: %s', e)
            return False

    def _poll_loop(self):
        backoff = self.cfg.backoff_initial_ms / 1000.0
        interval = self.cfg.poll_interval_ms / 1000.0
        max_backoff = self.cfg.backoff_max_ms / 1000.0
        retry_max = self.cfg.retry_max

        while not self.stop_event.is_set():
            # Ensure connection is still open
            with self.lock:
                modbus_ok = (self.modbus is not None and self.connected and self.slave_id is not None and self.modbus.is_open())
            if not modbus_ok:
                with self.lock:
                    self.last_error = 'disconnected'
                logging.error('Disconnected from device')
                # Attempt to sleep with backoff; external /connect must re-establish
                time.sleep(min(backoff, max_backoff))
                backoff = min(backoff * 2, max_backoff)
                continue

            success = False
            for attempt in range(1, retry_max + 1):
                if self.stop_event.is_set():
                    break
                if self._poll_once():
                    success = True
                    break
                else:
                    # brief wait before retry
                    time.sleep(0.05)

            if success:
                backoff = self.cfg.backoff_initial_ms / 1000.0
                # Sleep until next interval or until stop is requested
                waited = 0.0
                while waited < interval and not self.stop_event.is_set():
                    time.sleep(min(0.1, interval - waited))
                    waited += min(0.1, interval - waited)
            else:
                # Exponential backoff on failure
                logging.info('Backing off for %.2fs after failures', backoff)
                time.sleep(min(backoff, max_backoff))
                backoff = min(backoff * 2, max_backoff)

    def status(self) -> Dict[str, Any]:
        with self.lock:
            s = {
                'connected': bool(self.connected and self.modbus and self.modbus.is_open()),
                'port': self.port,
                'slave_id': self.slave_id,
                'baud': 9600,
                'parity': 'N',
                'stopbits': 1,
                'poll': {
                    'enabled': self.poll_running,
                    'interval_ms': self.cfg.poll_interval_ms,
                },
                'last_read_at': time.strftime('%Y-%m-%dT%H:%M:%S%z', time.localtime(self.last_read_at)) if self.last_read_at else None,
                'last_error': self.last_error,
                'sample': self.sample,
            }
            return s


class RequestHandler(BaseHTTPRequestHandler):
    driver: DriverState = None  # type: ignore

    def _send_json(self, status_code: int, payload: Dict[str, Any]):
        body = json.dumps(payload).encode('utf-8')
        self.send_response(status_code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/status':
            payload = self.driver.status()
            self._send_json(200, payload)
        else:
            self._send_json(404, {'error': 'Not Found'})

    def do_POST(self):
        if self.path == '/connect':
            length = int(self.headers.get('Content-Length', '0') or '0')
            body_bytes = self.rfile.read(length) if length > 0 else b''
            port = None
            slave_id = None
            if body_bytes:
                try:
                    data = json.loads(body_bytes.decode('utf-8'))
                    port = data.get('port')
                    slave_id = data.get('slave_id')
                except Exception:
                    return self._send_json(400, {'error': 'Invalid JSON'})
            try:
                status = self.driver.connect(port, slave_id)
                self._send_json(200, status)
            except ValueError as ve:
                self._send_json(400, {'error': str(ve)})
            except Exception as e:
                logging.exception('Connect failed')
                self._send_json(500, {'error': str(e)})
        else:
            self._send_json(404, {'error': 'Not Found'})

    def log_message(self, format: str, *args):
        # Route HTTP server logs through logging module
        logging.info('%s - %s', self.address_string(), format % args)


def main():
    cfg = load_config()

    driver = DriverState(cfg)
    RequestHandler.driver = driver

    server = ThreadingHTTPServer((cfg.http_host, cfg.http_port), RequestHandler)

    stop_event = threading.Event()

    def handle_signal(signum, frame):
        logging.info('Signal %s received, shutting down...', signum)
        stop_event.set()
        try:
            server.shutdown()
        except Exception:
            pass

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    logging.info('HTTP server starting on %s:%d', cfg.http_host, cfg.http_port)
    try:
        server.serve_forever()
    finally:
        driver.shutdown()
        try:
            server.server_close()
        except Exception:
            pass
        logging.info('HTTP server stopped')


if __name__ == '__main__':
    try:
        main()
    except KeyboardInterrupt:
        pass
    except Exception as e:
        logging.exception('Fatal error: %s', e)
        sys.exit(1)
