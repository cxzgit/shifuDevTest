import atexit
import json
import logging
import signal
import threading
import time
from datetime import datetime, timezone
from typing import Optional, Dict, Any

from flask import Flask, jsonify, request
from pymodbus.client import ModbusSerialClient

from config import load_config_from_env, Config


logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(message)s'
)
logger = logging.getLogger("modbus_driver")


class ModbusPoller:
    def __init__(self, cfg: Config):
        self.cfg = cfg
        self._lock = threading.RLock()
        self._stop_evt = threading.Event()
        self._thread: Optional[threading.Thread] = None

        self._client: Optional[ModbusSerialClient] = None
        self._connected: bool = False

        self._desired_connected: bool = False
        self._desired_port: Optional[str] = None
        self._desired_slave_id: Optional[int] = None

        self._current_port: Optional[str] = None
        self._current_slave_id: Optional[int] = None

        self._last_values: Dict[str, Optional[float]] = {
            'temperature': None,
            'humidity': None,
            'co2': None,
        }
        self._last_read_at: Optional[float] = None
        self._last_error: Optional[str] = None

        self._poll_enabled: bool = True
        self._backoff_ms: int = cfg.backoff_initial_ms
        self._failure_count: int = 0

    def start(self):
        if self._thread and self._thread.is_alive():
            return
        self._thread = threading.Thread(target=self._run, name="modbus_poller", daemon=True)
        self._thread.start()

    def stop(self):
        self._stop_evt.set()
        try:
            if self._thread:
                self._thread.join(timeout=5)
        finally:
            with self._lock:
                if self._client is not None:
                    try:
                        self._client.close()
                    except Exception:
                        pass
                    self._client = None
                self._connected = False
            logger.info("Poller stopped and connection closed")

    def request_connect(self, port: Optional[str] = None, slave_id: Optional[int] = None):
        with self._lock:
            # Use provided overrides if given, otherwise environment-configured defaults
            self._desired_port = port if port else self.cfg.modbus_port
            self._desired_slave_id = slave_id if slave_id is not None else self.cfg.slave_id
            self._desired_connected = True
            # Trigger reconnect if already connected to a different target
            if self._connected and (
                self._current_port != self._desired_port or self._current_slave_id != self._desired_slave_id
            ):
                logger.info(
                    "Reinitializing link from %s/%s to %s/%s",
                    self._current_port, self._current_slave_id, self._desired_port, self._desired_slave_id
                )
                self._safe_close()
                self._connected = False
                self._failure_count = 0
                self._backoff_ms = self.cfg.backoff_initial_ms

    def _safe_close(self):
        try:
            if self._client is not None:
                self._client.close()
        except Exception:
            pass
        finally:
            self._client = None

    def _open_client(self) -> bool:
        assert self._desired_port is not None and self._desired_slave_id is not None
        # Establish Modbus RTU client: 9600 baud, 8N1, function 0x03 used for reads
        self._safe_close()
        self._client = ModbusSerialClient(
            method='rtu',
            port=self._desired_port,
            baudrate=9600,
            stopbits=1,
            bytesize=8,
            parity='N',
            timeout=self.cfg.modbus_timeout_s,
        )
        try:
            ok = self._client.connect()
        except Exception as e:
            logger.error("Serial connect error: %s", e)
            ok = False
        if ok:
            self._current_port = self._desired_port
            self._current_slave_id = self._desired_slave_id
            self._connected = True
            self._failure_count = 0
            self._backoff_ms = self.cfg.backoff_initial_ms
            logger.info("Connected to Modbus RTU @ %s (unit=%s)", self._current_port, self._current_slave_id)
            return True
        else:
            self._connected = False
            self._safe_close()
            logger.warning("Failed to connect to %s (unit=%s)", self._desired_port, self._desired_slave_id)
            return False

    def _read_register(self, addr: int) -> int:
        assert self._client is not None
        assert self._current_slave_id is not None
        rr = self._client.read_holding_registers(address=addr, count=1, unit=self._current_slave_id)
        if rr is None or hasattr(rr, 'isError') and rr.isError():
            # pymodbus 2.x returns response with isError()
            # in case of IO exception, rr may be ModbusIOException
            reason = getattr(rr, 'message', 'Read error') if rr is not None else 'No response'
            raise IOError(f"Modbus read failed at address {addr}: {reason}")
        if not hasattr(rr, 'registers') or not rr.registers:
            raise IOError(f"Modbus read returned empty data at address {addr}")
        return int(rr.registers[0])

    def _poll_once(self) -> None:
        # Read temperature, humidity, CO2 using function 0x03
        temp_reg = self._read_register(self.cfg.reg_temperature_addr)
        humi_reg = self._read_register(self.cfg.reg_humidity_addr)
        co2_reg = self._read_register(self.cfg.reg_co2_addr)

        temp = temp_reg * self.cfg.scale_temperature
        humi = humi_reg * self.cfg.scale_humidity
        co2 = co2_reg * self.cfg.scale_co2

        with self._lock:
            self._last_values = {
                'temperature': float(temp),
                'humidity': float(humi),
                'co2': float(co2),
            }
            self._last_read_at = time.time()
            self._last_error = None

    def _sleep_with_stop(self, seconds: float):
        # Sleep in small chunks to react to stop event quickly
        end = time.time() + seconds
        while not self._stop_evt.is_set() and time.time() < end:
            remaining = end - time.time()
            time.sleep(0.05 if remaining > 0.05 else max(remaining, 0))

    def _run(self):
        poll_interval_s = max(self.cfg.poll_interval_ms, 1) / 1000.0
        while not self._stop_evt.is_set():
            if not self._desired_connected:
                self._sleep_with_stop(0.2)
                continue

            if not self._connected:
                ok = self._open_client()
                if not ok:
                    backoff = min(self._backoff_ms, self.cfg.backoff_max_ms) / 1000.0
                    logger.info("Reconnect backoff %.2fs", backoff)
                    self._sleep_with_stop(backoff)
                    self._backoff_ms = min(self.cfg.backoff_max_ms, max(self.cfg.backoff_initial_ms, self._backoff_ms * 2))
                continue

            # Connected: poll
            start_t = time.time()
            try:
                if self._poll_enabled:
                    self._poll_once()
                    with self._lock:
                        ts = self._last_read_at
                    logger.debug("Polled successfully at %s", ts)
                    self._failure_count = 0
                    self._backoff_ms = self.cfg.backoff_initial_ms
            except Exception as e:
                self._failure_count += 1
                msg = str(e)
                with self._lock:
                    self._last_error = msg
                logger.warning("Poll error (%d/%d): %s", self._failure_count, self.cfg.retry_max, msg)
                if self._failure_count >= self.cfg.retry_max:
                    logger.error("Too many errors, closing connection to trigger reconnect")
                    self._safe_close()
                    self._connected = False
                    # Exponential backoff before trying to reconnect
                    self._sleep_with_stop(min(self._backoff_ms, self.cfg.backoff_max_ms) / 1000.0)
                    self._backoff_ms = min(self.cfg.backoff_max_ms, max(self.cfg.backoff_initial_ms, self._backoff_ms * 2))
                    continue

            # Sleep until next interval
            elapsed = time.time() - start_t
            delay = poll_interval_s - elapsed
            if delay > 0:
                self._sleep_with_stop(delay)

    # State accessors for HTTP
    def snapshot_status(self) -> Dict[str, Any]:
        with self._lock:
            last_read_iso = None
            if self._last_read_at is not None:
                last_read_iso = datetime.fromtimestamp(self._last_read_at, tz=timezone.utc).isoformat()
            return {
                'connected': bool(self._connected),
                'port': self._current_port,
                'slave_id': self._current_slave_id,
                'poll': {
                    'enabled': bool(self._poll_enabled),
                    'interval_ms': self.cfg.poll_interval_ms,
                },
                'last_read_at': last_read_iso,
                'last_error': self._last_error,
                'last_values': self._last_values.copy() if self._last_values else None,
            }

    def is_connected_to(self, port: Optional[str], slave_id: Optional[int]) -> bool:
        with self._lock:
            return bool(
                self._connected and self._current_port == port and self._current_slave_id == slave_id
            )


# Initialize
cfg: Config = load_config_from_env()
poller = ModbusPoller(cfg)
poller.start()

app = Flask(__name__)


@app.route('/status', methods=['GET'])
def status():
    return jsonify(poller.snapshot_status())


@app.route('/connect', methods=['POST'])
def connect():
    try:
        body = request.get_json(silent=True) or {}
    except Exception:
        body = {}

    req_port = body.get('port')
    req_slave = body.get('slave_id')

    # Determine effective target (request overrides env if provided)
    eff_port = req_port if req_port else cfg.modbus_port
    eff_slave = int(req_slave) if req_slave is not None else cfg.slave_id

    already = poller.is_connected_to(eff_port, eff_slave)
    if already:
        msg = {
            'message': 'Already connected to requested device',
            'connected': True,
            'port': eff_port,
            'slave_id': eff_slave,
        }
        return jsonify(msg)

    poller.request_connect(port=eff_port, slave_id=eff_slave)
    snap = poller.snapshot_status()
    resp = {
        'message': 'Connecting (asynchronous). Check /status for state.',
        'requested': {
            'port': eff_port,
            'slave_id': eff_slave,
        },
        'connected': snap.get('connected', False),
    }
    return jsonify(resp)


# Graceful shutdown

def _graceful_shutdown(signum=None, frame=None):
    logger.info("Received shutdown signal: %s", signum)
    poller.stop()


signal.signal(signal.SIGTERM, _graceful_shutdown)
signal.signal(signal.SIGINT, _graceful_shutdown)
atexit.register(poller.stop)


if __name__ == '__main__':
    logger.info("Starting HTTP server on %s:%s", cfg.http_host, cfg.http_port)
    app.run(host=cfg.http_host, port=cfg.http_port, threaded=True, use_reloader=False)
