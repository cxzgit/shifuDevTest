#include "modbus_rtu.h"
#include <cstring>

ModbusRTU::ModbusRTU(SerialPort* port, int timeout_ms) : port_(port), timeout_ms_(timeout_ms), last_error_("") {}

uint16_t ModbusRTU::crc16(const uint8_t* data, size_t len) {
    uint16_t crc = 0xFFFF;
    for (size_t pos = 0; pos < len; pos++) {
        crc ^= (uint16_t)data[pos];
        for (int i = 0; i < 8; i++) {
            if (crc & 1)
                crc = (crc >> 1) ^ 0xA001;
            else
                crc >>= 1;
        }
    }
    return crc;
}

bool ModbusRTU::readHoldingRegisters(uint8_t slave_id, uint16_t address, uint16_t quantity, std::vector<uint16_t>& out) {
    out.clear();
    if (!port_ || !port_->isOpen()) { last_error_ = "serial not open"; return false; }
    if (quantity < 1 || quantity > 125) { last_error_ = "invalid quantity"; return false; }

    uint8_t req[8];
    req[0] = slave_id;
    req[1] = 0x03; // function code: read holding registers
    req[2] = (uint8_t)((address >> 8) & 0xFF);
    req[3] = (uint8_t)(address & 0xFF);
    req[4] = (uint8_t)((quantity >> 8) & 0xFF);
    req[5] = (uint8_t)(quantity & 0xFF);
    uint16_t crc = crc16(req, 6);
    req[6] = (uint8_t)(crc & 0xFF);       // CRC low
    req[7] = (uint8_t)((crc >> 8) & 0xFF); // CRC high

    // Flush any pending I/O before sending a new request
    // Not all platforms need an explicit flush here; using tcflush in open.

    if (!port_->writeAll(req, sizeof(req))) {
        last_error_ = "write failed";
        return false;
    }

    // Expected response: addr(1) func(1) bytecount(1) data(2*quantity) crc(2)
    size_t expected = 5 + 2 * quantity;
    std::vector<uint8_t> resp(expected);
    if (!port_->readExact(resp.data(), resp.size(), timeout_ms_)) {
        last_error_ = "read timeout";
        return false;
    }

    // Validate address and function
    if (resp[0] != slave_id) { last_error_ = "mismatched slave id"; return false; }
    if (resp[1] == (0x80 | 0x03)) { last_error_ = "modbus exception"; return false; }
    if (resp[1] != 0x03) { last_error_ = "unexpected function"; return false; }

    uint8_t byte_count = resp[2];
    if (byte_count != 2 * quantity) { last_error_ = "unexpected byte count"; return false; }

    // CRC check
    uint16_t rcrc = (uint16_t)resp[expected - 1] << 8 | resp[expected - 2];
    uint16_t ccrc = crc16(resp.data(), expected - 2);
    if (rcrc != ccrc) {
        last_error_ = "crc mismatch";
        return false;
    }

    out.reserve(quantity);
    for (size_t i = 0; i < quantity; ++i) {
        uint16_t hi = resp[3 + i * 2];
        uint16_t lo = resp[3 + i * 2 + 1];
        out.push_back((uint16_t)((hi << 8) | lo));
    }

    last_error_.clear();
    return true;
}
