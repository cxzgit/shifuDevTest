#include <iostream>
#include <string>
#include <vector>
#include <thread>
#include <mutex>
#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdlib>
#include <cstdint>
#include <cstring>
#include <cctype>
#include <sstream>
#include <iomanip>
#include <cerrno>

#include <unistd.h>
#include <fcntl.h>
#include <termios.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <netdb.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/select.h>

// Simple logging
static std::mutex g_log_mtx;
static void log_line(const std::string &msg) {
    using namespace std::chrono;
    auto now = system_clock::now();
    auto t = system_clock::to_time_t(now);
    auto ms = duration_cast<milliseconds>(now.time_since_epoch()) % 1000;
    std::tm tm;
    localtime_r(&t, &tm);
    std::ostringstream oss;
    oss << std::put_time(&tm, "%Y-%m-%d %H:%M:%S") << "." << std::setw(3) << std::setfill('0') << ms.count() << " ";
    std::lock_guard<std::mutex> lk(g_log_mtx);
    std::cerr << oss.str() << msg << std::endl;
}

static std::atomic<bool> g_running(true);

static void on_signal(int) {
    g_running.store(false);
}

static std::string iso8601_now() {
    using namespace std::chrono;
    auto now = system_clock::now();
    auto t = system_clock::to_time_t(now);
    std::tm tm;
    gmtime_r(&t, &tm);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

static std::string to_iso8601(const std::chrono::system_clock::time_point &tp) {
    using namespace std::chrono;
    auto t = system_clock::to_time_t(tp);
    std::tm tm;
    gmtime_r(&t, &tm);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

// Environment helpers
static std::string getEnvString(const char* name) {
    const char* v = std::getenv(name);
    if (!v || std::string(v).empty()) {
        std::ostringstream oss; oss << "Missing environment variable: " << name;
        log_line(oss.str());
        std::exit(1);
    }
    return std::string(v);
}

static long getEnvLong(const char* name) {
    std::string s = getEnvString(name);
    char* end = nullptr;
    errno = 0;
    long val = std::strtol(s.c_str(), &end, 10);
    if (errno != 0 || !end || *end != '\0') {
        std::ostringstream oss; oss << "Invalid integer for " << name << ": " << s;
        log_line(oss.str());
        std::exit(1);
    }
    return val;
}

static double getEnvDouble(const char* name) {
    std::string s = getEnvString(name);
    char* end = nullptr;
    errno = 0;
    double val = std::strtod(s.c_str(), &end);
    if (errno != 0 || !end || *end != '\0') {
        std::ostringstream oss; oss << "Invalid float for " << name << ": " << s;
        log_line(oss.str());
        std::exit(1);
    }
    return val;
}

struct Config {
    std::string http_host;
    int http_port;

    std::string serial_port;
    int serial_baud;
    int serial_data_bits;
    char serial_parity; // 'N','E','O'
    int serial_stop_bits; // 1 or 2

    int modbus_slave_id;

    int reg_temp_addr;
    int reg_hum_addr;
    int reg_co2_addr;

    double scale_temp;
    double scale_hum;
    double scale_co2;

    int poll_interval_ms;
    int serial_read_timeout_ms;

    int initial_backoff_ms;
    int max_backoff_ms;
    double backoff_factor;
};

static Config load_config() {
    Config c;
    c.http_host = getEnvString("HTTP_HOST");
    c.http_port = (int)getEnvLong("HTTP_PORT");

    c.serial_port = getEnvString("SERIAL_PORT");
    c.serial_baud = (int)getEnvLong("SERIAL_BAUD");
    c.serial_data_bits = (int)getEnvLong("SERIAL_DATA_BITS");
    std::string parity = getEnvString("SERIAL_PARITY");
    if (parity.size() != 1 || (parity[0] != 'N' && parity[0] != 'E' && parity[0] != 'O')) {
        log_line("SERIAL_PARITY must be one of N/E/O");
        std::exit(1);
    }
    c.serial_parity = parity[0];
    c.serial_stop_bits = (int)getEnvLong("SERIAL_STOP_BITS");
    if (!(c.serial_stop_bits == 1 || c.serial_stop_bits == 2)) {
        log_line("SERIAL_STOP_BITS must be 1 or 2");
        std::exit(1);
    }

    c.modbus_slave_id = (int)getEnvLong("MODBUS_SLAVE_ID");
    if (c.modbus_slave_id < 1 || c.modbus_slave_id > 247) {
        log_line("MODBUS_SLAVE_ID must be in 1..247");
        std::exit(1);
    }

    c.reg_temp_addr = (int)getEnvLong("REG_TEMP_ADDR");
    c.reg_hum_addr = (int)getEnvLong("REG_HUM_ADDR");
    c.reg_co2_addr = (int)getEnvLong("REG_CO2_ADDR");

    c.scale_temp = getEnvDouble("SCALE_TEMP");
    c.scale_hum = getEnvDouble("SCALE_HUM");
    c.scale_co2 = getEnvDouble("SCALE_CO2");

    c.poll_interval_ms = (int)getEnvLong("POLL_INTERVAL_MS");
    c.serial_read_timeout_ms = (int)getEnvLong("SERIAL_READ_TIMEOUT_MS");

    c.initial_backoff_ms = (int)getEnvLong("INITIAL_BACKOFF_MS");
    c.max_backoff_ms = (int)getEnvLong("MAX_BACKOFF_MS");
    c.backoff_factor = getEnvDouble("BACKOFF_FACTOR");
    if (c.backoff_factor < 1.0) {
        log_line("BACKOFF_FACTOR must be >= 1.0");
        std::exit(1);
    }

    return c;
}

// CRC16 Modbus
static uint16_t modbus_crc16(const uint8_t* data, size_t length) {
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < length; ++i) {
        crc ^= data[i];
        for (int j = 0; j < 8; ++j) {
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

class SerialPort {
public:
    SerialPort(): fd_(-1) {}
    ~SerialPort() { closePort(); }

    bool openPort(const Config& c) {
        closePort();
        int fd = ::open(c.serial_port.c_str(), O_RDWR | O_NOCTTY | O_SYNC);
        if (fd < 0) {
            std::ostringstream oss; oss << "Failed to open serial port " << c.serial_port << ": " << strerror(errno);
            log_line(oss.str());
            return false;
        }
        struct termios tty;
        memset(&tty, 0, sizeof tty);
        if (tcgetattr(fd, &tty) != 0) {
            std::ostringstream oss; oss << "tcgetattr failed: " << strerror(errno);
            log_line(oss.str());
            ::close(fd);
            return false;
        }

        // Set speed
        speed_t speed;
        switch (c.serial_baud) {
            case 1200: speed = B1200; break;
            case 2400: speed = B2400; break;
            case 4800: speed = B4800; break;
            case 9600: speed = B9600; break;
            case 19200: speed = B19200; break;
            case 38400: speed = B38400; break;
            case 57600: speed = B57600; break;
            case 115200: speed = B115200; break;
#ifdef B230400
            case 230400: speed = B230400; break;
#endif
            default:
                log_line("Unsupported baud rate");
                ::close(fd);
                return false;
        }
        cfsetospeed(&tty, speed);
        cfsetispeed(&tty, speed);

        // Raw mode
        tty.c_cflag = (tty.c_cflag & ~CSIZE);
        switch (c.serial_data_bits) {
            case 5: tty.c_cflag |= CS5; break;
            case 6: tty.c_cflag |= CS6; break;
            case 7: tty.c_cflag |= CS7; break;
            case 8: tty.c_cflag |= CS8; break;
            default:
                log_line("Unsupported data bits");
                ::close(fd);
                return false;
        }

        // Parity
        if (c.serial_parity == 'N') {
            tty.c_cflag &= ~PARENB;
        } else if (c.serial_parity == 'E') {
            tty.c_cflag |= PARENB;
            tty.c_cflag &= ~PARODD;
        } else { // 'O'
            tty.c_cflag |= PARENB;
            tty.c_cflag |= PARODD;
        }

        // Stop bits
        if (c.serial_stop_bits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

        tty.c_cflag |= CLOCAL | CREAD;
        tty.c_iflag &= ~(IXON | IXOFF | IXANY);
        tty.c_iflag &= ~(IGNBRK | BRKINT | PARMRK | ISTRIP | INLCR | IGNCR | ICRNL);
        tty.c_lflag &= ~(ECHO | ECHONL | ICANON | ISIG | IEXTEN);
        tty.c_oflag &= ~OPOST;

        tty.c_cc[VMIN] = 0;  // non-blocking read
        tty.c_cc[VTIME] = 0; // we'll use select for timeout

        if (tcsetattr(fd, TCSANOW, &tty) != 0) {
            std::ostringstream oss; oss << "tcsetattr failed: " << strerror(errno);
            log_line(oss.str());
            ::close(fd);
            return false;
        }

        fd_ = fd;
        return true;
    }

    void closePort() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
        }
    }

    bool isOpen() const { return fd_ >= 0; }

    bool writeAll(const uint8_t* data, size_t len) {
        if (fd_ < 0) return false;
        size_t written = 0;
        while (written < len) {
            ssize_t w = ::write(fd_, data + written, len - written);
            if (w < 0) {
                if (errno == EINTR) continue;
                std::ostringstream oss; oss << "Serial write error: " << strerror(errno);
                log_line(oss.str());
                return false;
            }
            written += (size_t)w;
        }
        return true;
    }

    bool readExact(uint8_t* buf, size_t len, int timeout_ms) {
        if (fd_ < 0) return false;
        size_t read_total = 0;
        auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeout_ms);
        while (read_total < len) {
            auto now = std::chrono::steady_clock::now();
            if (now >= deadline) {
                return false; // timeout
            }
            int remaining_ms = (int)std::chrono::duration_cast<std::chrono::milliseconds>(deadline - now).count();
            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(fd_, &rfds);
            struct timeval tv;
            tv.tv_sec = remaining_ms / 1000;
            tv.tv_usec = (remaining_ms % 1000) * 1000;
            int rv = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
            if (rv < 0) {
                if (errno == EINTR) continue;
                std::ostringstream oss; oss << "Serial select error: " << strerror(errno);
                log_line(oss.str());
                return false;
            } else if (rv == 0) {
                return false; // timeout
            }
            ssize_t r = ::read(fd_, buf + read_total, len - read_total);
            if (r < 0) {
                if (errno == EINTR) continue;
                std::ostringstream oss; oss << "Serial read error: " << strerror(errno);
                log_line(oss.str());
                return false;
            } else if (r == 0) {
                // No data
                continue;
            }
            read_total += (size_t)r;
        }
        return true;
    }

private:
    int fd_;
};

class ModbusRTU {
public:
    explicit ModbusRTU(SerialPort &sp) : sp_(sp) {}

    bool readHoldingRegisters(uint8_t slave, uint16_t addr, uint16_t count, std::vector<uint16_t>& regs, int timeout_ms) {
        // Build request: slave, 0x03, addr_hi, addr_lo, count_hi, count_lo, crc_lo, crc_hi
        std::vector<uint8_t> req;
        req.reserve(8);
        req.push_back(slave);
        req.push_back(0x03);
        req.push_back((uint8_t)((addr >> 8) & 0xFF));
        req.push_back((uint8_t)(addr & 0xFF));
        req.push_back((uint8_t)((count >> 8) & 0xFF));
        req.push_back((uint8_t)(count & 0xFF));
        uint16_t crc = modbus_crc16(req.data(), req.size());
        req.push_back((uint8_t)(crc & 0xFF));
        req.push_back((uint8_t)((crc >> 8) & 0xFF));

        // Flush I/O to reduce stale data
        // tcflush requires fd, but we keep it private; we will rely on request-response order.

        if (!sp_.writeAll(req.data(), req.size())) {
            return false;
        }
        // Expected response: slave, 0x03, bytecount (2*count), data..., crc_lo, crc_hi
        size_t expect_len = 5 + 2 * count; // 1 + 1 + 1 + 2*count + 2
        std::vector<uint8_t> resp(expect_len);
        if (!sp_.readExact(resp.data(), resp.size(), timeout_ms)) {
            log_line("Modbus read timeout or error");
            return false;
        }
        // Validate basic fields
        if (resp[0] != slave) {
            log_line("Modbus slave mismatch");
            return false;
        }
        if (resp[1] == (uint8_t)(0x80 | 0x03)) {
            log_line("Modbus exception response");
            return false;
        }
        if (resp[1] != 0x03) {
            log_line("Unexpected function code");
            return false;
        }
        uint8_t bc = resp[2];
        if (bc != (uint8_t)(2 * count)) {
            log_line("Unexpected byte count in response");
            return false;
        }
        // Validate CRC
        uint16_t calc_crc = modbus_crc16(resp.data(), resp.size() - 2);
        uint16_t resp_crc = (uint16_t)resp[resp.size() - 2] | ((uint16_t)resp[resp.size() - 1] << 8);
        if (calc_crc != resp_crc) {
            log_line("CRC mismatch in Modbus response");
            return false;
        }
        regs.clear();
        regs.reserve(count);
        for (uint16_t i = 0; i < count; ++i) {
            uint8_t hi = resp[3 + 2*i];
            uint8_t lo = resp[3 + 2*i + 1];
            uint16_t val = ((uint16_t)hi << 8) | (uint16_t)lo;
            regs.push_back(val);
        }
        return true;
    }

private:
    SerialPort &sp_;
};

struct LatestData {
    double temperature = 0.0;
    double humidity = 0.0;
    double co2 = 0.0;
    bool has_temp = false;
    bool has_hum = false;
    bool has_co2 = false;
    std::chrono::system_clock::time_point ts_temp{};
    std::chrono::system_clock::time_point ts_hum{};
    std::chrono::system_clock::time_point ts_co2{};
    std::mutex mtx;
};

struct StatusInfo {
    bool connected = false;
    bool polling_active = false;
    std::string port;
    int slave_id = 0;
    int interval_ms = 0;
    std::chrono::system_clock::time_point last_poll{};
    std::mutex mtx;
};

class Poller {
public:
    Poller(const Config &cfg, LatestData &data, StatusInfo &status)
        : cfg_(cfg), data_(data), status_(status) {}

    void start() {
        th_ = std::thread([this]{ this->run(); });
    }

    void join() {
        if (th_.joinable()) th_.join();
    }

private:
    void run() {
        SerialPort sp;
        ModbusRTU mb(sp);

        int backoff_ms = cfg_.initial_backoff_ms;
        {
            std::lock_guard<std::mutex> lk(status_.mtx);
            status_.polling_active = true;
            status_.port = cfg_.serial_port;
            status_.slave_id = cfg_.modbus_slave_id;
            status_.interval_ms = cfg_.poll_interval_ms;
        }

        while (g_running.load()) {
            if (!sp.isOpen()) {
                log_line("Attempting to open serial port...");
                if (sp.openPort(cfg_)) {
                    log_line("Serial port opened successfully");
                    std::lock_guard<std::mutex> lk(status_.mtx);
                    status_.connected = true;
                    backoff_ms = cfg_.initial_backoff_ms;
                } else {
                    std::lock_guard<std::mutex> lk(status_.mtx);
                    status_.connected = false;
                    log_line("Serial open failed, backing off");
                    sleep_for(backoff_ms);
                    backoff_ms = next_backoff(backoff_ms);
                    continue;
                }
            }

            bool ok = true;
            std::vector<uint16_t> regs;
            if (!mb.readHoldingRegisters((uint8_t)cfg_.modbus_slave_id, (uint16_t)cfg_.reg_temp_addr, 1, regs, cfg_.serial_read_timeout_ms)) {
                ok = false;
            } else {
                double t = (double)regs[0] * cfg_.scale_temp;
                auto now = std::chrono::system_clock::now();
                {
                    std::lock_guard<std::mutex> lk(data_.mtx);
                    data_.temperature = t;
                    data_.has_temp = true;
                    data_.ts_temp = now;
                }
            }

            if (ok) {
                regs.clear();
                if (!mb.readHoldingRegisters((uint8_t)cfg_.modbus_slave_id, (uint16_t)cfg_.reg_hum_addr, 1, regs, cfg_.serial_read_timeout_ms)) {
                    ok = false;
                } else {
                    double h = (double)regs[0] * cfg_.scale_hum;
                    auto now = std::chrono::system_clock::now();
                    {
                        std::lock_guard<std::mutex> lk(data_.mtx);
                        data_.humidity = h;
                        data_.has_hum = true;
                        data_.ts_hum = now;
                    }
                }
            }

            if (ok) {
                regs.clear();
                if (!mb.readHoldingRegisters((uint8_t)cfg_.modbus_slave_id, (uint16_t)cfg_.reg_co2_addr, 1, regs, cfg_.serial_read_timeout_ms)) {
                    ok = false;
                } else {
                    double c = (double)regs[0] * cfg_.scale_co2;
                    auto now = std::chrono::system_clock::now();
                    {
                        std::lock_guard<std::mutex> lk(data_.mtx);
                        data_.co2 = c;
                        data_.has_co2 = true;
                        data_.ts_co2 = now;
                    }
                }
            }

            auto now = std::chrono::system_clock::now();
            {
                std::lock_guard<std::mutex> lk(status_.mtx);
                status_.last_poll = now;
            }

            if (!ok) {
                log_line("Polling error detected; closing serial and backing off");
                sp.closePort();
                {
                    std::lock_guard<std::mutex> lk(status_.mtx);
                    status_.connected = false;
                }
                sleep_for(backoff_ms);
                backoff_ms = next_backoff(backoff_ms);
                continue;
            }

            // Sleep until next poll or shutdown
            int slept = 0;
            while (g_running.load() && slept < cfg_.poll_interval_ms) {
                int step = std::min(100, cfg_.poll_interval_ms - slept);
                sleep_for(step);
                slept += step;
            }
        }
        log_line("Poller stopping");
    }

    void sleep_for(int ms) {
        std::this_thread::sleep_for(std::chrono::milliseconds(ms));
    }

    int next_backoff(int current_ms) {
        double next = (double)current_ms * cfg_.backoff_factor;
        if (next > (double)cfg_.max_backoff_ms) next = (double)cfg_.max_backoff_ms;
        return (int)next;
    }

    const Config &cfg_;
    LatestData &data_;
    StatusInfo &status_;
    std::thread th_;
};

class HttpServer {
public:
    HttpServer(const Config &cfg, LatestData &data, StatusInfo &status) : cfg_(cfg), data_(data), status_(status), listen_fd_(-1) {}

    bool start() {
        struct addrinfo hints; memset(&hints, 0, sizeof(hints));
        hints.ai_family = AF_UNSPEC; // Support IPv4/IPv6 if possible
        hints.ai_socktype = SOCK_STREAM;
        hints.ai_flags = AI_PASSIVE;

        struct addrinfo *res = nullptr;
        int rv = getaddrinfo(cfg_.http_host.c_str(), std::to_string(cfg_.http_port).c_str(), &hints, &res);
        if (rv != 0) {
            std::ostringstream oss; oss << "getaddrinfo: " << gai_strerror(rv);
            log_line(oss.str());
            return false;
        }

        int fd = -1;
        for (struct addrinfo *p = res; p; p = p->ai_next) {
            fd = ::socket(p->ai_family, p->ai_socktype, p->ai_protocol);
            if (fd < 0) continue;
            int opt = 1;
            setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
#ifdef SO_REUSEPORT
            setsockopt(fd, SOL_SOCKET, SO_REUSEPORT, &opt, sizeof(opt));
#endif
            if (::bind(fd, p->ai_addr, p->ai_addrlen) == 0) {
                if (::listen(fd, 16) == 0) {
                    listen_fd_ = fd;
                    freeaddrinfo(res);
                    log_line("HTTP server listening");
                    th_ = std::thread([this]{ this->accept_loop(); });
                    return true;
                }
            }
            ::close(fd);
            fd = -1;
        }
        freeaddrinfo(res);
        log_line("Failed to bind/listen HTTP server");
        return false;
    }

    void stop() {
        if (listen_fd_ >= 0) {
            ::shutdown(listen_fd_, SHUT_RDWR);
            ::close(listen_fd_);
            listen_fd_ = -1;
        }
    }

    void join() {
        if (th_.joinable()) th_.join();
    }

private:
    void accept_loop() {
        while (g_running.load()) {
            fd_set rfds; FD_ZERO(&rfds); FD_SET(listen_fd_, &rfds);
            struct timeval tv; tv.tv_sec = 1; tv.tv_usec = 0;
            int rv = select(listen_fd_+1, &rfds, nullptr, nullptr, &tv);
            if (rv < 0) {
                if (errno == EINTR) continue;
                log_line("HTTP server select error");
                break;
            } else if (rv == 0) {
                continue; // timeout, check running
            }

            struct sockaddr_storage addr; socklen_t addrlen = sizeof(addr);
            int cfd = ::accept(listen_fd_, (struct sockaddr*)&addr, &addrlen);
            if (cfd < 0) {
                if (errno == EINTR) continue;
                if (!g_running.load()) break;
                log_line("HTTP accept error");
                continue;
            }
            std::thread(&HttpServer::handle_client, this, cfd).detach();
        }
        log_line("HTTP server stopping");
    }

    void handle_client(int cfd) {
        // Read request (simple, up to 8KB)
        std::string req;
        req.reserve(8192);
        char buf[1024];
        bool header_done = false;
        auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(5);
        while (!header_done) {
            auto now = std::chrono::steady_clock::now();
            if (now >= deadline) break;
            fd_set rfds; FD_ZERO(&rfds); FD_SET(cfd, &rfds);
            struct timeval tv; tv.tv_sec = 0; tv.tv_usec = 200000;
            int rv = select(cfd+1, &rfds, nullptr, nullptr, &tv);
            if (rv < 0) {
                if (errno == EINTR) continue;
                break;
            } else if (rv == 0) {
                continue;
            }
            ssize_t r = ::read(cfd, buf, sizeof(buf));
            if (r <= 0) break;
            req.append(buf, buf + r);
            if (req.find("\r\n\r\n") != std::string::npos) header_done = true;
            if (req.size() > 8192) break;
        }
        std::string method, path;
        parse_request_line(req, method, path);

        if (method != "GET") {
            std::string body = "{""error"": ""method not allowed""}";
            send_response(cfd, 405, "Method Not Allowed", body);
            ::close(cfd);
            return;
        }

        if (path == "/status") {
            send_status(cfd);
        } else if (path == "/readings") {
            send_readings(cfd);
        } else {
            std::string body = "{""error"": ""not found""}";
            send_response(cfd, 404, "Not Found", body);
        }
        ::close(cfd);
    }

    void parse_request_line(const std::string &req, std::string &method, std::string &path) {
        method.clear(); path.clear();
        size_t pos = req.find("\r\n");
        if (pos == std::string::npos) return;
        std::string line = req.substr(0, pos);
        std::istringstream iss(line);
        iss >> method >> path; // ignore HTTP version
    }

    void send_response(int cfd, int code, const std::string &status, const std::string &body) {
        std::ostringstream oss;
        oss << "HTTP/1.1 " << code << " " << status << "\r\n";
        oss << "Content-Type: application/json\r\n";
        oss << "Content-Length: " << body.size() << "\r\n";
        oss << "Connection: close\r\n\r\n";
        std::string hdr = oss.str();
        ::write(cfd, hdr.data(), hdr.size());
        ::write(cfd, body.data(), body.size());
    }

    void send_status(int cfd) {
        bool connected; bool polling; std::string port; int slave; int interval; std::string last_poll_ts = "null";
        {
            std::lock_guard<std::mutex> lk(status_.mtx);
            connected = status_.connected;
            polling = status_.polling_active;
            port = status_.port;
            slave = status_.slave_id;
            interval = status_.interval_ms;
            if (status_.last_poll.time_since_epoch().count() != 0) {
                last_poll_ts = '"' + to_iso8601(status_.last_poll) + '"';
            }
        }
        std::ostringstream body;
        body << "{";
        body << "\"connected\":" << (connected ? "true" : "false") << ",";
        body << "\"port\":\"" << escape_json(port) << "\",";
        body << "\"slave_id\":" << slave << ",";
        body << "\"polling_active\":" << (polling ? "true" : "false") << ",";
        body << "\"interval_ms\":" << interval << ",";
        body << "\"last_poll_time\":" << last_poll_ts;
        body << "}";
        send_response(cfd, 200, "OK", body.str());
    }

    void send_readings(int cfd) {
        double t=0, h=0, c=0; bool ht=false, hh=false, hc=false; std::string tts="null", hts="null", cts="null";
        {
            std::lock_guard<std::mutex> lk(data_.mtx);
            if (data_.has_temp) { t = data_.temperature; ht = true; tts = '"' + to_iso8601(data_.ts_temp) + '"'; }
            if (data_.has_hum)  { h = data_.humidity;   hh = true; hts = '"' + to_iso8601(data_.ts_hum) + '"'; }
            if (data_.has_co2)  { c = data_.co2;        hc = true; cts = '"' + to_iso8601(data_.ts_co2) + '"'; }
        }
        std::ostringstream body;
        body << "{";
        body << "\"temperature\":" << (ht ? num_to_json(t) : std::string("null")) << ",";
        body << "\"humidity\":" << (hh ? num_to_json(h) : std::string("null")) << ",";
        body << "\"co2\":" << (hc ? num_to_json(c) : std::string("null")) << ",";
        body << "\"temperature_ts\":" << tts << ",";
        body << "\"humidity_ts\":" << hts << ",";
        body << "\"co2_ts\":" << cts;
        body << "}";
        send_response(cfd, 200, "OK", body.str());
    }

    static std::string escape_json(const std::string &s) {
        std::ostringstream oss;
        for (char ch : s) {
            switch (ch) {
                case '"': oss << "\\\""; break;
                case '\\': oss << "\\\\"; break;
                case '\n': oss << "\\n"; break;
                case '\r': oss << "\\r"; break;
                case '\t': oss << "\\t"; break;
                default:
                    if ((unsigned char)ch < 0x20) {
                        oss << "\\u" << std::hex << std::uppercase << std::setw(4) << std::setfill('0') << (int)(unsigned char)ch << std::dec;
                    } else {
                        oss << ch;
                    }
            }
        }
        return oss.str();
    }

    static std::string num_to_json(double v) {
        std::ostringstream oss;
        oss.setf(std::ios::fixed); oss << std::setprecision(6) << v;
        return oss.str();
    }

    const Config &cfg_;
    LatestData &data_;
    StatusInfo &status_;
    int listen_fd_;
    std::thread th_;
};

int main() {
    std::signal(SIGINT, on_signal);
    std::signal(SIGTERM, on_signal);

    Config cfg = load_config();
    log_line("Configuration loaded");

    LatestData data;
    StatusInfo status;

    Poller poller(cfg, data, status);
    poller.start();

    HttpServer server(cfg, data, status);
    if (!server.start()) {
        log_line("Failed to start HTTP server");
        g_running.store(false);
    }

    while (g_running.load()) {
        std::this_thread::sleep_for(std::chrono::milliseconds(200));
    }

    log_line("Shutting down...");
    server.stop();
    server.join();
    poller.join();

    log_line("Exited cleanly");
    return 0;
}
