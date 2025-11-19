import time
from typing import List

try:
    import serial  # pyserial
except ImportError as e:
    raise ImportError('pyserial is required: pip install pyserial')


class ModbusError(Exception):
    pass


class ModbusRTU:
    def __init__(self, port: str, baudrate: int, bytesize: int, parity: str, stopbits: int, timeout_s: float):
        self.port = port
        self.baudrate = baudrate
        self.bytesize = bytesize
        self.parity = parity
        self.stopbits = stopbits
        self.timeout_s = timeout_s
        self.ser: serial.Serial | None = None

    def open(self):
        if self.ser and self.ser.is_open:
            return
        parity_map = {
            'N': serial.PARITY_NONE,
            'E': serial.PARITY_EVEN,
            'O': serial.PARITY_ODD,
            'M': serial.PARITY_MARK,
            'S': serial.PARITY_SPACE,
        }
        self.ser = serial.Serial(
            port=self.port,
            baudrate=self.baudrate,
            bytesize=self.bytesize,
            parity=parity_map.get(self.parity, serial.PARITY_NONE),
            stopbits=self.stopbits,
            timeout=self.timeout_s,
        )
        # Clean input buffer
        try:
            self.ser.reset_input_buffer()
            self.ser.reset_output_buffer()
        except Exception:
            pass

    def close(self):
        if self.ser:
            try:
                self.ser.close()
            finally:
                self.ser = None

    def is_open(self) -> bool:
        return bool(self.ser and self.ser.is_open)

    @staticmethod
    def _crc16(data: bytes) -> int:
        crc = 0xFFFF
        for b in data:
            crc ^= b
            for _ in range(8):
                if (crc & 0x0001) != 0:
                    crc >>= 1
                    crc ^= 0xA001
                else:
                    crc >>= 1
        return crc & 0xFFFF

    def _write(self, data: bytes):
        assert self.ser is not None
        self.ser.write(data)
        self.ser.flush()

    def _read_exactly(self, n: int, overall_timeout: float) -> bytes:
        assert self.ser is not None
        deadline = time.monotonic() + overall_timeout
        buf = bytearray()
        while len(buf) < n:
            chunk = self.ser.read(n - len(buf))
            if chunk:
                buf.extend(chunk)
                continue
            if time.monotonic() > deadline:
                break
        if len(buf) != n:
            raise TimeoutError(f'timeout reading {n} bytes (got {len(buf)})')
        return bytes(buf)

    def read_holding_registers(self, slave_id: int, start_address: int, quantity: int) -> List[int]:
        if slave_id < 1 or slave_id > 247:
            raise ModbusError('invalid slave_id')
        if quantity < 1 or quantity > 125:
            raise ModbusError('invalid quantity')
        if start_address < 0 or start_address > 0xFFFF:
            raise ModbusError('invalid start_address')

        # Build request: [slave][0x03][addr_hi][addr_lo][qty_hi][qty_lo][crc_lo][crc_hi]
        pdu = bytes([
            0x03,
            (start_address >> 8) & 0xFF,
            start_address & 0xFF,
            (quantity >> 8) & 0xFF,
            quantity & 0xFF,
        ])
        adu_wo_crc = bytes([slave_id]) + pdu
        crc = self._crc16(adu_wo_crc)
        req = adu_wo_crc + bytes([crc & 0xFF, (crc >> 8) & 0xFF])

        self._write(req)

        # Response handling
        # Read address + function first
        header = self._read_exactly(2, self.timeout_s)
        resp_slave = header[0]
        func = header[1]
        if resp_slave != slave_id:
            raise ModbusError(f'unexpected slave id {resp_slave}')

        if func & 0x80:
            # Exception response: next byte is exception code, followed by CRC
            ex_code = self._read_exactly(1, self.timeout_s)[0]
            trailer = self._read_exactly(2, self.timeout_s)
            frame_wo_crc = header + bytes([ex_code])
            crc_calc = self._crc16(frame_wo_crc)
            crc_recv = trailer[0] | (trailer[1] << 8)
            if crc_calc != crc_recv:
                raise ModbusError('CRC mismatch on exception response')
            raise ModbusError(f'Modbus exception code {ex_code}')

        # Normal response: next byte is byte count
        byte_count = self._read_exactly(1, self.timeout_s)[0]
        data = self._read_exactly(byte_count, self.timeout_s)
        crc_bytes = self._read_exactly(2, self.timeout_s)
        frame_wo_crc = header + bytes([byte_count]) + data
        crc_calc = self._crc16(frame_wo_crc)
        crc_recv = crc_bytes[0] | (crc_bytes[1] << 8)
        if crc_calc != crc_recv:
            raise ModbusError('CRC mismatch')

        if byte_count != quantity * 2:
            raise ModbusError(f'unexpected byte count {byte_count}, expected {quantity*2}')

        regs: List[int] = []
        for i in range(0, byte_count, 2):
            regs.append((data[i] << 8) | data[i + 1])
        return regs
