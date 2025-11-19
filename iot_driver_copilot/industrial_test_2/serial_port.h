#ifndef SERIAL_PORT_H
#define SERIAL_PORT_H

#include <string>
#include <cerrno>
#include <cstring>
#include <unistd.h>
#include <fcntl.h>
#include <sys/select.h>
#include <sys/ioctl.h>
#include <termios.h>
#include <iostream>

class SerialPort {
public:
    SerialPort(): fd_(-1) {}
    ~SerialPort() { close(); }

    bool open(const std::string &device, int baud, char parity, int data_bits, int stop_bits) {
        close();
        fd_ = ::open(device.c_str(), O_RDWR | O_NOCTTY | O_SYNC);
        if (fd_ < 0) {
            std::cerr << "open(" << device << ") failed: " << strerror(errno) << std::endl;
            return false;
        }

        struct termios tty;
        memset(&tty, 0, sizeof tty);
        if (tcgetattr(fd_, &tty) != 0) {
            std::cerr << "tcgetattr failed: " << strerror(errno) << std::endl;
            close();
            return false;
        }

        // set raw mode
        cfmakeraw(&tty);

        speed_t speed;
        if (!map_baud(baud, speed)) {
            std::cerr << "Unsupported baud rate: " << baud << std::endl;
            close();
            return false;
        }
        cfsetispeed(&tty, speed);
        cfsetospeed(&tty, speed);

        // data bits
        tty.c_cflag &= ~CSIZE;
        if (data_bits == 7) tty.c_cflag |= CS7; else tty.c_cflag |= CS8;

        // parity
        if (parity == 'N') {
            tty.c_cflag &= ~PARENB;
        } else if (parity == 'E') {
            tty.c_cflag |= PARENB;
            tty.c_cflag &= ~PARODD;
        } else if (parity == 'O') {
            tty.c_cflag |= PARENB;
            tty.c_cflag |= PARODD;
        }

        // stop bits
        if (stop_bits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_cflag &= ~CRTSCTS; // no HW flow control

        // VMIN/VTIME: non-blocking read; we'll use select for timeout
        tty.c_cc[VMIN] = 0;
        tty.c_cc[VTIME] = 0;

        if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
            std::cerr << "tcsetattr failed: " << strerror(errno) << std::endl;
            close();
            return false;
        }

        // flush both
        tcflush(fd_, TCIOFLUSH);
        return true;
    }

    void close() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
        }
    }

    bool isOpen() const { return fd_ >= 0; }

    bool writeAll(const uint8_t *data, size_t len, int timeout_ms) {
        if (fd_ < 0) return false;
        size_t total = 0;
        auto start = now_ms();
        while (total < len) {
            int remaining = timeout_ms - (int)(now_ms() - start);
            if (remaining <= 0) return false;
            fd_set wfds; FD_ZERO(&wfds); FD_SET(fd_, &wfds);
            struct timeval tv; tv.tv_sec = remaining / 1000; tv.tv_usec = (remaining % 1000) * 1000;
            int r = select(fd_ + 1, NULL, &wfds, NULL, &tv);
            if (r <= 0) return false;
            ssize_t w = ::write(fd_, data + total, len - total);
            if (w < 0) {
                if (errno == EINTR) continue;
                return false;
            }
            total += (size_t)w;
        }
        return true;
    }

    bool readExact(uint8_t *buf, size_t len, int total_timeout_ms) {
        if (fd_ < 0) return false;
        size_t readn = 0;
        auto start = now_ms();
        while (readn < len) {
            int remaining = total_timeout_ms - (int)(now_ms() - start);
            if (remaining <= 0) return false;
            fd_set rfds; FD_ZERO(&rfds); FD_SET(fd_, &rfds);
            struct timeval tv; tv.tv_sec = remaining / 1000; tv.tv_usec = (remaining % 1000) * 1000;
            int r = select(fd_ + 1, &rfds, NULL, NULL, &tv);
            if (r <= 0) return false;
            ssize_t got = ::read(fd_, buf + readn, len - readn);
            if (got < 0) {
                if (errno == EINTR) continue;
                return false;
            }
            if (got == 0) continue; // no data yet
            readn += (size_t)got;
        }
        return true;
    }

private:
    int fd_;

    static long long now_ms() {
        struct timeval tv; gettimeofday(&tv, NULL);
        return (long long)tv.tv_sec * 1000LL + tv.tv_usec / 1000;
    }

    static bool map_baud(int baud, speed_t &out) {
        switch (baud) {
            case 9600: out = B9600; return true;
            case 19200: out = B19200; return true;
            case 38400: out = B38400; return true;
            case 57600: out = B57600; return true;
            case 115200: out = B115200; return true;
            default: return false;
        }
    }
};

#endif // SERIAL_PORT_H
