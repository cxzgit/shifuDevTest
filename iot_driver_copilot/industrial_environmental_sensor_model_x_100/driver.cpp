#include <iostream>
#include <string>
#include <vector>
#include <thread>
#include <mutex>
#include <atomic>
#include <chrono>
#include <ctime>
#include <cstring>
#include <csignal>
#include <cerrno>

#include <unistd.h>
#include <fcntl.h>
#include <termios.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>

// ============================
// Config
// ============================
struct Config {
    std::string http_host = "0.0.0.0";
    int http_port = 8080;

    std::string serial_port = "/dev/ttyUSB0";
    int baud_rate = 9600;
    int data_bits = 8;
    char parity = 'N';
    int stop_bits = 1;

    int modbus_address = 1; // Default 0x01

    int poll_interval_ms = 5000; // 5 seconds
    int read_timeout_ms = 1500; // sensor response <2s
    int retry_limit = 3; // CRC/retry limit

    int backoff_base_ms = 500;
    int backoff_max_ms = 10000;
};

static std::atomic<bool> g_running(true);

static std::string now_iso8601() {
    using namespace std::chrono;
    auto now = system_clock::now();
    std::time_t t = system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t, &tm);
    char buf[64];
    std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

static void log(const std::string &msg) {
    std::cerr << now_iso8601() << " | " << msg << std::endl;
}

static const char* getenv_str(const char* name) {
    const char* v = std::getenv(name);
    return v && *v ? v : nullptr;
}

static int getenv_int(const char* name, int def) {
    const char* v = std::getenv(name);
    if (!v || !*v) return def;
    try {
        return std::stoi(v);
    } catch (...) {
        return def;
    }
}

static Config load_config() {
    Config c;
    if (const char* v = getenv_str("HTTP_HOST")) c.http_host = v;
    c.http_port = getenv_int("HTTP_PORT", c.http_port);

    if (const char* v = getenv_str("SERIAL_PORT")) c.serial_port = v;
    c.baud_rate = getenv_int("BAUD_RATE", c.baud_rate);
    c.data_bits = getenv_int("DATA_BITS", c.data_bits);
    if (const char* v = getenv_str("PARITY")) c.parity = *v; // 'N', 'E', 'O'
    c.stop_bits = getenv_int("STOP_BITS", c.stop_bits);

    c.modbus_address = getenv_int("MODBUS_ADDRESS", c.modbus_address);

    c.poll_interval_ms = getenv_int("POLL_INTERVAL_MS", c.poll_interval_ms);
    c.read_timeout_ms = getenv_int("READ_TIMEOUT_MS", c.read_timeout_ms);
    c.retry_limit = getenv_int("RETRY_LIMIT", c.retry_limit);

    c.backoff_base_ms = getenv_int("BACKOFF_BASE_MS", c.backoff_base_ms);
    c.backoff_max_ms = getenv_int("BACKOFF_MAX_MS", c.backoff_max_ms);

    return c;
}

// ============================
// Serial & Modbus RTU helpers
// ============================
static speed_t baud_to_speed(int baud) {
    switch (baud) {
        case 9600: return B9600;
        case 1200: return B1200;
        case 2400: return B2400;
        case 4800: return B4800;
        case 19200: return B19200;
        case 38400: return B38400;
        case 57600: return B57600;
        case 115200: return B115200;
        default: return B9600;
    }
}

class SerialPort {
public:
    SerialPort() : fd_(-1) {}

    bool open_port(const std::string &path, int baud_rate, int data_bits, char parity, int stop_bits) {
        close_port();
        fd_ = ::open(path.c_str(), O_RDWR | O_NOCTTY | O_NONBLOCK);
        if (fd_ < 0) {
            log("Serial open failed: " + std::string(strerror(errno)));
            return false;
        }

        struct termios tty{};
        if (tcgetattr(fd_, &tty) != 0) {
            log("tcgetattr failed: " + std::string(strerror(errno)));
            close_port();
            return false;
        }

        cfmakeraw(&tty);
        speed_t spd = baud_to_speed(baud_rate);
        cfsetispeed(&tty, spd);
        cfsetospeed(&tty, spd);

        tty.c_cflag &= ~CSIZE;
        switch (data_bits) {
            case 5: tty.c_cflag |= CS5; break;
            case 6: tty.c_cflag |= CS6; break;
            case 7: tty.c_cflag |= CS7; break;
            case 8: default: tty.c_cflag |= CS8; break;
        }

        tty.c_cflag &= ~PARENB;
        tty.c_cflag &= ~PARODD;
        if (parity == 'E' || parity == 'e') {
            tty.c_cflag |= PARENB; // even
            tty.c_cflag &= ~PARODD;
        } else if (parity == 'O' || parity == 'o') {
            tty.c_cflag |= PARENB; // odd
            tty.c_cflag |= PARODD;
        } // else 'N' no parity

        if (stop_bits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_iflag = 0;
        tty.c_oflag = 0;
        tty.c_lflag = 0;
        tty.c_cc[VMIN]  = 0;
        tty.c_cc[VTIME] = 0; // We'll use select for timeouts

        if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
            log("tcsetattr failed: " + std::string(strerror(errno)));
            close_port();
            return false;
        }

        // Set blocking mode
        int flags = fcntl(fd_, F_GETFL, 0);
        fcntl(fd_, F_SETFL, flags & ~O_NONBLOCK);

        tcflush(fd_, TCIOFLUSH);
        log("Serial port opened: " + path);
        return true;
    }

    void close_port() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
            log("Serial port closed");
        }
    }

    bool is_open() const { return fd_ >= 0; }

    bool write_all(const uint8_t* buf, size_t len) {
        if (fd_ < 0) return false;
        size_t sent = 0;
        while (sent < len) {
            ssize_t n = ::write(fd_, buf + sent, len - sent);
            if (n < 0) {
                if (errno == EINTR) continue;
                log(std::string("write failed: ") + strerror(errno));
                return false;
            }
            sent += (size_t)n;
        }
        tcdrain(fd_);
        return true;
    }

    bool read_exact(uint8_t* buf, size_t len, int timeout_ms) {
        if (fd_ < 0) return false;
        size_t recvd = 0;
        auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeout_ms);
        while (recvd < len) {
            auto now = std::chrono::steady_clock::now();
            if (now >= deadline) {
                log("read timeout");
                return false;
            }
            int remain_ms = (int)std::chrono::duration_cast<std::chrono::milliseconds>(deadline - now).count();
            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(fd_, &rfds);
            struct timeval tv{};
            tv.tv_sec = remain_ms / 1000;
            tv.tv_usec = (remain_ms % 1000) * 1000;
            int rv = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
            if (rv < 0) {
                if (errno == EINTR) continue;
                log(std::string("select failed: ") + strerror(errno));
                return false;
            } else if (rv == 0) {
                log("select timeout");
                return false;
            }
            ssize_t n = ::read(fd_, buf + recvd, len - recvd);
            if (n < 0) {
                if (errno == EINTR) continue;
                log(std::string("read failed: ") + strerror(errno));
                return false;
            } else if (n == 0) {
                // No data
                continue;
            }
            recvd += (size_t)n;
        }
        return true;
    }

private:
    int fd_;
};

static uint16_t modbus_crc16(const uint8_t* data, size_t len) {
    uint16_t crc = 0xFFFF;
    for (size_t pos = 0; pos < len; pos++) {
        crc ^= (uint16_t)data[pos];
        for (int i = 0; i < 8; i++) {
            if (crc & 0x0001) {
                crc >>= 1;
                crc ^= 0xA001;
            } else {
                crc >>= 1;
            }
        }
    }
    return crc;
}

class ModbusRTU {
public:
    ModbusRTU() {}
    bool open(const Config& cfg) {
        return sp_.open_port(cfg.serial_port, cfg.baud_rate, cfg.data_bits, cfg.parity, cfg.stop_bits);
    }
    void close() { sp_.close_port(); }
    bool is_open() const { return sp_.is_open(); }

    // Read Input Registers (function 0x04)
    bool read_input_registers(uint8_t slave, uint16_t start_addr, uint16_t quantity, std::vector<uint16_t>& out_regs, int timeout_ms) {
        if (!sp_.is_open()) return false;
        uint8_t req[8];
        req[0] = slave;
        req[1] = 0x04;
        req[2] = (uint8_t)((start_addr >> 8) & 0xFF);
        req[3] = (uint8_t)(start_addr & 0xFF);
        req[4] = (uint8_t)((quantity >> 8) & 0xFF);
        req[5] = (uint8_t)(quantity & 0xFF);
        uint16_t crc = modbus_crc16(req, 6);
        req[6] = (uint8_t)(crc & 0xFF); // CRC Low
        req[7] = (uint8_t)((crc >> 8) & 0xFF); // CRC High

        if (!sp_.write_all(req, sizeof(req))) {
            log("Modbus write failed");
            return false;
        }

        // Response: slave (1), func (1), byte count (1), data (N), CRC (2)
        uint8_t hdr[3];
        if (!sp_.read_exact(hdr, sizeof(hdr), timeout_ms)) return false;
        if (hdr[0] != slave) {
            log("Modbus response slave mismatch");
            return false;
        }
        if (hdr[1] == 0x84) {
            // Exception code
            uint8_t ex[3];
            if (!sp_.read_exact(ex, 3, timeout_ms)) return false; // 1 byte ex code + 2 CRC
            log("Modbus exception: func 0x84 code " + std::to_string(ex[0]));
            return false;
        }
        if (hdr[1] != 0x04) {
            log("Modbus response function mismatch");
            return false;
        }
        uint8_t byte_count = hdr[2];
        size_t data_len = byte_count;
        size_t total_len = 3 + data_len + 2;
        std::vector<uint8_t> resp(3 + data_len + 2);
        resp[0] = hdr[0]; resp[1] = hdr[1]; resp[2] = hdr[2];
        if (!sp_.read_exact(resp.data() + 3, data_len + 2, timeout_ms)) return false;

        // CRC check
        uint16_t resp_crc = (uint16_t)resp[3 + data_len] | ((uint16_t)resp[3 + data_len + 1] << 8);
        uint16_t calc_crc = modbus_crc16(resp.data(), 3 + data_len);
        if (resp_crc != calc_crc) {
            log("Modbus CRC error");
            return false;
        }
        if (data_len % 2 != 0) {
            log("Modbus invalid byte count");
            return false;
        }
        size_t reg_count = data_len / 2;
        out_regs.resize(reg_count);
        for (size_t i = 0; i < reg_count; ++i) {
            uint16_t hi = resp[3 + i*2];
            uint16_t lo = resp[3 + i*2 + 1];
            out_regs[i] = (uint16_t)((hi << 8) | lo);
        }
        return true;
    }

private:
    SerialPort sp_;
};

// ============================
// Shared reading buffer
// ============================
struct Reading {
    double temperature_c = 0.0;
    double humidity_rh = 0.0;
    double pm25_ug_m3 = 0.0;
    std::string timestamp;
    bool has_data = false;
    std::string status = "no_data"; // ok, error, no_data, stale
};

class ReadingBuffer {
public:
    void update(double t, double h, double p) {
        std::lock_guard<std::mutex> lk(mu_);
        last_.temperature_c = t;
        last_.humidity_rh = h;
        last_.pm25_ug_m3 = p;
        last_.timestamp = now_iso8601();
        last_.has_data = true;
        last_.status = "ok";
        last_update_ = std::chrono::steady_clock::now();
    }

    void mark_error(const std::string &err) {
        std::lock_guard<std::mutex> lk(mu_);
        last_.status = err;
        // keep last_.has_data, last values intact
    }

    Reading snapshot() const {
        std::lock_guard<std::mutex> lk(mu_);
        return last_;
    }

    std::string to_json() const {
        std::lock_guard<std::mutex> lk(mu_);
        if (!last_.has_data) {
            std::string ts = now_iso8601();
            return std::string("{\"status\":\"no_data\",\"message\":\"no successful poll yet\",\"timestamp\":\"") + ts + "\"}";
        }
        char buf[512];
        std::snprintf(buf, sizeof(buf),
            "{\"temperature_c\":%.3f,\"humidity_rh\":%.3f,\"pm25_ug_m3\":%.3f,\"timestamp\":\"%s\"}",
            last_.temperature_c, last_.humidity_rh, last_.pm25_ug_m3, last_.timestamp.c_str());
        return std::string(buf);
    }

private:
    mutable std::mutex mu_;
    Reading last_;
    std::chrono::steady_clock::time_point last_update_{};
};

// ============================
// Poller thread
// ============================
static float regs_to_float_be(uint16_t hi, uint16_t lo) {
    // Big-endian word order: [hi_hi][hi_lo][lo_hi][lo_lo]
    uint8_t bytes[4];
    bytes[0] = (uint8_t)((hi >> 8) & 0xFF);
    bytes[1] = (uint8_t)(hi & 0xFF);
    bytes[2] = (uint8_t)((lo >> 8) & 0xFF);
    bytes[3] = (uint8_t)(lo & 0xFF);
    float f;
    std::memcpy(&f, bytes, sizeof(float));
    return f;
}

void poller_loop(const Config& cfg, ReadingBuffer* buffer) {
    ModbusRTU modbus;
    int backoff = cfg.backoff_base_ms;

    auto ensure_open = [&]() -> bool {
        if (modbus.is_open()) return true;
        if (!modbus.open(cfg)) {
            log("Failed to open serial port; will backoff");
            return false;
        }
        backoff = cfg.backoff_base_ms; // reset backoff after successful open
        return true;
    };

    while (g_running.load()) {
        if (!ensure_open()) {
            std::this_thread::sleep_for(std::chrono::milliseconds(backoff));
            backoff = std::min(cfg.backoff_max_ms, backoff * 2);
            continue;
        }

        // Read 6 input registers starting at 0x0001: Temp (2), Hum (2), PM2.5 (2)
        bool success = false;
        std::vector<uint16_t> regs;
        for (int attempt = 1; attempt <= cfg.retry_limit && g_running.load(); ++attempt) {
            if (modbus.read_input_registers((uint8_t)cfg.modbus_address, 0x0001, 6, regs, cfg.read_timeout_ms)) {
                if (regs.size() == 6) success = true;
            }
            if (success) break;
            log("Read attempt " + std::to_string(attempt) + " failed; retrying");
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }

        if (success) {
            float t_f = regs_to_float_be(regs[0], regs[1]);
            float h_f = regs_to_float_be(regs[2], regs[3]);
            float p_f = regs_to_float_be(regs[4], regs[5]);

            double t = static_cast<double>(t_f);
            double h = static_cast<double>(h_f);
            double p = static_cast<double>(p_f);

            buffer->update(t, h, p);
            log("Updated readings: T=" + std::to_string(t) + "C H=" + std::to_string(h) + "%RH PM2.5=" + std::to_string(p) + " ug/m3");

            // Sleep for poll interval
            std::this_thread::sleep_for(std::chrono::milliseconds(cfg.poll_interval_ms));
        } else {
            buffer->mark_error("error");
            log("Failed to poll sensor; applying backoff");
            std::this_thread::sleep_for(std::chrono::milliseconds(backoff));
            backoff = std::min(cfg.backoff_max_ms, backoff * 2);
        }
    }

    modbus.close();
    log("Poller loop stopped");
}

// ============================
// HTTP server
// ============================
class HttpServer {
public:
    HttpServer(const std::string& host, int port, ReadingBuffer* buf)
        : host_(host), port_(port), buf_(buf), server_fd_(-1) {}

    bool start() {
        server_fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        if (server_fd_ < 0) {
            log("socket() failed: " + std::string(strerror(errno)));
            return false;
        }
        int opt = 1;
        setsockopt(server_fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

        struct sockaddr_in addr{};
        addr.sin_family = AF_INET;
        addr.sin_port = htons((uint16_t)port_);
        if (host_ == "0.0.0.0") {
            addr.sin_addr.s_addr = INADDR_ANY;
        } else {
            if (inet_pton(AF_INET, host_.c_str(), &addr.sin_addr) != 1) {
                log("Invalid HTTP_HOST; defaulting to 0.0.0.0");
                addr.sin_addr.s_addr = INADDR_ANY;
            }
        }

        if (bind(server_fd_, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
            log("bind() failed: " + std::string(strerror(errno)));
            ::close(server_fd_);
            server_fd_ = -1;
            return false;
        }
        if (listen(server_fd_, 16) < 0) {
            log("listen() failed: " + std::string(strerror(errno)));
            ::close(server_fd_);
            server_fd_ = -1;
            return false;
        }
        log("HTTP server listening on " + host_ + ":" + std::to_string(port_));
        return true;
    }

    void serve() {
        while (g_running.load()) {
            struct sockaddr_in cli{};
            socklen_t clilen = sizeof(cli);
            int cfd = ::accept(server_fd_, (struct sockaddr*)&cli, &clilen);
            if (cfd < 0) {
                if (!g_running.load()) break;
                if (errno == EINTR) continue;
                log("accept() failed: " + std::string(strerror(errno)));
                continue;
            }
            handle_client(cfd);
            ::close(cfd);
        }
        log("HTTP server stopped");
    }

    void stop() {
        if (server_fd_ >= 0) {
            ::shutdown(server_fd_, SHUT_RDWR);
            ::close(server_fd_);
            server_fd_ = -1;
        }
    }

private:
    void handle_client(int cfd) {
        // Read up to 4KB of request
        char buf[4096];
        ssize_t n = ::recv(cfd, buf, sizeof(buf) - 1, 0);
        if (n <= 0) return;
        buf[n] = '\0';
        std::string req(buf);
        // Parse first line
        size_t pos = req.find("\r\n");
        std::string line = (pos == std::string::npos) ? req : req.substr(0, pos);
        // Expect: GET /readings HTTP/1.1
        std::string method, path;
        {
            size_t s1 = line.find(' ');
            if (s1 != std::string::npos) method = line.substr(0, s1);
            size_t s2 = line.find(' ', s1 + 1);
            if (s2 != std::string::npos) path = line.substr(s1 + 1, s2 - (s1 + 1));
        }

        if (method == "GET" && path == "/readings") {
            std::string body = buf_->to_json();
            std::string hdr = "HTTP/1.1 200 OK\r\n";
            hdr += "Content-Type: application/json\r\n";
            hdr += "Connection: close\r\n";
            hdr += "Content-Length: " + std::to_string(body.size()) + "\r\n\r\n";
            ::send(cfd, hdr.c_str(), hdr.size(), 0);
            ::send(cfd, body.c_str(), body.size(), 0);
        } else {
            const char* body = "{\"error\":\"not found\"}";
            std::string hdr = "HTTP/1.1 404 Not Found\r\n";
            hdr += "Content-Type: application/json\r\n";
            hdr += "Connection: close\r\n";
            hdr += std::string("Content-Length: ") + std::to_string(std::strlen(body)) + "\r\n\r\n";
            ::send(cfd, hdr.c_str(), hdr.size(), 0);
            ::send(cfd, body, std::strlen(body), 0);
        }
    }

    std::string host_;
    int port_;
    ReadingBuffer* buf_;
    int server_fd_;
};

// ============================
// Signal handler
// ============================
static void signal_handler(int) {
    g_running.store(false);
}

int main() {
    std::signal(SIGINT, signal_handler);
    std::signal(SIGTERM, signal_handler);

    Config cfg = load_config();

    log("Starting driver for Industrial Environmental Sensor Model X100");
    log("HTTP on " + cfg.http_host + ":" + std::to_string(cfg.http_port));
    log("Serial on " + cfg.serial_port + " baud=" + std::to_string(cfg.baud_rate) + " " + std::to_string(cfg.data_bits) + cfg.parity + std::to_string(cfg.stop_bits));
    log("Modbus address: " + std::to_string(cfg.modbus_address));

    ReadingBuffer buffer;

    std::thread poller([&]() { poller_loop(cfg, &buffer); });

    HttpServer server(cfg.http_host, cfg.http_port, &buffer);
    if (!server.start()) {
        g_running.store(false);
        poller.join();
        return 1;
    }

    server.serve();

    server.stop();
    g_running.store(false);
    if (poller.joinable()) poller.join();

    log("Driver exited");
    return 0;
}
