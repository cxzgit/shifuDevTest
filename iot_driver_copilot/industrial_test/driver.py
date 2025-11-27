import json
import logging
import socket
import struct
import threading
import time
import signal
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from dataclasses import dataclass
from datetime import datetime, timezone

from config import load_config


class ModbusError(Exception):
    pass


class ModbusTCPClient:
    def __init__(self, host: str, port: int, unit_id: int, timeout: float, logger: logging.Logger):
        self.host = host
        self.port = port
        self.unit_id = unit_id & 0xFF
        self.timeout = timeout
        self.sock = None
        self.tx_id = 0
        self.logger = logger

    def connect(self):
        self.close()
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(self.timeout)
        s.connect((self.host, self.port))
        self.sock = s
        self.logger.info(f"Connected to Modbus TCP {self.host}:{self.port} unit={self.unit_id}")

    def close(self):
        if self.sock is not None:
            try:
                self.sock.close()
            except Exception:
                pass
            finally:
                self.sock = None

    def _ensure_connected(self):
        if self.sock is None:
            self.connect()

    def _next_tx_id(self) -> int:
        self.tx_id = (self.tx_id + 1) & 0xFFFF
        return self.tx_id

    def _read_exact(self, n: int) -> bytes:
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise ModbusError("Socket closed by peer")
            buf += chunk
        return buf

    def read_holding_registers(self, address: int, quantity: int) -> list:
        if quantity <= 0 or quantity > 125:
            raise ModbusError("Invalid quantity for Read Holding Registers")
        self._ensure_connected()
        tx_id = self._next_tx_id()
        pdu = struct.pack(
            ">BHH", 0x03, address & 0xFFFF, quantity & 0xFFFF
        )
        length = 1 + len(pdu)  # unit id + PDU
        mbap = struct.pack(
            ">HHHB", tx_id, 0x0000, length, self.unit_id
        )
        req = mbap + pdu
        try:
            self.sock.sendall(req)
            # Read MBAP header
            hdr = self._read_exact(7)
            (rx_tx_id, proto, rx_len, rx_unit) = struct.unpack(
                ">HHHB", hdr
            )
            if rx_tx_id != tx_id:
                raise ModbusError("Transaction ID mismatch")
            if proto != 0 or rx_unit != self.unit_id:
                raise ModbusError("Invalid response header")
            pdu_resp = self._read_exact(rx_len - 1)  # pdu only
            # Parse PDU
            fc = pdu_resp[0]
            if fc == 0x83:  # Exception to 0x03
                ex_code = pdu_resp[1]
                raise ModbusError(f"Modbus exception code {ex_code}")
            if fc != 0x03:
                raise ModbusError(f"Unexpected function code {fc}")
            byte_count = pdu_resp[1]
            if byte_count != quantity * 2:
                raise ModbusError("Byte count mismatch")
            data = pdu_resp[2:2 + byte_count]
            regs = [struct.unpack(
                ">H", data[i:i+2]
            )[0] for i in range(0, byte_count, 2)]
            return regs
        except (socket.timeout, OSError) as e:
            self.close()
            raise ModbusError(str(e))


@dataclass
class LatestReading:
    temperature_c: float
    humidity_rh: float
    timestamp: str


class DriverState:
    def __init__(self):
        self.lock = threading.Lock()
        self.connected = False
        self.last_poll_time = None  # ISO 8601 string or None
        self.consecutive_failures = 0
        self.last_error_message = ""
        self.last_device_status = None

    def set_connected(self, connected: bool):
        with self.lock:
            self.connected = connected

    def set_status(self, last_poll_time: str, consecutive_failures: int, last_error_message: str, device_status):
        with self.lock:
            self.last_poll_time = last_poll_time
            self.consecutive_failures = consecutive_failures
            self.last_error_message = last_error_message
            self.last_device_status = device_status

    def snapshot(self):
        with self.lock:
            return {
                "connected": self.connected,
                "last_poll_time": self.last_poll_time,
                "consecutive_failures": self.consecutive_failures,
                "last_error_message": self.last_error_message,
                "last_device_status": self.last_device_status,
            }


class LatestBuffer:
    def __init__(self):
        self.lock = threading.Lock()
        self.reading = None  # LatestReading or None

    def update(self, reading: LatestReading):
        with self.lock:
            self.reading = reading

    def get(self):
        with self.lock:
            return self.reading


def iso_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def poll_loop(cfg, buffer: LatestBuffer, state: DriverState, stop_event: threading.Event, logger: logging.Logger):
    client = ModbusTCPClient(
        host=cfg.modbus_host,
        port=cfg.modbus_port,
        unit_id=cfg.modbus_unit_id,
        timeout=cfg.timeout_ms / 1000.0,
        logger=logger,
    )
    failures = 0

    while not stop_event.is_set():
        success = False
        device_status_val = None
        for attempt in range(1, cfg.retry_count + 1):
            if stop_event.is_set():
                break
            try:
                # Read registers: temperature, humidity, device status
                temp_reg = client.read_holding_registers(cfg.reg_temp_addr, 1)[0]
                hum_reg = client.read_holding_registers(cfg.reg_hum_addr, 1)[0]
                status_reg = client.read_holding_registers(cfg.reg_status_addr, 1)[0]

                # Interpret temperature as signed 16-bit
                if temp_reg >= 0x8000:
                    temp_val = float(temp_reg - 0x10000)
                else:
                    temp_val = float(temp_reg)
                hum_val = float(hum_reg)
                device_status_val = int(status_reg)

                ts = iso_now()
                buffer.update(LatestReading(
                    temperature_c=temp_val,
                    humidity_rh=hum_val,
                    timestamp=ts,
                ))
                state.set_status(
                    last_poll_time=ts,
                    consecutive_failures=0,
                    last_error_message="",
                    device_status=device_status_val,
                )
                if not state.snapshot()["connected"]:
                    logger.info("Device reconnected")
                state.set_connected(True)
                logger.info(f"Polled sensor: T={temp_val}°C H={hum_val}% at {ts}")
                success = True
                failures = 0
                break
            except ModbusError as e:
                failures += 1
                state.set_connected(False)
                err_msg = f"Poll attempt {attempt}/{cfg.retry_count} failed: {e}"
                state.set_status(
                    last_poll_time=state.snapshot()["last_poll_time"],
                    consecutive_failures=failures,
                    last_error_message=str(e),
                    device_status=device_status_val,
                )
                logger.error(err_msg)
                backoff = min(cfg.backoff_initial_seconds * (2 ** (failures - 1)), cfg.backoff_max_seconds)
                # Wait for backoff or until stop
                if stop_event.wait(backoff):
                    break

        # If success, wait sampling interval; otherwise continue (backoff already done above)
        if success:
            if stop_event.wait(cfg.sampling_interval_seconds):
                break
        else:
            # No success in this cycle; continue to next cycle immediately (backoff already applied)
            continue

    # Cleanup
    try:
        client.close()
    except Exception:
        pass
    logger.info("Polling loop stopped")


class RequestHandler(BaseHTTPRequestHandler):
    cfg = None
    buffer = None
    state = None
    logger = None

    def log_message(self, format, *args):
        # Route HTTP handler logs to standard logger
        if self.logger:
            self.logger.info("HTTP %s - %s" % (self.address_string(), format % args))

    def _send_json(self, obj, status=200):
        payload = json.dumps(obj).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == "/readings":
            latest = self.buffer.get()
            if latest is None:
                self._send_json({"error": "no data available yet"}, status=503)
                return
            self._send_json({
                "temperature_c": latest.temperature_c,
                "humidity_rh": latest.humidity_rh,
                "timestamp": latest.timestamp,
            })
            return
        elif self.path == "/config":
            self._send_json({
                "sampling_interval_seconds": self.cfg.sampling_interval_seconds,
                "alarm_thresholds": {
                    "temp_low": self.cfg.alarm_temp_low,
                    "temp_high": self.cfg.alarm_temp_high,
                    "humidity_low": self.cfg.alarm_hum_low,
                    "humidity_high": self.cfg.alarm_hum_high,
                },
                "communication": {
                    "retry_count": self.cfg.retry_count,
                    "timeout_ms": self.cfg.timeout_ms,
                    "backoff_initial_seconds": self.cfg.backoff_initial_seconds,
                    "backoff_max_seconds": self.cfg.backoff_max_seconds,
                },
            })
            return
        elif self.path == "/status":
            st = self.state.snapshot()
            self._send_json({
                "connected": st["connected"],
                "last_poll_time": st["last_poll_time"],
                "sampling_interval_seconds": self.cfg.sampling_interval_seconds,
                "retry_count": self.cfg.retry_count,
                "timeout_ms": self.cfg.timeout_ms,
                "consecutive_failures": st["consecutive_failures"],
                "last_error_message": st["last_error_message"],
            })
            return
        else:
            self._send_json({"error": "not found"}, status=404)


def main():
    cfg = load_config()

    # Configure logging
    level_map = {
        "DEBUG": logging.DEBUG,
        "INFO": logging.INFO,
        "WARNING": logging.WARNING,
        "ERROR": logging.ERROR,
        "CRITICAL": logging.CRITICAL,
    }
    logging.basicConfig(level=level_map.get(cfg.log_level.upper(), logging.INFO), format="%(asctime)s %(levelname)s %(message)s")
    logger = logging.getLogger("driver")

    buffer = LatestBuffer()
    state = DriverState()
    stop_event = threading.Event()

    # Start poll loop
    poll_thread = threading.Thread(target=poll_loop, args=(cfg, buffer, state, stop_event, logger), daemon=True)
    poll_thread.start()

    # Start HTTP server
    RequestHandler.cfg = cfg
    RequestHandler.buffer = buffer
    RequestHandler.state = state
    RequestHandler.logger = logger

    httpd = ThreadingHTTPServer((cfg.http_host, cfg.http_port), RequestHandler)

    def shutdown_handler(signum, frame):
        logger.info(f"Received signal {signum}, shutting down...")
        stop_event.set()
        httpd.shutdown()

    signal.signal(signal.SIGINT, shutdown_handler)
    signal.signal(signal.SIGTERM, shutdown_handler)

    logger.info(f"HTTP server listening on {cfg.http_host}:{cfg.http_port}")
    try:
        httpd.serve_forever()
    finally:
        stop_event.set()
        poll_thread.join(timeout=5)
        logger.info("Driver stopped")


if __name__ == "__main__":
    main()
