#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdarg.h>
#include <string.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <termios.h>
#include <time.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <iomanip>
#include <iostream>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

using namespace std::chrono;

// ---------------- Logging ----------------
static std::mutex g_log_mtx;
static void logf(const char* level, const char* fmt, ...) {
    std::lock_guard<std::mutex> lk(g_log_mtx);
    auto now = system_clock::now();
    std::time_t t = system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t, &tm);
    char ts[32];
    std::snprintf(ts, sizeof(ts), "%04d-%02d-%02dT%02d:%02d:%02dZ",
                  tm.tm_year + 1900, tm.tm_mon + 1, tm.tm_mday,
                  tm.tm_hour, tm.tm_min, tm.tm_sec);

    std::fprintf(stderr, "%s [%s] ", ts, level);
    va_list ap; va_start(ap, fmt);
    std::vfprintf(stderr, fmt, ap);
    va_end(ap);
    std::fprintf(stderr, "\n");
}

// ---------------- Utils ----------------
static std::string getenv_str(const char* key, const char* dflt = nullptr) {
    const char* v = std::getenv(key);
    if (!v) return dflt ? std::string(dflt) : std::string();
    return std::string(v);
}

static bool getenv_int(const char* key, int& out, int dflt, bool has_default = true) {
    const char* v = std::getenv(key);
    if (!v) {
        if (has_default) { out = dflt; return true; }
        return false;
    }
    char* end = nullptr;
    long val = std::strtol(v, &end, 10);
    if (end == v || *end != '\0') return false;
    out = static_cast<int>(val);
    return true;
}

static std::string iso8601_now() {
    auto now = system_clock::now();
    std::time_t t = system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t, &tm);
    char buf[64];
    std::snprintf(buf, sizeof(buf), "%04d-%02d-%02dT%02d:%02d:%02dZ",
                  tm.tm_year + 1900, tm.tm_mon + 1, tm.tm_mday,
                  tm.tm_hour, tm.tm_min, tm.tm_sec);
    return std::string(buf);
}

// ---------------- CRC16 (Modbus) ----------------
static uint16_t modbus_crc16(const uint8_t* data, size_t len) {
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < len; ++i) {
        crc ^= data[i];
        for (int j = 0; j < 8; ++j) {
            if (crc & 0x0001) {
                crc = (crc >> 1) ^ 0xA001;
            } else {
                crc >>= 1;
            }
        }
    }
    return crc;
}

// ---------------- Serial Port ----------------
class SerialPort {
public:
    SerialPort() : fd_(-1), read_timeout_ms_(500) {}
    ~SerialPort() { closePort(); }

    bool openPort(const std::string& dev, int baud, int data_bits, char parity, int stop_bits, int read_timeout_ms) {
        read_timeout_ms_ = read_timeout_ms;
        fd_ = ::open(dev.c_str(), O_RDWR | O_NOCTTY | O_NONBLOCK);
        if (fd_ < 0) {
            logf("ERROR", "Failed to open serial port %s: %s", dev.c_str(), strerror(errno));
            return false;
        }
        if (!configure(baud, data_bits, parity, stop_bits)) {
            logf("ERROR", "Failed to configure serial port %s", dev.c_str());
            ::close(fd_);
            fd_ = -1;
            return false;
        }
        // Set blocking mode after configure
        int flags = fcntl(fd_, F_GETFL, 0);
        fcntl(fd_, F_SETFL, flags & ~O_NONBLOCK);
        // Flush buffers
        tcflush(fd_, TCIOFLUSH);
        logf("INFO", "Serial port %s opened", dev.c_str());
        return true;
    }

    void closePort() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
            logf("INFO", "Serial port closed");
        }
    }

    bool isOpen() const { return fd_ >= 0; }

    bool writeAll(const uint8_t* buf, size_t len) {
        size_t written = 0;
        while (written < len) {
            ssize_t n = ::write(fd_, buf + written, len - written);
            if (n < 0) {
                if (errno == EINTR) continue;
                logf("ERROR", "Serial write error: %s", strerror(errno));
                return false;
            }
            written += (size_t)n;
        }
        return true;
    }

    // Read up to len bytes with overall timeout read_timeout_ms_
    ssize_t readSome(uint8_t* buf, size_t len, int timeout_ms) {
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(fd_, &rfds);
        struct timeval tv;
        tv.tv_sec = timeout_ms / 1000;
        tv.tv_usec = (timeout_ms % 1000) * 1000;
        int rv = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
        if (rv < 0) {
            if (errno == EINTR) return 0;
            logf("ERROR", "select() on serial failed: %s", strerror(errno));
            return -1;
        }
        if (rv == 0) return 0; // timeout, no data
        ssize_t n = ::read(fd_, buf, len);
        if (n < 0) {
            if (errno == EINTR) return 0;
            logf("ERROR", "Serial read error: %s", strerror(errno));
            return -1;
        }
        return n;
    }

    bool readExact(uint8_t* buf, size_t len) {
        size_t got = 0;
        auto start = steady_clock::now();
        while (got < len) {
            int elapsed = (int)duration_cast<milliseconds>(steady_clock::now() - start).count();
            int left_ms = read_timeout_ms_ - elapsed;
            if (left_ms <= 0) return false;
            ssize_t n = readSome(buf + got, len - got, left_ms);
            if (n < 0) return false;
            if (n == 0) continue; // timeout portion
            got += (size_t)n;
        }
        return true;
    }

    // Read variable length: first at least 'need', then remaining till 'total'
    bool readAtLeast(std::vector<uint8_t>& out, size_t need) {
        out.clear();
        uint8_t tmp[256];
        auto start = steady_clock::now();
        while (out.size() < need) {
            int elapsed = (int)duration_cast<milliseconds>(steady_clock::now() - start).count();
            int left_ms = read_timeout_ms_ - elapsed;
            if (left_ms <= 0) return false;
            ssize_t n = readSome(tmp, sizeof(tmp), left_ms);
            if (n < 0) return false;
            if (n == 0) continue;
            out.insert(out.end(), tmp, tmp + n);
        }
        return true;
    }

private:
    int fd_;
    int read_timeout_ms_;

    static speed_t baud_to_constant(int baud) {
        switch (baud) {
            case 1200: return B1200;
            case 2400: return B2400;
            case 4800: return B4800;
            case 9600: return B9600;
            case 19200: return B19200;
            case 38400: return B38400;
            case 57600: return B57600;
            case 115200: return B115200;
#ifdef B230400
            case 230400: return B230400;
#endif
            default: return 0;
        }
    }

    bool configure(int baud, int data_bits, char parity, int stop_bits) {
        struct termios tty{};
        if (tcgetattr(fd_, &tty) != 0) {
            logf("ERROR", "tcgetattr failed: %s", strerror(errno));
            return false;
        }

        cfmakeraw(&tty);

        speed_t sp = baud_to_constant(baud);
        if (sp == 0) {
            logf("ERROR", "Unsupported baud rate: %d", baud);
            return false;
        }
        cfsetispeed(&tty, sp);
        cfsetospeed(&tty, sp);

        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_cflag &= ~CSIZE;
        switch (data_bits) {
            case 5: tty.c_cflag |= CS5; break;
            case 6: tty.c_cflag |= CS6; break;
            case 7: tty.c_cflag |= CS7; break;
            case 8: tty.c_cflag |= CS8; break;
            default:
                logf("ERROR", "Unsupported data bits: %d", data_bits);
                return false;
        }

        parity = (char)toupper(parity);
        if (parity == 'N') {
            tty.c_cflag &= ~PARENB;
        } else if (parity == 'E') {
            tty.c_cflag |= PARENB;
            tty.c_cflag &= ~PARODD;
        } else if (parity == 'O') {
            tty.c_cflag |= PARENB;
            tty.c_cflag |= PARODD;
        } else {
            logf("ERROR", "Unsupported parity: %c", parity);
            return false;
        }

        if (stop_bits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

        tty.c_cc[VMIN] = 0;
        tty.c_cc[VTIME] = 0; // we'll use select timeouts

        if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
            logf("ERROR", "tcsetattr failed: %s", strerror(errno));
            return false;
        }
        return true;
    }
};

// ---------------- Modbus RTU ----------------
class ModbusRTU {
public:
    explicit ModbusRTU(SerialPort& port) : port_(port) {}

    bool read_input_registers(uint8_t slave, uint16_t start_addr, uint16_t count, std::vector<uint8_t>& data_bytes) {
        // Build request: [slave][0x04][addr_hi][addr_lo][cnt_hi][cnt_lo][crc_lo][crc_hi]
        uint8_t req[8];
        req[0] = slave;
        req[1] = 0x04; // Read Input Registers
        req[2] = (uint8_t)((start_addr >> 8) & 0xFF);
        req[3] = (uint8_t)(start_addr & 0xFF);
        req[4] = (uint8_t)((count >> 8) & 0xFF);
        req[5] = (uint8_t)(count & 0xFF);
        uint16_t crc = modbus_crc16(req, 6);
        req[6] = (uint8_t)(crc & 0xFF);
        req[7] = (uint8_t)((crc >> 8) & 0xFF);

        // Flush input buffer to avoid stale data
        // tcflush requires fd; we cannot access it; rely on good behavior
        if (!port_.writeAll(req, sizeof(req))) return false;

        // Response: [slave][0x04][byte_count][data...][crc_lo][crc_hi]
        std::vector<uint8_t> hdr;
        if (!port_.readAtLeast(hdr, 3)) {
            logf("WARN", "Modbus: header timeout or error");
            return false;
        }
        if (hdr.size() < 3) return false;
        if (hdr[0] != slave) {
            logf("WARN", "Modbus: slave mismatch (got %u)", hdr[0]);
            return false;
        }
        if ((hdr[1] & 0x80) != 0) {
            // Exception
            // read remaining 2 CRC bytes
            std::vector<uint8_t> rest;
            if (!port_.readAtLeast(rest, 2)) {}
            logf("WARN", "Modbus exception code: %u", hdr.size() >= 3 ? hdr[2] : 0u);
            return false;
        }
        if (hdr[1] != 0x04) {
            logf("WARN", "Modbus: function mismatch (got 0x%02X)", hdr[1]);
            return false;
        }
        uint8_t byte_count = hdr[2];
        std::vector<uint8_t> rest;
        if (!port_.readAtLeast(rest, byte_count + 2)) { // +CRC
            logf("WARN", "Modbus: payload timeout or error");
            return false;
        }
        // Assemble full response for CRC check
        std::vector<uint8_t> full;
        full.reserve(3 + rest.size());
        full.insert(full.end(), hdr.begin(), hdr.end());
        full.insert(full.end(), rest.begin(), rest.end());

        if (full.size() != (size_t)(3 + byte_count + 2)) {
            logf("WARN", "Modbus: invalid response size %zu", full.size());
            return false;
        }
        uint16_t rcrc = (uint16_t)full[full.size() - 2] | ((uint16_t)full[full.size() - 1] << 8);
        uint16_t ccrc = modbus_crc16(full.data(), full.size() - 2);
        if (rcrc != ccrc) {
            logf("WARN", "Modbus: CRC mismatch (rx=0x%04X calc=0x%04X)", rcrc, ccrc);
            return false;
        }
        data_bytes.assign(full.begin() + 3, full.begin() + 3 + byte_count);
        return true;
    }

private:
    SerialPort& port_;
};

// ---------------- Shared sample ----------------
struct Sample {
    double temperature_c = 0.0;
    double humidity_rh = 0.0;
    double pm25_ugm3 = 0.0;
    std::string timestamp;
    bool valid = false;
};

class SampleBuffer {
public:
    void update(const Sample& s) {
        std::lock_guard<std::mutex> lk(mtx_);
        sample_ = s;
    }
    Sample get() {
        std::lock_guard<std::mutex> lk(mtx_);
        return sample_;
    }
private:
    Sample sample_{};
    std::mutex mtx_;
};

// ---------------- Config ----------------
struct Config {
    std::string http_host;
    int http_port;
    std::string serial_port;
    int serial_baud;
    int serial_data_bits;
    char serial_parity;
    int serial_stop_bits;
    int modbus_slave_id;
    int poll_interval_ms;
    int read_timeout_ms;
    int retry_limit;
    int backoff_base_ms;
    int backoff_max_ms;
};

static bool load_config(Config& cfg) {
    // HTTP
    cfg.http_host = getenv_str("HTTP_HOST", "0.0.0.0");
    if (cfg.http_host.empty()) {
        logf("ERROR", "HTTP_HOST not set");
        return false;
    }
    if (!getenv_int("HTTP_PORT", cfg.http_port, 8080, true)) {
        logf("ERROR", "HTTP_PORT not set or invalid");
        return false;
    }

    // Serial
    cfg.serial_port = getenv_str("SERIAL_PORT", nullptr);
    if (cfg.serial_port.empty()) {
        logf("ERROR", "SERIAL_PORT not set (e.g., /dev/ttyUSB0)");
        return false;
    }
    if (!getenv_int("SERIAL_BAUD", cfg.serial_baud, 9600, true)) {
        logf("ERROR", "SERIAL_BAUD invalid");
        return false;
    }
    if (!getenv_int("SERIAL_DATA_BITS", cfg.serial_data_bits, 8, true)) {
        logf("ERROR", "SERIAL_DATA_BITS invalid");
        return false;
    }
    std::string parity = getenv_str("SERIAL_PARITY", "N");
    if (parity.empty()) parity = "N";
    cfg.serial_parity = (char)toupper(parity[0]);
    if (cfg.serial_parity != 'N' && cfg.serial_parity != 'E' && cfg.serial_parity != 'O') {
        logf("ERROR", "SERIAL_PARITY must be N/E/O");
        return false;
    }
    if (!getenv_int("SERIAL_STOP_BITS", cfg.serial_stop_bits, 1, true)) {
        logf("ERROR", "SERIAL_STOP_BITS invalid");
        return false;
    }

    // Modbus
    if (!getenv_int("MODBUS_SLAVE_ID", cfg.modbus_slave_id, 1, true)) {
        logf("ERROR", "MODBUS_SLAVE_ID invalid");
        return false;
    }

    // Polling and timeouts
    if (!getenv_int("POLL_INTERVAL_MS", cfg.poll_interval_ms, 5000, true)) {
        logf("ERROR", "POLL_INTERVAL_MS invalid");
        return false;
    }
    if (!getenv_int("READ_TIMEOUT_MS", cfg.read_timeout_ms, 500, true)) {
        logf("ERROR", "READ_TIMEOUT_MS invalid");
        return false;
    }
    if (!getenv_int("RETRY_LIMIT", cfg.retry_limit, 3, true)) {
        logf("ERROR", "RETRY_LIMIT invalid");
        return false;
    }
    if (cfg.retry_limit > 3) {
        logf("WARN", "RETRY_LIMIT > 3; clamping to 3 to meet device requirements");
        cfg.retry_limit = 3;
    }
    if (!getenv_int("BACKOFF_BASE_MS", cfg.backoff_base_ms, 200, true)) {
        logf("ERROR", "BACKOFF_BASE_MS invalid");
        return false;
    }
    if (!getenv_int("BACKOFF_MAX_MS", cfg.backoff_max_ms, 3000, true)) {
        logf("ERROR", "BACKOFF_MAX_MS invalid");
        return false;
    }
    return true;
}

// ---------------- Poller ----------------
class Poller {
public:
    Poller(const Config& cfg, SampleBuffer& buf) : cfg_(cfg), buf_(buf), port_(), modbus_(port_), running_(false) {}

    void start() {
        running_.store(true);
        worker_ = std::thread([this]{ this->run(); });
    }

    void stop() {
        running_.store(false);
        if (worker_.joinable()) worker_.join();
        port_.closePort();
    }

private:
    void run() {
        int backoff_ms = cfg_.backoff_base_ms;
        int consecutive_failures = 0;

        while (running_.load()) {
            auto loop_start = steady_clock::now();

            if (!port_.isOpen()) {
                if (!port_.openPort(cfg_.serial_port, cfg_.serial_baud, cfg_.serial_data_bits, cfg_.serial_parity, cfg_.serial_stop_bits, cfg_.read_timeout_ms)) {
                    consecutive_failures++;
                    backoff_ms = std::min(cfg_.backoff_max_ms, (consecutive_failures == 1) ? cfg_.backoff_base_ms : backoff_ms * 2);
                    logf("WARN", "Serial open failed; retry in %d ms", backoff_ms);
                    sleep_ms_or_shutdown(backoff_ms);
                    continue;
                } else {
                    consecutive_failures = 0;
                    backoff_ms = cfg_.backoff_base_ms;
                }
            }

            bool success = false;
            std::vector<uint8_t> payload;
            for (int attempt = 1; attempt <= cfg_.retry_limit; ++attempt) {
                if (!running_.load()) break;
                bool ok = modbus_.read_input_registers((uint8_t)cfg_.modbus_slave_id, 0x0001, 6, payload);
                if (ok) { success = true; break; }
                logf("WARN", "Modbus read attempt %d/%d failed", attempt, cfg_.retry_limit);
            }

            if (success) {
                if (payload.size() != 12) {
                    logf("WARN", "Unexpected payload size: %zu", payload.size());
                    consecutive_failures++;
                } else {
                    // Parse: 2 regs per value; Big-endian register order
                    auto parse_float = [&](size_t off) -> double {
                        uint32_t u = ((uint32_t)payload[off] << 24) |
                                     ((uint32_t)payload[off + 1] << 16) |
                                     ((uint32_t)payload[off + 2] << 8) |
                                     (uint32_t)payload[off + 3];
                        float f = 0.0f;
                        std::memcpy(&f, &u, sizeof(float));
                        return (double)f;
                    };

                    Sample s;
                    s.temperature_c = parse_float(0);
                    s.humidity_rh   = parse_float(4);
                    s.pm25_ugm3     = parse_float(8);
                    s.timestamp     = iso8601_now();
                    s.valid         = true;

                    buf_.update(s);
                    consecutive_failures = 0;
                    backoff_ms = cfg_.backoff_base_ms;
                    logf("INFO", "Updated sample: T=%.3f C, RH=%.3f %%RH, PM2.5=%.3f ug/m3", s.temperature_c, s.humidity_rh, s.pm25_ugm3);
                }
            } else {
                consecutive_failures++;
                backoff_ms = std::min(cfg_.backoff_max_ms, (consecutive_failures == 1) ? cfg_.backoff_base_ms : backoff_ms * 2);
                logf("ERROR", "All Modbus attempts failed; backoff %d ms", backoff_ms);
            }

            // Sleep handling
            if (success) {
                auto elapsed = duration_cast<milliseconds>(steady_clock::now() - loop_start).count();
                int sleep_ms = cfg_.poll_interval_ms - (int)elapsed;
                if (sleep_ms < 0) sleep_ms = 0;
                sleep_ms_or_shutdown(sleep_ms);
            } else {
                sleep_ms_or_shutdown(std::min(backoff_ms, cfg_.poll_interval_ms));
            }
        }
    }

    void sleep_ms_or_shutdown(int ms) {
        const int step = 100;
        int remaining = ms;
        while (running_.load() && remaining > 0) {
            int s = remaining > step ? step : remaining;
            std::this_thread::sleep_for(std::chrono::milliseconds(s));
            remaining -= s;
        }
    }

    const Config& cfg_;
    SampleBuffer& buf_;
    SerialPort port_;
    ModbusRTU modbus_;
    std::atomic<bool> running_;
    std::thread worker_;
};

// ---------------- HTTP Server ----------------
class HttpServer {
public:
    HttpServer(const Config& cfg, SampleBuffer& buf) : cfg_(cfg), buf_(buf), running_(false), listen_fd_(-1) {}

    bool start() {
        listen_fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        if (listen_fd_ < 0) {
            logf("ERROR", "socket() failed: %s", strerror(errno));
            return false;
        }
        int yes = 1;
        setsockopt(listen_fd_, SOL_SOCKET, SO_REUSEADDR, &yes, sizeof(yes));

        struct sockaddr_in addr{};
        addr.sin_family = AF_INET;
        addr.sin_port = htons((uint16_t)cfg_.http_port);
        if (cfg_.http_host == "0.0.0.0" || cfg_.http_host == "*" || cfg_.http_host.empty()) {
            addr.sin_addr.s_addr = INADDR_ANY;
        } else {
            if (inet_pton(AF_INET, cfg_.http_host.c_str(), &addr.sin_addr) != 1) {
                logf("ERROR", "Invalid HTTP_HOST: %s", cfg_.http_host.c_str());
                ::close(listen_fd_);
                listen_fd_ = -1;
                return false;
            }
        }
        if (bind(listen_fd_, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
            logf("ERROR", "bind() failed: %s", strerror(errno));
            ::close(listen_fd_);
            listen_fd_ = -1;
            return false;
        }
        if (listen(listen_fd_, 16) < 0) {
            logf("ERROR", "listen() failed: %s", strerror(errno));
            ::close(listen_fd_);
            listen_fd_ = -1;
            return false;
        }
        running_.store(true);
        logf("INFO", "HTTP server listening on %s:%d", cfg_.http_host.c_str(), cfg_.http_port);
        return true;
    }

    void loop() {
        while (running_.load()) {
            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(listen_fd_, &rfds);
            struct timeval tv; tv.tv_sec = 0; tv.tv_usec = 300000; // 300ms
            int rv = select(listen_fd_ + 1, &rfds, nullptr, nullptr, &tv);
            if (rv < 0) {
                if (errno == EINTR) continue;
                logf("ERROR", "select() on listen failed: %s", strerror(errno));
                break;
            }
            if (rv == 0) continue;
            if (!FD_ISSET(listen_fd_, &rfds)) continue;
            int cfd = accept(listen_fd_, nullptr, nullptr);
            if (cfd < 0) {
                if (errno == EINTR) continue;
                logf("WARN", "accept() failed: %s", strerror(errno));
                continue;
            }
            handle_client(cfd);
            ::close(cfd);
        }
    }

    void stop() {
        running_.store(false);
        if (listen_fd_ >= 0) {
            ::shutdown(listen_fd_, SHUT_RDWR);
            ::close(listen_fd_);
            listen_fd_ = -1;
        }
    }

private:
    void handle_client(int cfd) {
        std::string req;
        if (!read_http_request(cfd, req)) {
            return;
        }
        std::string method, path;
        parse_request_line(req, method, path);

        if (method == "GET" && path == "/readings") {
            Sample s = buf_.get();
            if (!s.valid) {
                std::string body = std::string("{\n  \"error\": \"no data yet\",\n  \"timestamp\": \"") + iso8601_now() + "\"\n}\n";
                write_response(cfd, 503, "Service Unavailable", "application/json", body);
                return;
            }
            std::ostringstream oss;
            oss.setf(std::ios::fixed); oss << std::setprecision(3);
            oss << "{\n";
            oss << "  \"temperature_c\": " << s.temperature_c << ",\n";
            oss << "  \"humidity_rh\": " << s.humidity_rh << ",\n";
            oss << "  \"pm25_ugm3\": " << s.pm25_ugm3 << ",\n";
            oss << "  \"timestamp\": \"" << s.timestamp << "\"\n";
            oss << "}\n";
            write_response(cfd, 200, "OK", "application/json; charset=utf-8", oss.str());
        } else {
            const char* notfound = "{\n  \"error\": \"not found\"\n}\n";
            write_response(cfd, 404, "Not Found", "application/json", std::string(notfound));
        }
    }

    bool read_http_request(int cfd, std::string& out) {
        char buf[1024];
        out.clear();
        // Simple read until CRLFCRLF or limit
        for (;;) {
            ssize_t n = ::recv(cfd, buf, sizeof(buf), 0);
            if (n < 0) {
                if (errno == EINTR) continue;
                return false;
            }
            if (n == 0) break;
            out.append(buf, buf + n);
            if (out.size() > 8192) break;
            if (out.find("\r\n\r\n") != std::string::npos || out.find("\n\n") != std::string::npos) break;
        }
        return !out.empty();
    }

    void parse_request_line(const std::string& req, std::string& method, std::string& path) {
        method.clear(); path.clear();
        std::istringstream iss(req);
        std::string line;
        if (!std::getline(iss, line)) return;
        if (!line.empty() && line.back() == '\r') line.pop_back();
        std::istringstream ls(line);
        ls >> method >> path;
        if (path.empty()) path = "/";
    }

    void write_response(int cfd, int status, const char* status_text, const char* ctype, const std::string& body) {
        std::ostringstream hdr;
        hdr << "HTTP/1.1 " << status << " " << status_text << "\r\n";
        hdr << "Content-Type: " << ctype << "\r\n";
        hdr << "Content-Length: " << body.size() << "\r\n";
        hdr << "Cache-Control: no-store\r\n";
        hdr << "Connection: close\r\n\r\n";
        auto hs = hdr.str();
        ::send(cfd, hs.data(), hs.size(), 0);
        ::send(cfd, body.data(), body.size(), 0);
    }

    const Config& cfg_;
    SampleBuffer& buf_;
    std::atomic<bool> running_;
    int listen_fd_;
};

// ---------------- Global control ----------------
static std::atomic<bool> g_running(true);
static void handle_signal(int) {
    g_running.store(false);
}

int main() {
    // Setup signal handlers
    struct sigaction sa{}; sa.sa_handler = handle_signal; sigemptyset(&sa.sa_mask); sa.sa_flags = 0;
    sigaction(SIGINT, &sa, nullptr);
    sigaction(SIGTERM, &sa, nullptr);

    Config cfg{};
    if (!load_config(cfg)) {
        logf("ERROR", "Configuration error; exiting");
        return 1;
    }

    SampleBuffer buffer;
    Poller poller(cfg, buffer);
    poller.start();

    HttpServer server(cfg, buffer);
    if (!server.start()) {
        logf("ERROR", "HTTP server failed to start");
        poller.stop();
        return 1;
    }

    // Main loop waits for signal
    while (g_running.load()) {
        server.loop();
        if (!g_running.load()) break;
    }

    server.stop();
    poller.stop();

    logf("INFO", "Shutdown complete");
    return 0;
}
