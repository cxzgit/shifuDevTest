#include "serial_port.h"
#include <termios.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/select.h>
#include <sys/ioctl.h>
#include <cerrno>
#include <cstring>

SerialPort::SerialPort() : fd_(-1), last_error_("") {}
SerialPort::~SerialPort() { closePort(); }

static speed_t baud_to_flag(int baud) {
    switch (baud) {
        case 9600: return B9600;
        case 19200: return B19200;
        case 38400: return B38400;
        case 57600: return B57600;
        case 115200: return B115200;
        default: return B9600; // fallback
    }
}

bool SerialPort::openPort(const char* device, int baudrate, char parity, int databits, int stopbits) {
    closePort();
    fd_ = ::open(device, O_RDWR | O_NOCTTY | O_SYNC);
    if (fd_ < 0) {
        last_error_ = std::string("open failed: ") + std::strerror(errno);
        return false;
    }

    termios tty{};
    if (tcgetattr(fd_, &tty) != 0) {
        last_error_ = std::string("tcgetattr failed: ") + std::strerror(errno);
        closePort();
        return false;
    }

    cfmakeraw(&tty);

    // Set speed
    speed_t speed = baud_to_flag(baudrate);
    cfsetispeed(&tty, speed);
    cfsetospeed(&tty, speed);

    // Control flags
    tty.c_cflag &= ~PARENB;
    tty.c_cflag &= ~CSTOPB;
    tty.c_cflag &= ~CSIZE;

    // databits
    switch (databits) {
        case 5: tty.c_cflag |= CS5; break;
        case 6: tty.c_cflag |= CS6; break;
        case 7: tty.c_cflag |= CS7; break;
        case 8: default: tty.c_cflag |= CS8; break;
    }

    // parity
    if (parity == 'E' || parity == 'e') {
        tty.c_cflag |= PARENB;
        tty.c_cflag &= ~PARODD;
    } else if (parity == 'O' || parity == 'o') {
        tty.c_cflag |= PARENB;
        tty.c_cflag |= PARODD;
    } else {
        tty.c_cflag &= ~PARENB;
    }

    // stopbits
    if (stopbits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

    tty.c_cflag |= (CLOCAL | CREAD);

    // Input flags
    tty.c_iflag = IGNPAR;

    // Output flags
    tty.c_oflag = 0;

    // Local flags
    tty.c_lflag = 0;

    // Read timeout handling will be done with select; set VMIN/VTIME to 0
    tty.c_cc[VMIN] = 0;
    tty.c_cc[VTIME] = 0;

    if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
        last_error_ = std::string("tcsetattr failed: ") + std::strerror(errno);
        closePort();
        return false;
    }

    // Flush I/O
    tcflush(fd_, TCIOFLUSH);

    return true;
}

void SerialPort::closePort() {
    if (fd_ >= 0) {
        ::close(fd_);
        fd_ = -1;
    }
}

bool SerialPort::isOpen() const { return fd_ >= 0; }

bool SerialPort::writeAll(const uint8_t* data, size_t len) {
    if (fd_ < 0) return false;
    size_t written = 0;
    while (written < len) {
        ssize_t w = ::write(fd_, data + written, len - written);
        if (w < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        written += (size_t)w;
    }
    return true;
}

bool SerialPort::readExact(uint8_t* buf, size_t len, int timeout_ms) {
    if (fd_ < 0) return false;
    size_t read_total = 0;
    auto start = std::chrono::steady_clock::now();

    while (read_total < len) {
        // Check timeout
        auto now = std::chrono::steady_clock::now();
        int elapsed_ms = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
        int remain_ms = timeout_ms - elapsed_ms;
        if (remain_ms <= 0) {
            return false;
        }

        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(fd_, &rfds);
        timeval tv;
        tv.tv_sec = remain_ms / 1000;
        tv.tv_usec = (remain_ms % 1000) * 1000;

        int rv = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
        if (rv < 0) {
            if (errno == EINTR) continue;
            return false;
        } else if (rv == 0) {
            // timeout
            return false;
        }

        ssize_t r = ::read(fd_, buf + read_total, len - read_total);
        if (r < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        if (r == 0) {
            // No more data
            continue;
        }
        read_total += (size_t)r;
    }

    return true;
}
