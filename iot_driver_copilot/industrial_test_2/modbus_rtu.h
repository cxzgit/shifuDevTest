#pragma once
#include <cstdint>
#include <vector>
#include <string>
#include "serial_port.h"

class ModbusRTU {
public:
    ModbusRTU(SerialPort* port, int timeout_ms);

    bool readHoldingRegisters(uint8_t slave_id, uint16_t address, uint16_t quantity, std::vector<uint16_t>& out);

    std::string lastError() const { return last_error_; }

private:
    SerialPort* port_;
    int timeout_ms_;
    std::string last_error_;

    static uint16_t crc16(const uint8_t* data, size_t len);
};
