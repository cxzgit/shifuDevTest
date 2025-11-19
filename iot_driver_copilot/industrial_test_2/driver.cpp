#include <iostream>
#include <string>
#include <thread>
#include <atomic>
#include <mutex>
#include <chrono>
#include <csignal>
#include <ctime>
#include <sstream>
#include <vector>
#include <condition_variable>
#include <map>
#include <iomanip>
#include <cerrno>
#include <cstring>

#include <unistd.h>
#include <fcntl.h>
#include <termios.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <netdb.h>
#include <netinet/in.h>
#include <arpa/inet.h>

// Simple logger
static std::mutex g_log_mtx;
static void log_msg(const std::string &level, const std::string &msg) {
    std::lock_guard<std::mutex> lk(g_log_mtx);
    auto now = std::chrono::system_clock::now();
    std::time_t t = std::chrono::system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t, &tm);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    std::cerr << "[" << buf << "] [" << level << "] " << msg << std::endl;
}

// ISO 8601 UTC time now
static std::string iso_time_now() {
    auto now = std::chrono::system_clock::now();
    std::time_t t = std::chrono::system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t, &tm);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

static const char* getenv_or(const char* name, const char* defv) {
    const char* v = std::getenv(name);
    return v ? v : defv;
}

static int getenv_int(const char* name, int defv) {
    const char* v = std::getenv(name);
    if (!v) return defv;
    try {
        return std::stoi(v);
    } catch (...) {
        return defv;
    }
}

static double getenv_double(const char* name, double defv) {
    const char* v = std::getenv(name);
    if (!v) return defv;
    try {
        return std::stod(v);
    } catch (...) {
        return defv;
    }
}

static uint16_t getenv_uint16(const char* name, uint16_t defv) {
    const char* v = std::getenv(name);
    if (!v) return defv;
    try {
        unsigned long val = std::stoul(v, nullptr, 0);
        if (val > 0xFFFF) return defv;
        return static_cast<uint16_t>(val);
    } catch (...) {
        return defv;
    }
}

struct Config {
    std::string http_host;
    int http_port;
    std::string serial_port;
    int serial_baud;
    int slave_id;
    uint16_t reg_temp;
    uint16_t reg_hum;
    uint16_t reg_co2;
    double temp_scale;
    double hum_scale;
    double co2_scale;
    int poll_interval_ms;
    int read_timeout_ms;
    int backoff_initial_ms;
    int backoff_max_ms;
};

struct Reading {
    double value = 0.0;
    std::string ts;
    bool has = false;
};

struct Latest {
    Reading temp;
    Reading hum;
    Reading co2;
    std::string last_poll_ts;
};

struct SharedState {
    std::mutex mtx;
    Latest latest;
    bool serial_connected = false;
    bool polling_active = false;
    std::string port;
    int slave_id = 1;
    int baud = 9600;
    int poll_interval_ms = 1000;
};

static std::atomic<bool> g_shutdown{false};

// Map integer baud rate to POSIX speed_t
static speed_t baud_to_speed(int baud) {
    switch (baud) {
        case 1200: return B1200;
        case 2400: return B2400;
        case 4800: return B4800;
        case 9600: return B9600;
        case 19200: return B19200;
        case 38400: return B38400;
#ifdef B57600
        case 57600: return B57600;
#endif
#ifdef B115200
        case 115200: return B115200;
#endif
        default: return B9600;
    }
}

static uint16_t modbus_crc(const uint8_t* data, size_t len) {
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < len; ++i) {
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

class ModbusRTU {
public:
    explicit ModbusRTU(const Config& cfg) : cfg_(cfg) {}

    bool open_port() {
        close_port();
        fd_ = ::open(cfg_.serial_port.c_str(), O_RDWR | O_NOCTTY | O_SYNC);
        if (fd_ < 0) {
            log_msg("ERROR", std::string("Failed to open serial port ") + cfg_.serial_port + ": " + std::strerror(errno));
            connected_ = false;
            return false;
        }
        struct termios tty{};
        if (tcgetattr(fd_, &tty) != 0) {
            log_msg("ERROR", std::string("tcgetattr failed: ") + std::strerror(errno));
            ::close(fd_);
            fd_ = -1;
            connected_ = false;
            return false;
        }
        cfmakeraw(&tty);
        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_cflag &= ~PARENB; // No parity
        tty.c_cflag &= ~CSTOPB; // 1 stop bit
        tty.c_cflag &= ~CSIZE;
        tty.c_cflag |= CS8;     // 8 data bits

        speed_t sp = baud_to_speed(cfg_.serial_baud);
        cfsetispeed(&tty, sp);
        cfsetospeed(&tty, sp);

        // Set read timeout: VTIME in deciseconds, VMIN=0 for non-blocking timed reads
        int vtime_ds = cfg_.read_timeout_ms / 100; // deciseconds
        if (vtime_ds <= 0) vtime_ds = 1;
        tty.c_cc[VMIN] = 0;
        tty.c_cc[VTIME] = static_cast<unsigned char>(vtime_ds);

        if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
            log_msg("ERROR", std::string("tcsetattr failed: ") + std::strerror(errno));
            ::close(fd_);
            fd_ = -1;
            connected_ = false;
            return false;
        }
        tcflush(fd_, TCIOFLUSH);
        connected_ = true;
        log_msg("INFO", std::string("Serial port opened: ") + cfg_.serial_port + ", baud=" + std::to_string(cfg_.serial_baud));
        return true;
    }

    void close_port() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
            if (connected_) {
                log_msg("INFO", "Serial port closed");
            }
        }
        connected_ = false;
    }

    bool is_connected() const { return connected_; }

    bool read_register(uint16_t reg_addr, uint16_t quantity, uint16_t &out_value) {
        if (fd_ < 0) {
            connected_ = false;
            return false;
        }
        uint8_t req[8];
        req[0] = static_cast<uint8_t>(cfg_.slave_id);
        req[1] = 0x03; // Read Holding Registers
        req[2] = static_cast<uint8_t>((reg_addr >> 8) & 0xFF);
        req[3] = static_cast<uint8_t>(reg_addr & 0xFF);
        req[4] = static_cast<uint8_t>((quantity >> 8) & 0xFF);
        req[5] = static_cast<uint8_t>(quantity & 0xFF);
        uint16_t crc = modbus_crc(req, 6);
        req[6] = static_cast<uint8_t>(crc & 0xFF); // CRC low
        req[7] = static_cast<uint8_t>((crc >> 8) & 0xFF); // CRC high

        tcflush(fd_, TCIOFLUSH);
        ssize_t w = ::write(fd_, req, sizeof(req));
        if (w != (ssize_t)sizeof(req)) {
            log_msg("ERROR", std::string("Write failed: ") + std::strerror(errno));
            connected_ = false;
            return false;
        }
        tcdrain(fd_);

        // Read response: id(1) func(1) bytecount(1) data(2*quantity) crc(2)
        // First read header 3 bytes
        uint8_t hdr[3];
        if (!read_exact(fd_, hdr, 3, cfg_.read_timeout_ms)) {
            log_msg("ERROR", "Timeout or error reading Modbus header");
            connected_ = false;
            return false;
        }
        if (hdr[0] != (uint8_t)cfg_.slave_id) {
            log_msg("ERROR", "Slave ID mismatch in response");
            return false;
        }
        uint8_t func = hdr[1];
        uint8_t bytecount = hdr[2];

        if (func == (uint8_t)(0x80 | 0x03)) { // Exception response
            uint8_t ex[2]; // code + CRC[0]
            if (!read_exact(fd_, ex, 2, cfg_.read_timeout_ms)) {
                log_msg("ERROR", "Modbus exception response read failed");
            }
            // Read remaining CRC byte
            uint8_t ex2[1];
            read_exact(fd_, ex2, 1, cfg_.read_timeout_ms);
            log_msg("ERROR", "Modbus exception response received");
            return false;
        }
        size_t data_len = bytecount;
        std::vector<uint8_t> data(data_len + 2); // data + CRC(2)
        if (!read_exact(fd_, data.data(), data.size(), cfg_.read_timeout_ms)) {
            log_msg("ERROR", "Timeout or error reading Modbus payload");
            connected_ = false;
            return false;
        }
        // Validate CRC of full frame: hdr(3)+data(data_len+2)
        std::vector<uint8_t> full;
        full.reserve(3 + data.size());
        full.push_back(hdr[0]); full.push_back(hdr[1]); full.push_back(hdr[2]);
        full.insert(full.end(), data.begin(), data.end());
        uint16_t rcv_crc = (uint16_t)data[data_len] | ((uint16_t)data[data_len + 1] << 8);
        uint16_t calc_crc = modbus_crc(full.data(), 3 + data_len);
        if (rcv_crc != calc_crc) {
            log_msg("ERROR", "CRC mismatch in Modbus response");
            return false;
        }
        if (bytecount != quantity * 2) {
            log_msg("ERROR", "Unexpected byte count in Modbus response");
            return false;
        }
        // Big endian register value
        out_value = ((uint16_t)data[0] << 8) | (uint16_t)data[1];
        connected_ = true;
        return true;
    }

private:
    static bool read_exact(int fd, uint8_t* buf, size_t len, int total_timeout_ms) {
        size_t got = 0;
        auto start = std::chrono::steady_clock::now();
        while (got < len) {
            // Compute remaining timeout
            auto now = std::chrono::steady_clock::now();
            int elapsed_ms = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
            int remain_ms = total_timeout_ms - elapsed_ms;
            if (remain_ms <= 0) return false;

            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(fd, &rfds);
            struct timeval tv;
            tv.tv_sec = remain_ms / 1000;
            tv.tv_usec = (remain_ms % 1000) * 1000;
            int sel = select(fd + 1, &rfds, nullptr, nullptr, &tv);
            if (sel < 0) {
                if (errno == EINTR) continue;
                return false;
            } else if (sel == 0) {
                // timeout
                return false;
            }
            ssize_t r = ::read(fd, buf + got, len - got);
            if (r < 0) {
                if (errno == EINTR) continue;
                return false;
            } else if (r == 0) {
                // EOF
                return false;
            }
            got += (size_t)r;
        }
        return true;
    }

    const Config& cfg_;
    int fd_ = -1;
    bool connected_ = false;
};

class Poller {
public:
    Poller(const Config& cfg, SharedState& st) : cfg_(cfg), st_(st), mb_(cfg) {}

    void start() {
        stop_ = false;
        st_.polling_active = true;
        st_.poll_interval_ms = cfg_.poll_interval_ms;
        th_ = std::thread(&Poller::loop, this);
    }

    void stop() {
        stop_ = true;
        cv_.notify_all();
        if (th_.joinable()) th_.join();
        mb_.close_port();
        st_.polling_active = false;
        st_.serial_connected = false;
    }

private:
    void loop() {
        int backoff_ms = cfg_.backoff_initial_ms;
        while (!stop_) {
            bool ok = poll_once();
            if (ok) {
                backoff_ms = cfg_.backoff_initial_ms;
                wait_for(cfg_.poll_interval_ms);
            } else {
                log_msg("WARN", std::string("Polling failed, retrying in ") + std::to_string(backoff_ms) + " ms");
                wait_for(backoff_ms);
                backoff_ms = std::min(backoff_ms * 2, cfg_.backoff_max_ms);
            }
        }
    }

    void wait_for(int ms) {
        std::unique_lock<std::mutex> lk(cv_mtx_);
        cv_.wait_for(lk, std::chrono::milliseconds(ms), [this]{ return stop_.load(); });
    }

    bool poll_once() {
        // Ensure serial port is open
        if (!mb_.is_connected()) {
            if (!mb_.open_port()) {
                st_.serial_connected = false;
                return false;
            }
        }
        st_.serial_connected = true;
        st_.port = cfg_.serial_port;
        st_.baud = cfg_.serial_baud;
        st_.slave_id = cfg_.slave_id;

        uint16_t t_raw = 0, h_raw = 0, c_raw = 0;
        std::string t_ts, h_ts, c_ts;

        if (!mb_.read_register(cfg_.reg_temp, 1, t_raw)) {
            return false;
        }
        t_ts = iso_time_now();

        if (!mb_.read_register(cfg_.reg_hum, 1, h_raw)) {
            return false;
        }
        h_ts = iso_time_now();

        if (!mb_.read_register(cfg_.reg_co2, 1, c_raw)) {
            return false;
        }
        c_ts = iso_time_now();

        double t_val = (double)t_raw * cfg_.temp_scale;
        double h_val = (double)h_raw * cfg_.hum_scale;
        double c_val = (double)c_raw * cfg_.co2_scale;

        {
            std::lock_guard<std::mutex> lk(st_.mtx);
            st_.latest.temp.value = t_val;
            st_.latest.temp.ts = t_ts;
            st_.latest.temp.has = true;

            st_.latest.hum.value = h_val;
            st_.latest.hum.ts = h_ts;
            st_.latest.hum.has = true;

            st_.latest.co2.value = c_val;
            st_.latest.co2.ts = c_ts;
            st_.latest.co2.has = true;

            st_.latest.last_poll_ts = iso_time_now();
        }
        log_msg("INFO", "Poll success: T=" + std::to_string(t_val) + ", H=" + std::to_string(h_val) + ", CO2=" + std::to_string(c_val));
        return true;
    }

    const Config& cfg_;
    SharedState& st_;
    ModbusRTU mb_;
    std::atomic<bool> stop_{false};
    std::thread th_;
    std::condition_variable cv_;
    std::mutex cv_mtx_;
};

class HttpServer {
public:
    HttpServer(const Config& cfg, SharedState& st) : cfg_(cfg), st_(st) {}

    bool start() {
        if (!bind_socket()) return false;
        stop_ = false;
        accept_th_ = std::thread(&HttpServer::accept_loop, this);
        return true;
    }

    void stop() {
        stop_ = true;
        // Closing listen socket so select wakes
        if (listen_sock_ >= 0) {
            ::close(listen_sock_);
            listen_sock_ = -1;
        }
        if (accept_th_.joinable()) accept_th_.join();
        // Join any remaining client threads
        for (auto &th : client_threads_) {
            if (th.joinable()) th.join();
        }
        client_threads_.clear();
    }

private:
    bool bind_socket() {
        struct addrinfo hints{};
        hints.ai_family = AF_UNSPEC;
        hints.ai_socktype = SOCK_STREAM;
        hints.ai_flags = AI_PASSIVE;
        struct addrinfo *res = nullptr;
        std::string port_str = std::to_string(cfg_.http_port);
        int rc = getaddrinfo(cfg_.http_host.c_str(), port_str.c_str(), &hints, &res);
        if (rc != 0) {
            log_msg("ERROR", std::string("getaddrinfo failed: ") + gai_strerror(rc));
            return false;
        }
        int sock = -1;
        for (struct addrinfo *p = res; p != nullptr; p = p->ai_next) {
            sock = ::socket(p->ai_family, p->ai_socktype, p->ai_protocol);
            if (sock < 0) continue;
            int opt = 1;
            setsockopt(sock, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
#ifdef SO_REUSEPORT
            setsockopt(sock, SOL_SOCKET, SO_REUSEPORT, &opt, sizeof(opt));
#endif
            if (::bind(sock, p->ai_addr, p->ai_addrlen) == 0) {
                if (::listen(sock, 16) == 0) {
                    listen_sock_ = sock;
                    char hostbuf[NI_MAXHOST];
                    char servbuf[NI_MAXSERV];
                    if (getnameinfo(p->ai_addr, p->ai_addrlen, hostbuf, sizeof(hostbuf), servbuf, sizeof(servbuf), NI_NUMERICHOST | NI_NUMERICSERV) == 0) {
                        log_msg("INFO", std::string("HTTP server listening on ") + hostbuf + ":" + servbuf);
                    }
                    freeaddrinfo(res);
                    return true;
                }
            }
            ::close(sock);
            sock = -1;
        }
        freeaddrinfo(res);
        log_msg("ERROR", "Failed to bind HTTP server socket");
        return false;
    }

    void accept_loop() {
        while (!stop_) {
            fd_set rfds;
            FD_ZERO(&rfds);
            if (listen_sock_ < 0) break;
            FD_SET(listen_sock_, &rfds);
            struct timeval tv{1, 0};
            int sel = select(listen_sock_ + 1, &rfds, nullptr, nullptr, &tv);
            if (sel < 0) {
                if (errno == EINTR) continue;
                break;
            }
            if (sel == 0) continue;
            if (FD_ISSET(listen_sock_, &rfds)) {
                struct sockaddr_storage addr;
                socklen_t addrlen = sizeof(addr);
                int cfd = ::accept(listen_sock_, (struct sockaddr*)&addr, &addrlen);
                if (cfd < 0) {
                    if (errno == EINTR) continue;
                    if (stop_) break;
                    continue;
                }
                client_threads_.emplace_back(&HttpServer::handle_client, this, cfd, addr);
            }
        }
    }

    static bool read_request_line(int fd, std::string &line) {
        line.clear();
        char c;
        while (true) {
            ssize_t r = ::read(fd, &c, 1);
            if (r <= 0) return false;
            if (c == '\r') {
                // Peek next \n
                char n;
                r = ::read(fd, &n, 1);
                if (r <= 0) return false;
                if (n == '\n') break;
            } else if (c == '\n') {
                break;
            } else {
                line.push_back(c);
            }
            if (line.size() > 4096) return false; // sanity
        }
        return true;
    }

    static void send_response(int fd, const std::string &status, const std::string &body, const std::string &content_type = "application/json; charset=utf-8") {
        std::ostringstream oss;
        oss << "HTTP/1.1 " << status << "\r\n";
        oss << "Content-Type: " << content_type << "\r\n";
        oss << "Content-Length: " << body.size() << "\r\n";
        oss << "Connection: close\r\n";
        oss << "Server: modbus-rtu-http-driver\r\n\r\n";
        std::string header = oss.str();
        ::write(fd, header.data(), header.size());
        ::write(fd, body.data(), body.size());
    }

    void handle_client(int fd, sockaddr_storage addr) {
        // Read request line
        std::string line;
        if (!read_request_line(fd, line)) {
            ::close(fd);
            return;
        }
        std::istringstream iss(line);
        std::string method, path, version;
        iss >> method >> path >> version;
        // Read and ignore headers until empty line
        std::string hdr;
        while (read_request_line(fd, hdr)) {
            if (hdr.empty()) break; // end of headers
        }

        if (method != "GET") {
            send_response(fd, "405 Method Not Allowed", "Method Not Allowed", "text/plain; charset=utf-8");
            ::close(fd);
            return;
        }

        if (path == "/status") {
            std::string body;
            {
                std::lock_guard<std::mutex> lk(st_.mtx);
                std::ostringstream b;
                b << "{";
                b << "\"serial_connected\":" << (st_.serial_connected ? "true" : "false") << ",";
                b << "\"serial_port\":\"" << st_.port << "\",";
                b << "\"slave_id\":" << st_.slave_id << ",";
                b << "\"baud\":" << st_.baud << ",";
                b << "\"polling_active\":" << (st_.polling_active ? "true" : "false") << ",";
                b << "\"poll_interval_ms\":" << st_.poll_interval_ms << ",";
                b << "\"last_poll_time\":\"" << st_.latest.last_poll_ts << "\"";
                b << "}";
                body = b.str();
            }
            send_response(fd, "200 OK", body);
        } else if (path == "/readings") {
            std::string body;
            {
                std::lock_guard<std::mutex> lk(st_.mtx);
                std::ostringstream b;
                b << "{";
                b << "\"temperature\":";
                if (st_.latest.temp.has) b << std::fixed << std::setprecision(3) << st_.latest.temp.value; else b << "null";
                b << ",\"humidity\":";
                if (st_.latest.hum.has) b << std::fixed << std::setprecision(3) << st_.latest.hum.value; else b << "null";
                b << ",\"co2\":";
                if (st_.latest.co2.has) b << std::fixed << std::setprecision(3) << st_.latest.co2.value; else b << "null";
                b << ",\"timestamps\":{";
                b << "\"temperature\":\"" << (st_.latest.temp.has ? st_.latest.temp.ts : "") << "\",";
                b << "\"humidity\":\"" << (st_.latest.hum.has ? st_.latest.hum.ts : "") << "\",";
                b << "\"co2\":\"" << (st_.latest.co2.has ? st_.latest.co2.ts : "") << "\"}";
                b << ",\"last_poll_time\":\"" << st_.latest.last_poll_ts << "\"";
                b << "}";
                body = b.str();
            }
            send_response(fd, "200 OK", body);
        } else {
            send_response(fd, "404 Not Found", "Not Found", "text/plain; charset=utf-8");
        }
        ::close(fd);
    }

    const Config& cfg_;
    SharedState& st_;
    std::atomic<bool> stop_{false};
    int listen_sock_ = -1;
    std::thread accept_th_;
    std::vector<std::thread> client_threads_;
};

static void signal_handler(int) {
    g_shutdown = true;
}

int main() {
    // Load configuration from environment
    Config cfg;
    cfg.http_host = getenv_or("HTTP_HOST", "0.0.0.0");
    cfg.http_port = getenv_int("HTTP_PORT", 8080);
    cfg.serial_port = getenv_or("SERIAL_PORT", "/dev/ttyUSB0");
    cfg.serial_baud = getenv_int("SERIAL_BAUD", 9600);
    cfg.slave_id = getenv_int("MODBUS_SLAVE_ID", 1);
    cfg.reg_temp = getenv_uint16("REG_TEMP_ADDR", 0x0000);
    cfg.reg_hum  = getenv_uint16("REG_HUM_ADDR",  0x0001);
    cfg.reg_co2  = getenv_uint16("REG_CO2_ADDR",  0x0002);
    cfg.temp_scale = getenv_double("TEMP_SCALE", 1.0);
    cfg.hum_scale  = getenv_double("HUM_SCALE",  1.0);
    cfg.co2_scale  = getenv_double("CO2_SCALE",  1.0);
    cfg.poll_interval_ms = getenv_int("POLL_INTERVAL_MS", 1000);
    cfg.read_timeout_ms  = getenv_int("READ_TIMEOUT_MS", 500);
    cfg.backoff_initial_ms = getenv_int("BACKOFF_INITIAL_MS", 500);
    cfg.backoff_max_ms     = getenv_int("BACKOFF_MAX_MS", 10000);

    SharedState state;
    state.port = cfg.serial_port;
    state.baud = cfg.serial_baud;
    state.slave_id = cfg.slave_id;

    // Setup signal handlers for graceful shutdown
    std::signal(SIGINT, signal_handler);
    std::signal(SIGTERM, signal_handler);

    Poller poller(cfg, state);
    poller.start();

    HttpServer server(cfg, state);
    if (!server.start()) {
        log_msg("ERROR", "Failed to start HTTP server");
        poller.stop();
        return 1;
    }

    log_msg("INFO", "Driver started");

    // Wait until shutdown requested
    while (!g_shutdown.load()) {
        std::this_thread::sleep_for(std::chrono::milliseconds(200));
    }

    log_msg("INFO", "Shutting down...");
    server.stop();
    poller.stop();
    log_msg("INFO", "Shutdown complete");
    return 0;
}
