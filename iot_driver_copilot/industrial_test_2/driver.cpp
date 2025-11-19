#include <iostream>
#include <string>
#include <thread>
#include <atomic>
#include <mutex>
#include <vector>
#include <chrono>
#include <ctime>
#include <csignal>
#include <cstdint>
#include <cstdlib>
#include <cerrno>
#include <cstring>
#include <sstream>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/socket.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/select.h>
#include <termios.h>

// ===================== Utility & Config =====================

static std::atomic<bool> g_stop(false);

std::string iso8601_now() {
    using namespace std::chrono;
    auto now = system_clock::now();
    std::time_t tt = system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&tt, &tm);
    char buf[64];
    std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

std::string format_time_point(const std::chrono::system_clock::time_point &tp) {
    if (tp.time_since_epoch().count() == 0) return "null";
    using namespace std::chrono;
    std::time_t tt = system_clock::to_time_t(tp);
    std::tm tm{};
    gmtime_r(&tt, &tm);
    char buf[64];
    std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

void log_event(const std::string &level, const std::string &msg) {
    std::cerr << iso8601_now() << " [" << level << "] " << msg << std::endl;
}

std::string getenv_str(const char *name) {
    const char *v = std::getenv(name);
    if (!v) {
        std::cerr << "Missing required environment variable: " << name << std::endl;
        std::exit(1);
    }
    return std::string(v);
}

int getenv_int(const char *name) {
    std::string s = getenv_str(name);
    try {
        size_t idx = 0;
        int val = std::stoi(s, &idx, 10);
        if (idx != s.size()) throw std::invalid_argument("extra chars");
        return val;
    } catch (...) {
        std::cerr << "Invalid integer for env var " << name << ": " << s << std::endl;
        std::exit(1);
    }
}

double getenv_double(const char *name) {
    std::string s = getenv_str(name);
    try {
        size_t idx = 0;
        double val = std::stod(s, &idx);
        if (idx != s.size()) throw std::invalid_argument("extra chars");
        return val;
    } catch (...) {
        std::cerr << "Invalid double for env var " << name << ": " << s << std::endl;
        std::exit(1);
    }
}

struct Config {
    std::string http_host;
    int http_port;

    std::string serial_port;
    int serial_baud;
    int serial_databits;
    int serial_stopbits;
    char serial_parity; // 'N', 'E', 'O'

    int slave_id;

    uint16_t temp_addr;
    int temp_qty;
    double temp_scale;

    uint16_t hum_addr;
    int hum_qty;
    double hum_scale;

    uint16_t co2_addr;
    int co2_qty;
    double co2_scale;

    int poll_interval_ms;
    int read_timeout_ms;

    int retry_init_ms;
    int retry_max_ms;
    double retry_multiplier;
};

Config load_config() {
    Config c;
    c.http_host = getenv_str("HTTP_HOST");
    c.http_port = getenv_int("HTTP_PORT");

    c.serial_port = getenv_str("SERIAL_PORT");
    c.serial_baud = getenv_int("SERIAL_BAUD");
    c.serial_databits = getenv_int("SERIAL_DATABITS");
    c.serial_stopbits = getenv_int("SERIAL_STOPBITS");
    {
        std::string p = getenv_str("SERIAL_PARITY");
        if (p.empty()) { std::cerr << "SERIAL_PARITY must be N/E/O" << std::endl; std::exit(1); }
        char ch = std::toupper(p[0]);
        if (ch!='N' && ch!='E' && ch!='O') { std::cerr << "SERIAL_PARITY must be N/E/O" << std::endl; std::exit(1); }
        c.serial_parity = ch;
    }

    c.slave_id = getenv_int("MODBUS_SLAVE_ID");

    c.temp_addr = static_cast<uint16_t>(getenv_int("TEMP_ADDR"));
    c.temp_qty = getenv_int("TEMP_QTY");
    c.temp_scale = getenv_double("TEMP_SCALE");

    c.hum_addr = static_cast<uint16_t>(getenv_int("HUM_ADDR"));
    c.hum_qty = getenv_int("HUM_QTY");
    c.hum_scale = getenv_double("HUM_SCALE");

    c.co2_addr = static_cast<uint16_t>(getenv_int("CO2_ADDR"));
    c.co2_qty = getenv_int("CO2_QTY");
    c.co2_scale = getenv_double("CO2_SCALE");

    c.poll_interval_ms = getenv_int("POLL_INTERVAL_MS");
    c.read_timeout_ms = getenv_int("READ_TIMEOUT_MS");

    c.retry_init_ms = getenv_int("RETRY_INIT_MS");
    c.retry_max_ms = getenv_int("RETRY_MAX_MS");
    c.retry_multiplier = getenv_double("RETRY_MULTIPLIER");
    if (c.retry_multiplier < 1.0) {
        std::cerr << "RETRY_MULTIPLIER must be >= 1.0" << std::endl;
        std::exit(1);
    }

    return c;
}

// ===================== Modbus RTU Utilities =====================

uint16_t modbus_crc16(const uint8_t *buf, size_t len) {
    uint16_t crc = 0xFFFF;
    for (size_t pos = 0; pos < len; pos++) {
        crc ^= (uint16_t)buf[pos];
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

speed_t baud_to_speed_t(int baud) {
    switch (baud) {
        case 1200: return B1200;
        case 2400: return B2400;
        case 4800: return B4800;
        case 9600: return B9600;
        case 19200: return B19200;
        case 38400: return B38400;
        case 57600: return B57600;
        case 115200: return B115200;
        default:
            std::cerr << "Unsupported SERIAL_BAUD: " << baud << std::endl;
            std::exit(1);
    }
}

int open_serial(const Config &cfg) {
    int fd = ::open(cfg.serial_port.c_str(), O_RDWR | O_NOCTTY | O_SYNC);
    if (fd < 0) {
        log_event("ERROR", std::string("open serial failed: ") + strerror(errno));
        return -1;
    }

    struct termios tty;
    memset(&tty, 0, sizeof tty);
    if (tcgetattr(fd, &tty) != 0) {
        log_event("ERROR", std::string("tcgetattr failed: ") + strerror(errno));
        ::close(fd);
        return -1;
    }

    cfmakeraw(&tty);

    // Set baud rates
    speed_t spd = baud_to_speed_t(cfg.serial_baud);
    cfsetospeed(&tty, spd);
    cfsetispeed(&tty, spd);

    // Data bits
    tty.c_cflag &= ~CSIZE;
    if (cfg.serial_databits == 7) tty.c_cflag |= CS7;
    else if (cfg.serial_databits == 8) tty.c_cflag |= CS8;
    else {
        log_event("ERROR", "Unsupported SERIAL_DATABITS (use 7 or 8)");
        ::close(fd);
        return -1;
    }

    // Parity
    if (cfg.serial_parity == 'N') {
        tty.c_cflag &= ~PARENB;
    } else if (cfg.serial_parity == 'E') {
        tty.c_cflag |= PARENB;
        tty.c_cflag &= ~PARODD;
    } else if (cfg.serial_parity == 'O') {
        tty.c_cflag |= PARENB;
        tty.c_cflag |= PARODD;
    }

    // Stop bits
    if (cfg.serial_stopbits == 1) tty.c_cflag &= ~CSTOPB;
    else if (cfg.serial_stopbits == 2) tty.c_cflag |= CSTOPB;
    else {
        log_event("ERROR", "Unsupported SERIAL_STOPBITS (use 1 or 2)");
        ::close(fd);
        return -1;
    }

    tty.c_cflag |= (CLOCAL | CREAD);

    tty.c_cc[VMIN]  = 0;
    tty.c_cc[VTIME] = 0; // We'll use select() for timeouts

    if (tcsetattr(fd, TCSANOW, &tty) != 0) {
        log_event("ERROR", std::string("tcsetattr failed: ") + strerror(errno));
        ::close(fd);
        return -1;
    }

    tcflush(fd, TCIOFLUSH);
    log_event("INFO", "Serial port opened: " + cfg.serial_port);
    return fd;
}

std::vector<uint8_t> read_n_with_timeout(int fd, size_t n, int timeout_ms) {
    std::vector<uint8_t> buf;
    buf.reserve(n);
    auto start = std::chrono::steady_clock::now();
    while (buf.size() < n) {
        auto now = std::chrono::steady_clock::now();
        int elapsed_ms = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
        int remain_ms = timeout_ms - elapsed_ms;
        if (remain_ms <= 0) {
            return {};
        }
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(fd, &rfds);
        struct timeval tv;
        tv.tv_sec = remain_ms / 1000;
        tv.tv_usec = (remain_ms % 1000) * 1000;
        int rv = select(fd + 1, &rfds, nullptr, nullptr, &tv);
        if (rv < 0) {
            if (errno == EINTR) continue;
            return {};
        } else if (rv == 0) {
            continue;
        } else {
            uint8_t tmp[256];
            size_t to_read = std::min((size_t)sizeof(tmp), n - buf.size());
            ssize_t r = ::read(fd, tmp, to_read);
            if (r <= 0) {
                return {};
            }
            buf.insert(buf.end(), tmp, tmp + r);
        }
    }
    return buf;
}

bool modbus_read_holding_registers(int fd, uint8_t slave_id, uint16_t start_addr, uint16_t quantity, std::vector<uint16_t> &out, int timeout_ms) {
    uint8_t req[8];
    req[0] = slave_id;
    req[1] = 0x03;
    req[2] = (uint8_t)((start_addr >> 8) & 0xFF);
    req[3] = (uint8_t)(start_addr & 0xFF);
    req[4] = (uint8_t)((quantity >> 8) & 0xFF);
    req[5] = (uint8_t)(quantity & 0xFF);
    uint16_t crc = modbus_crc16(req, 6);
    req[6] = (uint8_t)(crc & 0xFF);
    req[7] = (uint8_t)((crc >> 8) & 0xFF);

    ssize_t w = ::write(fd, req, sizeof(req));
    if (w != (ssize_t)sizeof(req)) {
        return false;
    }
    tcdrain(fd);

    auto header = read_n_with_timeout(fd, 3, timeout_ms);
    if (header.size() != 3) return false;

    if (header[0] != slave_id) return false;
    if (header[1] == (uint8_t)(0x80 | 0x03)) {
        // Exception response
        auto rest = read_n_with_timeout(fd, 2, timeout_ms); // exception code + CRC
        return false;
    }
    if (header[1] != 0x03) return false;

    uint8_t byte_count = header[2];
    auto payload_crc = read_n_with_timeout(fd, byte_count + 2, timeout_ms);
    if (payload_crc.size() != (size_t)(byte_count + 2)) return false;

    // Build full frame to verify CRC
    std::vector<uint8_t> frame;
    frame.reserve(3 + byte_count + 2);
    frame.insert(frame.end(), header.begin(), header.end());
    frame.insert(frame.end(), payload_crc.begin(), payload_crc.end());

    uint16_t recv_crc = (uint16_t)payload_crc[byte_count] | ((uint16_t)payload_crc[byte_count + 1] << 8);
    uint16_t calc_crc = modbus_crc16(frame.data(), frame.size() - 2);
    if (recv_crc != calc_crc) return false;

    if (byte_count != (uint8_t)(quantity * 2)) return false;

    out.clear();
    out.reserve(quantity);
    for (int i = 0; i < quantity; ++i) {
        uint8_t hi = payload_crc[2 * i];
        uint8_t lo = payload_crc[2 * i + 1];
        uint16_t v = ((uint16_t)hi << 8) | (uint16_t)lo;
        out.push_back(v);
    }

    return true;
}

// ===================== Shared State =====================

struct Readings {
    double temperature = 0.0;
    double humidity = 0.0;
    double co2 = 0.0;
    bool temp_valid = false;
    bool hum_valid = false;
    bool co2_valid = false;
    std::chrono::system_clock::time_point temp_ts{};
    std::chrono::system_clock::time_point hum_ts{};
    std::chrono::system_clock::time_point co2_ts{};
};

struct DriverState {
    std::mutex mtx;
    Readings readings;
    std::atomic<bool> connected{false};
    std::atomic<bool> polling_active{false};
    std::chrono::system_clock::time_point last_poll_time{};
};

// ===================== Poller =====================

class Poller {
public:
    Poller(const Config &cfg, DriverState &st) : cfg_(cfg), state_(st) {}

    void start() {
        stop_ = false;
        th_ = std::thread(&Poller::run, this);
    }
    void stop() {
        stop_ = true;
        if (th_.joinable()) th_.join();
    }

private:
    const Config &cfg_;
    DriverState &state_;
    std::thread th_;
    std::atomic<bool> stop_{false};
    int fd_ = -1;

    void close_serial() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
        }
        state_.connected.store(false);
        log_event("INFO", "Serial port closed");
    }

    bool ensure_serial_open() {
        if (fd_ >= 0) return true;
        fd_ = open_serial(cfg_);
        if (fd_ >= 0) {
            state_.connected.store(true);
            return true;
        }
        state_.connected.store(false);
        return false;
    }

    bool read_scaled(uint16_t addr, int qty, double scale, double &out_val) {
        std::vector<uint16_t> regs;
        bool ok = modbus_read_holding_registers(fd_, (uint8_t)cfg_.slave_id, addr, (uint16_t)qty, regs, cfg_.read_timeout_ms);
        if (!ok) return false;
        if (qty == 1) {
            out_val = (double)regs[0] * scale;
            return true;
        } else if (qty == 2) {
            uint32_t v32 = ((uint32_t)regs[0] << 16) | (uint32_t)regs[1];
            out_val = (double)v32 * scale;
            return true;
        } else {
            // For qty > 2, not supported
            return false;
        }
    }

    void run() {
        int backoff_ms = cfg_.retry_init_ms;
        while (!stop_) {
            if (!ensure_serial_open()) {
                log_event("WARN", "Failed to open serial. Backing off " + std::to_string(backoff_ms) + " ms");
                sleep_ms(backoff_ms);
                backoff_ms = std::min((int)(backoff_ms * cfg_.retry_multiplier), cfg_.retry_max_ms);
                continue;
            }

            state_.polling_active.store(true);

            auto poll_start = std::chrono::steady_clock::now();
            bool any_fail = false;
            double val = 0.0;

            // Temperature
            if (read_scaled(cfg_.temp_addr, cfg_.temp_qty, cfg_.temp_scale, val)) {
                std::lock_guard<std::mutex> lk(state_.mtx);
                state_.readings.temperature = val;
                state_.readings.temp_valid = true;
                state_.readings.temp_ts = std::chrono::system_clock::now();
            } else { any_fail = true; }

            // Humidity
            if (read_scaled(cfg_.hum_addr, cfg_.hum_qty, cfg_.hum_scale, val)) {
                std::lock_guard<std::mutex> lk(state_.mtx);
                state_.readings.humidity = val;
                state_.readings.hum_valid = true;
                state_.readings.hum_ts = std::chrono::system_clock::now();
            } else { any_fail = true; }

            // CO2
            if (read_scaled(cfg_.co2_addr, cfg_.co2_qty, cfg_.co2_scale, val)) {
                std::lock_guard<std::mutex> lk(state_.mtx);
                state_.readings.co2 = val;
                state_.readings.co2_valid = true;
                state_.readings.co2_ts = std::chrono::system_clock::now();
            } else { any_fail = true; }

            state_.last_poll_time = std::chrono::system_clock::now();
            if (any_fail) {
                log_event("ERROR", "Polling error occurred. Closing serial and backing off " + std::to_string(backoff_ms) + " ms");
                close_serial();
                sleep_ms(backoff_ms);
                backoff_ms = std::min((int)(backoff_ms * cfg_.retry_multiplier), cfg_.retry_max_ms);
                continue;
            } else {
                // Success; reset backoff
                backoff_ms = cfg_.retry_init_ms;
            }

            // sleep until next poll
            auto poll_end = std::chrono::steady_clock::now();
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(poll_end - poll_start).count();
            int wait_ms = cfg_.poll_interval_ms - elapsed;
            if (wait_ms < 0) wait_ms = 0;
            sleep_ms(wait_ms);
        }
        state_.polling_active.store(false);
        close_serial();
    }

    void sleep_ms(int ms) {
        if (ms <= 0) return;
        auto start = std::chrono::steady_clock::now();
        while (!stop_) {
            auto now = std::chrono::steady_clock::now();
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
            if (elapsed >= ms) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(10));
        }
    }
};

// ===================== HTTP Server =====================

std::string json_escape(const std::string &s) {
    std::ostringstream oss;
    for (char c : s) {
        switch (c) {
            case '"': oss << "\\\""; break;
            case '\\': oss << "\\\\"; break;
            case '\n': oss << "\\n"; break;
            case '\r': oss << "\\r"; break;
            case '\t': oss << "\\t"; break;
            default: oss << c; break;
        }
    }
    return oss.str();
}

void send_http_response(int client, int status_code, const std::string &body) {
    std::ostringstream oss;
    const char *status_text = (status_code == 200) ? "OK" : (status_code == 404 ? "Not Found" : "Error");
    oss << "HTTP/1.1 " << status_code << " " << status_text << "\r\n";
    oss << "Content-Type: application/json\r\n";
    oss << "Content-Length: " << body.size() << "\r\n";
    oss << "Connection: close\r\n\r\n";
    std::string headers = oss.str();
    ::send(client, headers.data(), headers.size(), 0);
    ::send(client, body.data(), body.size(), 0);
}

std::string build_status_json(const Config &cfg, const DriverState &state) {
    std::ostringstream oss;
    oss << "{";
    oss << "\"connected\": " << (state.connected.load() ? "true" : "false") << ", ";
    oss << "\"port\": \"" << json_escape(cfg.serial_port) << "\", ";
    oss << "\"slave_id\": " << cfg.slave_id << ", ";
    oss << "\"polling_active\": " << (state.polling_active.load() ? "true" : "false") << ", ";
    oss << "\"interval_ms\": " << cfg.poll_interval_ms << ", ";
    oss << "\"last_poll_time\": \"" << json_escape(format_time_point(state.last_poll_time)) << "\"";
    oss << "}";
    return oss.str();
}

std::string build_readings_json(const DriverState &state) {
    std::lock_guard<std::mutex> lk(const_cast<std::mutex&>(state.mtx));
    std::ostringstream oss;
    oss << "{";
    oss << "\"temperature\": { \"value\": ";
    if (state.readings.temp_valid) oss << state.readings.temperature; else oss << "null";
    oss << ", \"updated_at\": \"" << json_escape(format_time_point(state.readings.temp_ts)) << "\" }, ";

    oss << "\"humidity\": { \"value\": ";
    if (state.readings.hum_valid) oss << state.readings.humidity; else oss << "null";
    oss << ", \"updated_at\": \"" << json_escape(format_time_point(state.readings.hum_ts)) << "\" }, ";

    oss << "\"co2\": { \"value\": ";
    if (state.readings.co2_valid) oss << state.readings.co2; else oss << "null";
    oss << ", \"updated_at\": \"" << json_escape(format_time_point(state.readings.co2_ts)) << "\" }";

    oss << "}";
    return oss.str();
}

void handle_client(int client, const Config &cfg, const DriverState &state) {
    char buf[4096];
    ssize_t n = recv(client, buf, sizeof(buf) - 1, 0);
    if (n <= 0) {
        ::close(client);
        return;
    }
    buf[n] = '\0';
    std::string req(buf);

    // Parse first line
    size_t pos = req.find("\r\n");
    if (pos == std::string::npos) pos = req.find('\n');
    std::string line = (pos == std::string::npos) ? req : req.substr(0, pos);
    std::istringstream iss(line);
    std::string method, path, version;
    iss >> method >> path >> version;

    if (method != "GET") {
        send_http_response(client, 404, "{\"error\": \"Only GET supported\"}");
        ::close(client);
        return;
    }

    if (path == "/status") {
        std::string body = build_status_json(cfg, state);
        send_http_response(client, 200, body);
    } else if (path == "/readings") {
        std::string body = build_readings_json(state);
        send_http_response(client, 200, body);
    } else {
        send_http_response(client, 404, "{\"error\": \"Not Found\"}");
    }
    ::close(client);
}

void http_server(const Config &cfg, const DriverState &state) {
    int server_fd = ::socket(AF_INET, SOCK_STREAM, 0);
    if (server_fd < 0) {
        log_event("ERROR", std::string("socket failed: ") + strerror(errno));
        return;
    }
    int opt = 1;
    setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(cfg.http_port);
    if (inet_pton(AF_INET, cfg.http_host.c_str(), &addr.sin_addr) <= 0) {
        log_event("ERROR", "Invalid HTTP_HOST address");
        ::close(server_fd);
        return;
    }

    if (bind(server_fd, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
        log_event("ERROR", std::string("bind failed: ") + strerror(errno));
        ::close(server_fd);
        return;
    }

    if (listen(server_fd, 16) < 0) {
        log_event("ERROR", std::string("listen failed: ") + strerror(errno));
        ::close(server_fd);
        return;
    }

    log_event("INFO", "HTTP server listening on " + cfg.http_host + ":" + std::to_string(cfg.http_port));

    while (!g_stop.load()) {
        sockaddr_in cli{};
        socklen_t clilen = sizeof(cli);
        int client = accept(server_fd, (struct sockaddr*)&cli, &clilen);
        if (client < 0) {
            if (errno == EINTR) continue;
            if (g_stop.load()) break;
            log_event("ERROR", std::string("accept failed: ") + strerror(errno));
            continue;
        }
        std::thread([client, &cfg, &state]() {
            handle_client(client, cfg, state);
        }).detach();
    }

    ::close(server_fd);
    log_event("INFO", "HTTP server stopped");
}

// ===================== Main =====================

DriverState g_state;

void signal_handler(int) {
    g_stop.store(true);
}

int main() {
    std::signal(SIGINT, signal_handler);
    std::signal(SIGTERM, signal_handler);

    Config cfg = load_config();

    Poller poller(cfg, g_state);
    poller.start();

    http_server(cfg, g_state);

    g_stop.store(true);
    poller.stop();

    log_event("INFO", "Driver shutdown complete");
    return 0;
}
