#include <arpa/inet.h>
#include <chrono>
#include <csignal>
#include <cstring>
#include <fcntl.h>
#include <iomanip>
#include <iostream>
#include <mutex>
#include <netinet/in.h>
#include <poll.h>
#include <sstream>
#include <stdexcept>
#include <string>
#include <sys/socket.h>
#include <sys/types.h>
#include <termios.h>
#include <thread>
#include <unistd.h>
#include <vector>

// Simple logging helpers
static std::mutex g_log_mutex;
static void log_ts() {
    using namespace std::chrono;
    auto now = system_clock::now();
    std::time_t t = system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t, &tm);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    std::cerr << buf << " ";
}
static void log_info(const std::string &msg) {
    std::lock_guard<std::mutex> lk(g_log_mutex);
    log_ts();
    std::cerr << "INFO " << msg << std::endl;
}
static void log_warn(const std::string &msg) {
    std::lock_guard<std::mutex> lk(g_log_mutex);
    log_ts();
    std::cerr << "WARN " << msg << std::endl;
}
static void log_error(const std::string &msg) {
    std::lock_guard<std::mutex> lk(g_log_mutex);
    log_ts();
    std::cerr << "ERROR " << msg << std::endl;
}

struct Config {
    std::string httpHost;
    int httpPort;
    std::string serialPortPath;
    int modbusAddr;
    int baudRate;
    int dataBits;
    char parity; // 'N', 'E', 'O'
    int stopBits; // 1 or 2
    int pollIntervalSec;
    int requestTimeoutMs;
    int retryLimit;
    int backoffInitialMs;
    int backoffMaxMs;
};

static std::string getenv_or_throw(const char *name) {
    const char *v = std::getenv(name);
    if (!v) {
        std::ostringstream oss;
        oss << "Missing required environment variable: " << name;
        throw std::runtime_error(oss.str());
    }
    return std::string(v);
}

static int parse_int_env(const char *name) {
    std::string s = getenv_or_throw(name);
    try {
        size_t idx = 0;
        int val = std::stoi(s, &idx);
        if (idx != s.size()) throw std::invalid_argument("trailing");
        return val;
    } catch (...) {
        std::ostringstream oss;
        oss << "Invalid integer for env " << name << ": " << s;
        throw std::runtime_error(oss.str());
    }
}

static Config load_config() {
    Config c;
    c.httpHost = getenv_or_throw("HTTP_HOST");
    c.httpPort = parse_int_env("HTTP_PORT");
    c.serialPortPath = getenv_or_throw("SERIAL_PORT");
    c.modbusAddr = parse_int_env("MODBUS_ADDR");
    c.baudRate = parse_int_env("BAUD_RATE");
    c.dataBits = parse_int_env("DATA_BITS");
    {
        std::string p = getenv_or_throw("PARITY");
        if (p.size() != 1) throw std::runtime_error("PARITY must be one of N/E/O");
        c.parity = p[0];
    }
    c.stopBits = parse_int_env("STOP_BITS");
    c.pollIntervalSec = parse_int_env("POLL_INTERVAL_SEC");
    c.requestTimeoutMs = parse_int_env("REQUEST_TIMEOUT_MS");
    c.retryLimit = parse_int_env("RETRY_LIMIT");
    c.backoffInitialMs = parse_int_env("BACKOFF_INITIAL_MS");
    c.backoffMaxMs = parse_int_env("BACKOFF_MAX_MS");

    // Basic validation
    if (c.httpPort <= 0 || c.httpPort > 65535) throw std::runtime_error("HTTP_PORT out of range");
    if (c.modbusAddr < 1 || c.modbusAddr > 247) throw std::runtime_error("MODBUS_ADDR must be 1..247");
    if (!(c.dataBits == 7 || c.dataBits == 8)) throw std::runtime_error("DATA_BITS must be 7 or 8");
    if (!(c.parity == 'N' || c.parity == 'E' || c.parity == 'O')) throw std::runtime_error("PARITY must be N/E/O");
    if (!(c.stopBits == 1 || c.stopBits == 2)) throw std::runtime_error("STOP_BITS must be 1 or 2");
    if (c.pollIntervalSec <= 0) throw std::runtime_error("POLL_INTERVAL_SEC must be > 0");
    if (c.requestTimeoutMs <= 0) throw std::runtime_error("REQUEST_TIMEOUT_MS must be > 0");
    if (c.retryLimit <= 0) throw std::runtime_error("RETRY_LIMIT must be > 0");
    if (c.backoffInitialMs <= 0 || c.backoffMaxMs <= 0 || c.backoffInitialMs > c.backoffMaxMs)
        throw std::runtime_error("Backoff values invalid");

    std::ostringstream oss;
    oss << "Config loaded: HTTP " << c.httpHost << ":" << c.httpPort
        << ", Serial " << c.serialPortPath << ", Baud " << c.baudRate
        << ", " << c.dataBits << " data bits, parity " << c.parity << ", stop bits " << c.stopBits
        << ", Modbus addr " << c.modbusAddr
        << ", Poll interval " << c.pollIntervalSec << "s"
        << ", Timeout " << c.requestTimeoutMs << "ms"
        << ", Retry " << c.retryLimit
        << ", Backoff init " << c.backoffInitialMs << "ms max " << c.backoffMaxMs << "ms";
    log_info(oss.str());
    return c;
}

static uint16_t modbus_crc16(const uint8_t *data, size_t length) {
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < length; ++i) {
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

class SerialPort {
public:
    SerialPort() : fd_(-1) {}
    ~SerialPort() { close(); }

    bool open(const std::string &path, int baud, int dataBits, char parity, int stopBits) {
        close();
        fd_ = ::open(path.c_str(), O_RDWR | O_NOCTTY | O_NDELAY);
        if (fd_ < 0) {
            log_error(std::string("Failed to open ") + path + ": " + std::strerror(errno));
            return false;
        }
        fcntl(fd_, F_SETFL, 0); // blocking

        struct termios tty;
        memset(&tty, 0, sizeof tty);
        if (tcgetattr(fd_, &tty) != 0) {
            log_error(std::string("tcgetattr failed: ") + std::strerror(errno));
            close();
            return false;
        }

        cfmakeraw(&tty);
        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_cflag &= ~CRTSCTS; // disable HW flow control
        tty.c_iflag &= ~(IXON | IXOFF | IXANY); // disable SW flow control

        // Data bits
        tty.c_cflag &= ~CSIZE;
        if (dataBits == 7) tty.c_cflag |= CS7; else tty.c_cflag |= CS8;

        // Parity
        if (parity == 'N') {
            tty.c_cflag &= ~PARENB;
        } else if (parity == 'E') {
            tty.c_cflag |= PARENB;
            tty.c_cflag &= ~PARODD;
        } else if (parity == 'O') {
            tty.c_cflag |= PARENB;
            tty.c_cflag |= PARODD;
        }

        // Stop bits
        if (stopBits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

        // Baud rate mapping
        speed_t spd = B0;
        switch (baud) {
            case 1200: spd = B1200; break;
            case 2400: spd = B2400; break;
            case 4800: spd = B4800; break;
            case 9600: spd = B9600; break;
            case 19200: spd = B19200; break;
            case 38400: spd = B38400; break;
            case 57600: spd = B57600; break;
            case 115200: spd = B115200; break;
            default:
                log_error("Unsupported BAUD_RATE. Use one of 1200,2400,4800,9600,19200,38400,57600,115200");
                close();
                return false;
        }
        cfsetispeed(&tty, spd);
        cfsetospeed(&tty, spd);

        // Apply settings
        if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
            log_error(std::string("tcsetattr failed: ") + std::strerror(errno));
            close();
            return false;
        }

        // Flush any stale data
        tcflush(fd_, TCIOFLUSH);
        log_info("Serial port configured");
        return true;
    }

    void close() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
        }
    }

    bool write_all(const uint8_t *buf, size_t len) {
        if (fd_ < 0) return false;
        size_t sent = 0;
        while (sent < len) {
            ssize_t n = ::write(fd_, buf + sent, len - sent);
            if (n < 0) {
                if (errno == EINTR) continue;
                log_error(std::string("Serial write failed: ") + std::strerror(errno));
                return false;
            }
            if (n == 0) continue;
            sent += (size_t)n;
        }
        return true;
    }

    bool read_exact(std::vector<uint8_t> &out, size_t len, int timeoutMs) {
        out.clear();
        if (fd_ < 0) return false;
        auto start = std::chrono::steady_clock::now();
        while (out.size() < len) {
            auto now = std::chrono::steady_clock::now();
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
            int remaining = timeoutMs - elapsed;
            if (remaining <= 0) {
                return false;
            }
            struct pollfd pfd;
            pfd.fd = fd_;
            pfd.events = POLLIN;
            int pr = ::poll(&pfd, 1, remaining);
            if (pr < 0) {
                if (errno == EINTR) continue;
                log_error(std::string("Serial poll failed: ") + std::strerror(errno));
                return false;
            } else if (pr == 0) {
                return false; // timeout
            } else {
                if (pfd.revents & POLLIN) {
                    uint8_t buf[256];
                    size_t need = len - out.size();
                    size_t chunk = need < sizeof(buf) ? need : sizeof(buf);
                    ssize_t n = ::read(fd_, buf, chunk);
                    if (n < 0) {
                        if (errno == EINTR) continue;
                        log_error(std::string("Serial read failed: ") + std::strerror(errno));
                        return false;
                    } else if (n == 0) {
                        continue;
                    } else {
                        out.insert(out.end(), buf, buf + n);
                    }
                }
            }
        }
        return true;
    }

private:
    int fd_;
};

class ModbusRTU {
public:
    explicit ModbusRTU(SerialPort &sp) : sp_(sp) {}

    bool read_input_registers(uint8_t slave, uint16_t addr, uint16_t count,
                               std::vector<uint16_t> &regs,
                               int timeoutMs, int retryLimit) {
        regs.clear();
        for (int attempt = 1; attempt <= retryLimit; ++attempt) {
            uint8_t req[8];
            req[0] = slave;
            req[1] = 0x04; // Read Input Registers
            req[2] = (uint8_t)((addr >> 8) & 0xFF);
            req[3] = (uint8_t)(addr & 0xFF);
            req[4] = (uint8_t)((count >> 8) & 0xFF);
            req[5] = (uint8_t)(count & 0xFF);
            uint16_t crc = modbus_crc16(req, 6);
            req[6] = (uint8_t)(crc & 0xFF);
            req[7] = (uint8_t)((crc >> 8) & 0xFF);

            if (!sp_.write_all(req, sizeof(req))) {
                log_warn("Modbus write failed, attempt " + std::to_string(attempt));
                continue;
            }

            // Expected response: slave, func, byteCount, data(2*count), crcLo, crcHi
            size_t resp_len = 5 + 2 * count;
            std::vector<uint8_t> resp;
            if (!sp_.read_exact(resp, resp_len, timeoutMs)) {
                log_warn("Modbus timeout/no response, attempt " + std::to_string(attempt));
                continue;
            }
            if (resp.size() != resp_len) {
                log_warn("Modbus short response, attempt " + std::to_string(attempt));
                continue;
            }
            uint16_t rcrc = (uint16_t)resp[resp_len - 2] | ((uint16_t)resp[resp_len - 1] << 8);
            uint16_t ccrc = modbus_crc16(resp.data(), resp_len - 2);
            if (rcrc != ccrc) {
                log_warn("CRC mismatch in Modbus response, attempt " + std::to_string(attempt));
                continue;
            }
            if (resp[0] != slave || resp[1] != 0x04) {
                log_warn("Unexpected Modbus header (slave/func), attempt " + std::to_string(attempt));
                continue;
            }
            uint8_t byteCount = resp[2];
            if (byteCount != 2 * count) {
                log_warn("Unexpected byte count in Modbus response, attempt " + std::to_string(attempt));
                continue;
            }
            regs.resize(count);
            for (uint16_t i = 0; i < count; ++i) {
                uint8_t hb = resp[3 + 2 * i];
                uint8_t lb = resp[3 + 2 * i + 1];
                regs[i] = ((uint16_t)hb << 8) | (uint16_t)lb;
            }
            return true;
        }
        return false;
    }

private:
    SerialPort &sp_;
};

static float decode_float_from_regs(uint16_t r1, uint16_t r2) {
    // Assume big-endian register order, IEEE-754 float: r1 high word, r2 low word
    uint32_t bits = ((uint32_t)(r1 & 0xFFFF) << 16) | (uint32_t)(r2 & 0xFFFF);
    union { uint32_t u; float f; } u;
    u.u = bits;
    return u.f;
}

struct LatestData {
    std::string json; // cached latest JSON
    std::string timestampIso;
    bool hasData = false;
    std::string lastError;
};

class SensorPoller {
public:
    SensorPoller(const Config &cfg)
        : cfg_(cfg), running_(false), sp_(), mb_(sp_) {}

    void start() {
        running_ = true;
        th_ = std::thread(&SensorPoller::run, this);
    }
    void stop() {
        running_ = false;
        if (th_.joinable()) th_.join();
        sp_.close();
    }

    LatestData getLatest() {
        std::lock_guard<std::mutex> lk(m_);
        return latest_;
    }

private:
    void run() {
        int backoff = cfg_.backoffInitialMs;
        auto nextPoll = std::chrono::steady_clock::now();
        while (running_) {
            if (!ensure_serial_open()) {
                log_warn("Serial not open; backing off " + std::to_string(backoff) + "ms");
                sleep_for_ms(backoff);
                backoff = std::min(backoff * 2, cfg_.backoffMaxMs);
                continue;
            }

            // Sleep until next scheduled poll
            auto now = std::chrono::steady_clock::now();
            if (now < nextPoll) {
                auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(nextPoll - now).count();
                sleep_for_ms((int)ms);
            }

            if (!running_) break;

            // Perform one poll cycle
            bool success = poll_once();
            if (success) {
                backoff = cfg_.backoffInitialMs; // reset backoff on success
                nextPoll = std::chrono::steady_clock::now() + std::chrono::seconds(cfg_.pollIntervalSec);
            } else {
                // Failure: backoff before retrying
                log_warn("Poll failed; backing off " + std::to_string(backoff) + "ms");
                sleep_for_ms(backoff);
                backoff = std::min(backoff * 2, cfg_.backoffMaxMs);
                // Try again soon; don't wait full poll interval when recovering
                nextPoll = std::chrono::steady_clock::now();
            }
        }
    }

    bool ensure_serial_open() {
        static bool opened = false;
        if (opened) return true;
        if (sp_.open(cfg_.serialPortPath, cfg_.baudRate, cfg_.dataBits, cfg_.parity, cfg_.stopBits)) {
            opened = true;
            log_info("Serial port opened");
            return true;
        }
        return false;
    }

    bool poll_once() {
        // Read 3 parameters in one shot: 6 registers from 0x0001
        std::vector<uint16_t> regs;
        bool ok = mb_.read_input_registers((uint8_t)cfg_.modbusAddr, 0x0001, 6, regs, cfg_.requestTimeoutMs, cfg_.retryLimit);
        if (!ok) {
            std::lock_guard<std::mutex> lk(m_);
            latest_.lastError = "Communication failure";
            latest_.hasData = latest_.hasData; // keep previous data if any
            log_warn("Modbus read_input_registers failed");
            return false;
        }
        if (regs.size() != 6) {
            std::lock_guard<std::mutex> lk(m_);
            latest_.lastError = "Unexpected register count";
            log_warn("Unexpected register count");
            return false;
        }
        float temperature = decode_float_from_regs(regs[0], regs[1]);
        float humidity = decode_float_from_regs(regs[2], regs[3]);
        float pm25 = decode_float_from_regs(regs[4], regs[5]);

        // Build timestamp ISO8601 UTC
        using namespace std::chrono;
        auto now = system_clock::now();
        std::time_t t = system_clock::to_time_t(now);
        std::tm tm{}; gmtime_r(&t, &tm);
        char buf[64]; strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
        std::string iso(buf);

        std::ostringstream json;
        json.setf(std::ios::fixed); json << std::setprecision(3);
        json << "{"
             << "\"timestamp\":\"" << iso << "\",";
        json << "\"temperature_c\":" << temperature << ",";
        json << "\"humidity_rh\":" << humidity << ",";
        json << "\"pm25_ugm3\":" << pm25 << ",";
        json << "\"status\":\"ok\"";
        json << "}";

        {
            std::lock_guard<std::mutex> lk(m_);
            latest_.json = json.str();
            latest_.timestampIso = iso;
            latest_.hasData = true;
            latest_.lastError.clear();
        }
        log_info("Poll success at " + iso);
        return true;
    }

    static void sleep_for_ms(int ms) {
        std::this_thread::sleep_for(std::chrono::milliseconds(ms));
    }

private:
    const Config cfg_;
    std::atomic<bool> running_;
    std::thread th_;
    SerialPort sp_;
    ModbusRTU mb_;
    std::mutex m_;
    LatestData latest_;
};

class HttpServer {
public:
    HttpServer(const Config &cfg, SensorPoller &poller)
        : cfg_(cfg), poller_(poller) {}

    bool start() {
        listenFd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        if (listenFd_ < 0) {
            log_error(std::string("socket failed: ") + std::strerror(errno));
            return false;
        }
        int opt = 1;
        setsockopt(listenFd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

        struct sockaddr_in addr{};
        addr.sin_family = AF_INET;
        addr.sin_port = htons((uint16_t)cfg_.httpPort);
        if (inet_pton(AF_INET, cfg_.httpHost.c_str(), &addr.sin_addr) != 1) {
            log_error("Invalid HTTP_HOST (must be IPv4 dotted string)");
            ::close(listenFd_);
            listenFd_ = -1;
            return false;
        }
        if (bind(listenFd_, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
            log_error(std::string("bind failed: ") + std::strerror(errno));
            ::close(listenFd_);
            listenFd_ = -1;
            return false;
        }
        if (listen(listenFd_, 16) < 0) {
            log_error(std::string("listen failed: ") + std::strerror(errno));
            ::close(listenFd_);
            listenFd_ = -1;
            return false;
        }
        log_info("HTTP server listening");
        running_ = true;
        th_ = std::thread(&HttpServer::loop, this);
        return true;
    }

    void stop() {
        running_ = false;
        if (listenFd_ >= 0) {
            ::shutdown(listenFd_, SHUT_RDWR);
            ::close(listenFd_);
            listenFd_ = -1;
        }
        if (th_.joinable()) th_.join();
        log_info("HTTP server stopped");
    }

private:
    void loop() {
        while (running_) {
            struct sockaddr_in cli{};
            socklen_t clilen = sizeof(cli);
            int cfd = ::accept(listenFd_, (struct sockaddr *)&cli, &clilen);
            if (cfd < 0) {
                if (!running_) break;
                if (errno == EINTR) continue;
                log_warn(std::string("accept failed: ") + std::strerror(errno));
                continue;
            }
            std::thread(&HttpServer::handleClient, this, cfd).detach();
        }
    }

    static std::string http_200_json(const std::string &body) {
        std::ostringstream oss;
        oss << "HTTP/1.1 200 OK\r\n"
            << "Content-Type: application/json\r\n"
            << "Connection: close\r\n"
            << "Content-Length: " << body.size() << "\r\n\r\n"
            << body;
        return oss.str();
    }
    static std::string http_404() {
        std::string body = "{\"error\":\"not_found\"}";
        std::ostringstream oss;
        oss << "HTTP/1.1 404 Not Found\r\n"
            << "Content-Type: application/json\r\n"
            << "Connection: close\r\n"
            << "Content-Length: " << body.size() << "\r\n\r\n"
            << body;
        return oss.str();
    }
    static std::string http_405() {
        std::string body = "{\"error\":\"method_not_allowed\"}";
        std::ostringstream oss;
        oss << "HTTP/1.1 405 Method Not Allowed\r\n"
            << "Content-Type: application/json\r\n"
            << "Connection: close\r\n"
            << "Content-Length: " << body.size() << "\r\n\r\n"
            << body;
        return oss.str();
    }
    static std::string http_503(const std::string &msg) {
        std::ostringstream body;
        body << "{\"error\":\"" << msg << "\"}";
        std::ostringstream oss;
        std::string b = body.str();
        oss << "HTTP/1.1 503 Service Unavailable\r\n"
            << "Content-Type: application/json\r\n"
            << "Connection: close\r\n"
            << "Content-Length: " << b.size() << "\r\n\r\n"
            << b;
        return oss.str();
    }

    void handleClient(int cfd) {
        char buf[2048];
        ssize_t n = ::recv(cfd, buf, sizeof(buf) - 1, 0);
        if (n <= 0) {
            ::close(cfd);
            return;
        }
        buf[n] = '\0';
        std::string req(buf);
        // Parse request line
        std::istringstream iss(req);
        std::string line;
        std::getline(iss, line);
        if (!line.empty() && line.back() == '\r') line.pop_back();
        std::istringstream ls(line);
        std::string method, path, version;
        ls >> method >> path >> version;
        if (method != "GET") {
            auto resp = http_405();
            ::send(cfd, resp.c_str(), resp.size(), 0);
            ::close(cfd);
            return;
        }
        if (path == "/readings") {
            LatestData ld = poller_.getLatest();
            if (!ld.hasData) {
                auto resp = http_503(ld.lastError.empty() ? std::string("no_data") : ld.lastError);
                ::send(cfd, resp.c_str(), resp.size(), 0);
                ::close(cfd);
                return;
            }
            auto resp = http_200_json(ld.json);
            ::send(cfd, resp.c_str(), resp.size(), 0);
            ::close(cfd);
            return;
        } else {
            auto resp = http_404();
            ::send(cfd, resp.c_str(), resp.size(), 0);
            ::close(cfd);
            return;
        }
    }

private:
    const Config &cfg_;
    SensorPoller &poller_;
    int listenFd_ = -1;
    std::atomic<bool> running_{false};
    std::thread th_;
};

static std::atomic<bool> g_stop(false);
static SensorPoller *g_poller_ptr = nullptr;
static HttpServer *g_http_ptr = nullptr;

static void signal_handler(int) {
    g_stop = true;
    if (g_http_ptr) g_http_ptr->stop();
    if (g_poller_ptr) g_poller_ptr->stop();
}

int main() {
    try {
        Config cfg = load_config();
        SensorPoller poller(cfg);
        g_poller_ptr = &poller;
        poller.start();

        HttpServer http(cfg, poller);
        g_http_ptr = &http;
        if (!http.start()) {
            log_error("Failed to start HTTP server");
            poller.stop();
            return 1;
        }

        std::signal(SIGINT, signal_handler);
        std::signal(SIGTERM, signal_handler);

        // Wait until stopped
        while (!g_stop.load()) {
            std::this_thread::sleep_for(std::chrono::milliseconds(200));
        }

        log_info("Shutting down");
        return 0;
    } catch (const std::exception &ex) {
        log_error(std::string("Fatal: ") + ex.what());
        return 1;
    }
}
