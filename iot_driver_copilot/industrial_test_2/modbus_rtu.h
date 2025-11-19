#ifndef MODBUS_RTU_H
#define MODBUS_RTU_H

#include <vector>
#include <cstdint>
#include <iostream>
#include <sstream>
#include "serial_port.h"

class ModbusRTU {
public:
    bool readHoldingRegisters(SerialPort &port, uint8_t slave, uint16_t startAddr, uint16_t count, std::vector<uint16_t> &outRegs, int timeout_ms) {
        outRegs.clear();
        uint8_t req[8];
        req[0] = slave;
        req[1] = 0x03;
        req[2] = (uint8_t)((startAddr >> 8) & 0xFF);
        req[3] = (uint8_t)(startAddr & 0xFF);
        req[4] = (uint8_t)((count >> 8) & 0xFF);
        req[5] = (uint8_t)(count & 0xFF);
        uint16_t crc = crc16(req, 6);
        req[6] = (uint8_t)(crc & 0xFF); // CRC low
        req[7] = (uint8_t)((crc >> 8) & 0xFF); // CRC high

        // flush any existing data
        // We rely on tcflush in open; between requests, there may be leftover; not flushing here to avoid losing response of previous request.

        if (!port.writeAll(req, sizeof(req), timeout_ms)) {
            return false;
        }

        // Read first 3 bytes: address, function, byte count
        uint8_t header[3];
        if (!port.readExact(header, 3, timeout_ms)) return false;
        if (header[0] != slave) return false;
        if (header[1] == 0x83) { // exception for function 0x03
            // read remaining exception payload (1 byte + 2 crc)
            uint8_t ex[3];
            port.readExact(ex, 3, timeout_ms);
            return false;
        }
        if (header[1] != 0x03) return false;
        uint8_t byteCount = header[2];
        if (byteCount != count * 2) return false;

        std::vector<uint8_t> payload(byteCount + 2); // data + crc
        if (!port.readExact(payload.data(), payload.size(), timeout_ms)) return false;

        // verify CRC across: addr,function,bytecount,data
        std::vector<uint8_t> full;
        full.reserve(3 + byteCount);
        full.push_back(header[0]);
        full.push_back(header[1]);
        full.push_back(header[2]);
        for (int i = 0; i < byteCount; ++i) full.push_back(payload[i]);
        uint16_t expected = crc16(full.data(), full.size());
        uint16_t received = (uint16_t)payload[byteCount] | ((uint16_t)payload[byteCount + 1] << 8);
        if (expected != received) {
            return false;
        }

        outRegs.resize(count);
        for (int i = 0; i < count; ++i) {
            uint8_t hi = payload[i*2];
            uint8_t lo = payload[i*2 + 1];
            outRegs[i] = ((uint16_t)hi << 8) | (uint16_t)lo;
        }
        return true;
    }

private:
    static uint16_t crc16(const uint8_t *data, size_t len) {
        uint16_t crc = 0xFFFF;
        for (size_t i = 0; i < len; ++i) {
            crc ^= data[i];
            for (int j = 0; j < 8; ++j) {
                if (crc & 0x0001) crc = (crc >> 1) ^ 0xA001;
                else crc = crc >> 1;
            }
        }
        return crc;
    }
};

#endif // MODBUS_RTU_H
