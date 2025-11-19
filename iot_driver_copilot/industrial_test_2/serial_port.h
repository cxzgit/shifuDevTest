#pragma once
#include <string>
#include <cstdint>

class SerialPort {
public:
    SerialPort();
    ~SerialPort();

    bool openPort(const char* device, int baudrate, char parity, int databits, int stopbits);
    void closePort();

    bool isOpen() const;

    // write all bytes, return true on success
    bool writeAll(const uint8_t* data, size_t len);

    // read exact amount with timeout (ms). returns true on success
    bool readExact(uint8_t* buf, size_t len, int timeout_ms);

    std::string lastError() const { return last_error_; }

private:
    int fd_;
    std::string last_error_;
};
