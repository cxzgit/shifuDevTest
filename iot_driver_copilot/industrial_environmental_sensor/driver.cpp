#include <iostream>
#include <string>
#include <vector>
#include <thread>
#include <mutex>
#include <atomic>
#include <chrono>
#include <cstdlib>
#include <cstdint>
#include <csignal>
#include <cstring>
#include <cerrno>
#include <ctime>
#include <sstream>

#include <unistd.h>
#include <fcntl.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/select.h>
#include <termios.h>

// ==========================
// Utility: time formatting
// ==========================
static std::string iso8601_utc(std::chrono::system_clock::time_point tp) {
    std::time_t t = std::chrono::system_clock::to_time_t(tp);
    struct tm gm;
    gmtime_r(&t, &gm);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &gm);
    return std::string(buf);
}

// ==========================
// Logging
// ==========================
static std::mutex log_mutex;
static void log_info(const std::string &msg) {
    std::lock_guard<std::mutex> lk(log_mutex);
    auto now = std::chrono::system_clock::now();
    std::cerr << "[INFO] " << iso8601_utc(now) << " - " << msg << std::endl;
}
static void log_error(const std::string &msg) {
    std::lock_guard<std::mutex> lk(log_mutex);
    auto now = std::chrono::system_clock::now();
    std::cerr << "[ERROR] " << iso8601_utc(now) << " - " << msg << std::endl;
}

// ==========================
// CRC16 (Modbus)
// ==========================
static uint16_t modbus_crc16(const uint8_t *data, size_t len) {
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

// ==========================
// Configuration
// ==========================
struct Config {
    std::string http_host;
    int http_port;

    std::string serial_port;
    int baud_rate;
    int data_bits;
    char parity; // 'N', 'E', 'O'
    int stop_bits;

    int modbus_id;

    int reg_temp_addr;
    int reg_temp_len;
    double temp_scale;
    double temp_offset;

    int reg_hum_addr;
    int reg_hum_len;
    double hum_scale;
    double hum_offset;

    int reg_sample_addr;
    int reg_sample_len;

    int read_timeout_ms;
    int write_timeout_ms;
    int poll_interval_ms;

    int retry_base_ms;
    int retry_max_ms;

    bool auto_reconnect;

    bool set_sample_interval_present;
    int set_sample_interval_s; // 1..60 if present
};

static bool getenv_str(const char *key, std::string &out) {
    const char *val = std::getenv(key);
    if (!val) return false;
    out = std::string(val);
    return true;
}
static bool getenv_int(const char *key, int &out) {
    std::string s;
    if (!getenv_str(key, s)) return false;
    try {
        out = std::stoi(s);
    } catch (...) { return false; }
    return true;
}
static bool getenv_double(const char *key, double &out) {
    std::string s;
    if (!getenv_str(key, s)) return false;
    try {
        out = std::stod(s);
    } catch (...) { return false; }
    return true;
}

static Config load_config_or_exit() {
    Config cfg{};
    std::string tmp;

    if (!getenv_str("HTTP_HOST", cfg.http_host)) {
        log_error("Missing env: HTTP_HOST");
        std::exit(1);
    }
    if (!getenv_int("HTTP_PORT", cfg.http_port)) {
        log_error("Missing or invalid env: HTTP_PORT");
        std::exit(1);
    }

    if (!getenv_str("SERIAL_PORT", cfg.serial_port)) {
        log_error("Missing env: SERIAL_PORT");
        std::exit(1);
    }
    if (!getenv_int("BAUD_RATE", cfg.baud_rate)) {
        log_error("Missing or invalid env: BAUD_RATE");
        std::exit(1);
    }
    if (!getenv_int("DATA_BITS", cfg.data_bits)) {
        log_error("Missing or invalid env: DATA_BITS");
        std::exit(1);
    }
    if (!getenv_str("PARITY", tmp)) {
        log_error("Missing env: PARITY (N/E/O)");
        std::exit(1);
    }
    if (tmp.size() != 1 || (tmp[0] != 'N' && tmp[0] != 'E' && tmp[0] != 'O')) {
        log_error("Invalid PARITY: must be N/E/O");
        std::exit(1);
    }
    cfg.parity = tmp[0];
    if (!getenv_int("STOP_BITS", cfg.stop_bits)) {
        log_error("Missing or invalid env: STOP_BITS");
        std::exit(1);
    }

    if (!getenv_int("MODBUS_ID", cfg.modbus_id)) {
        log_error("Missing or invalid env: MODBUS_ID");
        std::exit(1);
    }

    if (!getenv_int("REG_TEMP_ADDR", cfg.reg_temp_addr)) {
        log_error("Missing or invalid env: REG_TEMP_ADDR");
        std::exit(1);
    }
    if (!getenv_int("REG_TEMP_LEN", cfg.reg_temp_len)) {
        log_error("Missing or invalid env: REG_TEMP_LEN");
        std::exit(1);
    }
    if (!getenv_double("TEMP_SCALE", cfg.temp_scale)) {
        log_error("Missing or invalid env: TEMP_SCALE");
        std::exit(1);
    }
    if (!getenv_double("TEMP_OFFSET", cfg.temp_offset)) {
        log_error("Missing or invalid env: TEMP_OFFSET");
        std::exit(1);
    }

    if (!getenv_int("REG_HUM_ADDR", cfg.reg_hum_addr)) {
        log_error("Missing or invalid env: REG_HUM_ADDR");
        std::exit(1);
    }
    if (!getenv_int("REG_HUM_LEN", cfg.reg_hum_len)) {
        log_error("Missing or invalid env: REG_HUM_LEN");
        std::exit(1);
    }
    if (!getenv_double("HUM_SCALE", cfg.hum_scale)) {
        log_error("Missing or invalid env: HUM_SCALE");
        std::exit(1);
    }
    if (!getenv_double("HUM_OFFSET", cfg.hum_offset)) {
        log_error("Missing or invalid env: HUM_OFFSET");
        std::exit(1);
    }

    if (!getenv_int("REG_SAMPLE_ADDR", cfg.reg_sample_addr)) {
        log_error("Missing or invalid env: REG_SAMPLE_ADDR");
        std::exit(1);
    }
    if (!getenv_int("REG_SAMPLE_LEN", cfg.reg_sample_len)) {
        log_error("Missing or invalid env: REG_SAMPLE_LEN");
        std::exit(1);
    }

    if (!getenv_int("READ_TIMEOUT_MS", cfg.read_timeout_ms)) {
        log_error("Missing or invalid env: READ_TIMEOUT_MS");
        std::exit(1);
    }
    if (!getenv_int("WRITE_TIMEOUT_MS", cfg.write_timeout_ms)) {
        log_error("Missing or invalid env: WRITE_TIMEOUT_MS");
        std::exit(1);
    }
    if (!getenv_int("POLL_INTERVAL_MS", cfg.poll_interval_ms)) {
        log_error("Missing or invalid env: POLL_INTERVAL_MS");
        std::exit(1);
    }

    if (!getenv_int("RETRY_BASE_MS", cfg.retry_base_ms)) {
        log_error("Missing or invalid env: RETRY_BASE_MS");
        std::exit(1);
    }
    if (!getenv_int("RETRY_MAX_MS", cfg.retry_max_ms)) {
        log_error("Missing or invalid env: RETRY_MAX_MS");
        std::exit(1);
    }

    if (!getenv_str("AUTO_RECONNECT", tmp)) {
        log_error("Missing env: AUTO_RECONNECT (enabled|disabled)");
        std::exit(1);
    }
    if (tmp == "enabled") cfg.auto_reconnect = true;
    else if (tmp == "disabled") cfg.auto_reconnect = false;
    else {
        log_error("Invalid AUTO_RECONNECT: must be enabled|disabled");
        std::exit(1);
    }

    // Optional: set sample interval on startup
    int si;
    if (getenv_int("SET_SAMPLE_INTERVAL_S", si)) {
        cfg.set_sample_interval_present = true;
        cfg.set_sample_interval_s = si;
    } else {
        cfg.set_sample_interval_present = false;
        cfg.set_sample_interval_s = 0;
    }

    return cfg;
}

// ==========================
// Serial Port
// ==========================
class SerialPort {
public:
    SerialPort() : fd_(-1), opened_(false) {}

    bool open(const std::string &path, int baud_rate, int data_bits, char parity, int stop_bits) {
        close();
        fd_ = ::open(path.c_str(), O_RDWR | O_NOCTTY | O_NONBLOCK);
        if (fd_ < 0) {
            log_error("Failed to open serial port: " + path + ": " + std::strerror(errno));
            return false;
        }
        // Clear O_NONBLOCK after open; we'll use select for timeouts
        int flags = fcntl(fd_, F_GETFL, 0);
        if (flags >= 0) fcntl(fd_, F_SETFL, flags & ~O_NONBLOCK);

        struct termios tio{};
        if (tcgetattr(fd_, &tio) != 0) {
            log_error("tcgetattr failed: " + std::string(std::strerror(errno)));
            ::close(fd_);
            fd_ = -1;
            return false;
        }

        cfmakeraw(&tio);
        tio.c_cflag |= (CLOCAL | CREAD);

        // Baud
        speed_t speed;
        switch (baud_rate) {
            case 1200: speed = B1200; break;
            case 2400: speed = B2400; break;
            case 4800: speed = B4800; break;
            case 9600: speed = B9600; break;
            case 19200: speed = B19200; break;
            case 38400: speed = B38400; break;
            case 57600: speed = B57600; break;
            case 115200: speed = B115200; break;
            default:
                log_error("Unsupported BAUD_RATE: " + std::to_string(baud_rate));
                ::close(fd_);
                fd_ = -1;
                return false;
        }
        cfsetispeed(&tio, speed);
        cfsetospeed(&tio, speed);

        // Data bits
        tio.c_cflag &= ~CSIZE;
        if (data_bits == 7) tio.c_cflag |= CS7;
        else if (data_bits == 8) tio.c_cflag |= CS8;
        else {
            log_error("Unsupported DATA_BITS: " + std::to_string(data_bits) + ", expected 7 or 8");
            ::close(fd_);
            fd_ = -1;
            return false;
        }

        // Parity
        if (parity == 'N') {
            tio.c_cflag &= ~PARENB;
        } else if (parity == 'E') {
            tio.c_cflag |= PARENB;
            tio.c_cflag &= ~PARODD;
        } else if (parity == 'O') {
            tio.c_cflag |= PARENB;
            tio.c_cflag |= PARODD;
        } else {
            log_error("Invalid parity");
            ::close(fd_);
            fd_ = -1;
            return false;
        }

        // Stop bits
        if (stop_bits == 1) {
            tio.c_cflag &= ~CSTOPB;
        } else if (stop_bits == 2) {
            tio.c_cflag |= CSTOPB;
        } else {
            log_error("Unsupported STOP_BITS: " + std::to_string(stop_bits) + ", expected 1 or 2");
            ::close(fd_);
            fd_ = -1;
            return false;
        }

        // Timeouts via select(), set non-blocking lines
        tio.c_cc[VMIN] = 0;
        tio.c_cc[VTIME] = 0;

        if (tcsetattr(fd_, TCSANOW, &tio) != 0) {
            log_error("tcsetattr failed: " + std::string(std::strerror(errno)));
            ::close(fd_);
            fd_ = -1;
            return false;
        }

        opened_ = true;
        return true;
    }

    void close() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
        }
        opened_ = false;
    }

    bool is_open() const { return opened_; }

    bool write_exact(const uint8_t *data, size_t len, int timeout_ms) {
        if (fd_ < 0) return false;
        size_t sent = 0;
        while (sent < len) {
            fd_set wfds;
            FD_ZERO(&wfds);
            FD_SET(fd_, &wfds);
            struct timeval tv{timeout_ms / 1000, (timeout_ms % 1000) * 1000};
            int r = select(fd_ + 1, nullptr, &wfds, nullptr, &tv);
            if (r <= 0) return false; // timeout or error
            ssize_t w = ::write(fd_, data + sent, len - sent);
            if (w < 0) {
                if (errno == EINTR) continue;
                return false;
            }
            sent += static_cast<size_t>(w);
        }
        return true;
    }

    bool read_exact(uint8_t *buf, size_t len, int timeout_ms) {
        if (fd_ < 0) return false;
        size_t recvd = 0;
        auto start = std::chrono::steady_clock::now();
        while (recvd < len) {
            auto now = std::chrono::steady_clock::now();
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
            int remain = timeout_ms - elapsed;
            if (remain <= 0) return false;

            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(fd_, &rfds);
            struct timeval tv{remain / 1000, (remain % 1000) * 1000};
            int r = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
            if (r < 0) {
                if (errno == EINTR) continue;
                return false;
            }
            if (r == 0) {
                // timeout
                return false;
            }
            ssize_t got = ::read(fd_, buf + recvd, len - recvd);
            if (got < 0) {
                if (errno == EINTR) continue;
                return false;
            }
            if (got == 0) {
                // no data available
                continue;
            }
            recvd += static_cast<size_t>(got);
        }
        return true;
    }

private:
    int fd_;
    bool opened_;
};

// ==========================
// Modbus RTU helpers
// ==========================
static std::vector<uint8_t> build_read_holding(uint8_t id, uint16_t addr, uint16_t qty) {
    std::vector<uint8_t> frame;
    frame.reserve(8);
    frame.push_back(id);
    frame.push_back(0x03);
    frame.push_back((uint8_t)((addr >> 8) & 0xFF));
    frame.push_back((uint8_t)(addr & 0xFF));
    frame.push_back((uint8_t)((qty >> 8) & 0xFF));
    frame.push_back((uint8_t)(qty & 0xFF));
    uint16_t crc = modbus_crc16(frame.data(), frame.size());
    frame.push_back((uint8_t)(crc & 0xFF));
    frame.push_back((uint8_t)((crc >> 8) & 0xFF));
    return frame;
}

static std::vector<uint8_t> build_write_single(uint8_t id, uint16_t addr, uint16_t value) {
    std::vector<uint8_t> frame;
    frame.reserve(8);
    frame.push_back(id);
    frame.push_back(0x06);
    frame.push_back((uint8_t)((addr >> 8) & 0xFF));
    frame.push_back((uint8_t)(addr & 0xFF));
    frame.push_back((uint8_t)((value >> 8) & 0xFF));
    frame.push_back((uint8_t)(value & 0xFF));
    uint16_t crc = modbus_crc16(frame.data(), frame.size());
    frame.push_back((uint8_t)(crc & 0xFF));
    frame.push_back((uint8_t)((crc >> 8) & 0xFF));
    return frame;
}

// ==========================
// Driver State
// ==========================
enum class ConnState { Disconnected, Reconnecting, Connected };
enum class ErrType { None, Timeout, CrcMismatch, InvalidResponse };

struct DriverState {
    std::string port;
    int device_id;
    ConnState connection_state = ConnState::Disconnected;

    ErrType last_error = ErrType::None;
    std::string last_error_msg;

    uint64_t timeouts = 0;
    uint64_t crc_mismatches = 0;
    uint64_t invalid_responses = 0;

    int retries_in_progress = 0;
    bool auto_reconnect = true;

    int sample_interval_s = -1;

    std::chrono::system_clock::time_point last_seen = std::chrono::system_clock::time_point{};

    // latest sensor values (not exposed via HTTP, but collected)
    double last_temp = 0.0;
    double last_hum = 0.0;
    std::chrono::system_clock::time_point last_update;
};

static std::mutex state_mutex;
static DriverState g_state;
static std::atomic<bool> g_running(true);

static std::string conn_state_str(ConnState cs) {
    switch (cs) {
        case ConnState::Disconnected: return "disconnected";
        case ConnState::Reconnecting: return "reconnecting";
        case ConnState::Connected: return "connected";
    }
    return "disconnected";
}

static std::string err_type_str(ErrType et) {
    switch (et) {
        case ErrType::None: return "none";
        case ErrType::Timeout: return "timeout";
        case ErrType::CrcMismatch: return "crc_mismatch";
        case ErrType::InvalidResponse: return "invalid_response";
    }
    return "none";
}

// ==========================
// Collector Loop
// ==========================
static bool read_registers(SerialPort &sp, const Config &cfg, uint16_t addr, uint16_t qty, std::vector<uint8_t> &out_bytes, ErrType &err_type, std::string &err_msg) {
    out_bytes.clear();
    std::vector<uint8_t> req = build_read_holding((uint8_t)cfg.modbus_id, addr, qty);
    if (!sp.write_exact(req.data(), req.size(), cfg.write_timeout_ms)) {
        err_type = ErrType::Timeout;
        err_msg = "write timeout";
        return false;
    }
    size_t expected = 5 + qty * 2; // id + func + bytecount + data + crc(2)
    std::vector<uint8_t> resp(expected);
    if (!sp.read_exact(resp.data(), resp.size(), cfg.read_timeout_ms)) {
        err_type = ErrType::Timeout;
        err_msg = "read timeout";
        return false;
    }
    // Validate CRC
    uint16_t got_crc = (uint16_t)resp[resp.size()-1] << 8 | (uint16_t)resp[resp.size()-2];
    uint16_t calc_crc = modbus_crc16(resp.data(), resp.size()-2);
    if (got_crc != calc_crc) {
        err_type = ErrType::CrcMismatch;
        err_msg = "CRC mismatch";
        return false;
    }
    // Validate header
    if (resp[0] != (uint8_t)cfg.modbus_id) {
        err_type = ErrType::InvalidResponse;
        err_msg = "slave id mismatch";
        return false;
    }
    if (resp[1] == 0x83) {
        err_type = ErrType::InvalidResponse;
        err_msg = "Modbus exception response";
        return false;
    }
    if (resp[1] != 0x03) {
        err_type = ErrType::InvalidResponse;
        err_msg = "unexpected function code";
        return false;
    }
    if (resp[2] != (uint8_t)(qty * 2)) {
        err_type = ErrType::InvalidResponse;
        err_msg = "byte count mismatch";
        return false;
    }
    out_bytes.assign(resp.begin()+3, resp.begin()+3 + qty*2);
    err_type = ErrType::None;
    err_msg.clear();
    return true;
}

static bool write_single_register(SerialPort &sp, const Config &cfg, uint16_t addr, uint16_t value, ErrType &err_type, std::string &err_msg) {
    std::vector<uint8_t> req = build_write_single((uint8_t)cfg.modbus_id, addr, value);
    if (!sp.write_exact(req.data(), req.size(), cfg.write_timeout_ms)) {
        err_type = ErrType::Timeout;
        err_msg = "write timeout";
        return false;
    }
    // Expect echo: id, func, addr(2), value(2), crc(2)
    std::vector<uint8_t> resp(8);
    if (!sp.read_exact(resp.data(), resp.size(), cfg.read_timeout_ms)) {
        err_type = ErrType::Timeout;
        err_msg = "read timeout";
        return false;
    }
    uint16_t got_crc = (uint16_t)resp[7] << 8 | (uint16_t)resp[6];
    uint16_t calc_crc = modbus_crc16(resp.data(), 6);
    if (got_crc != calc_crc) {
        err_type = ErrType::CrcMismatch;
        err_msg = "CRC mismatch on write ack";
        return false;
    }
    if (resp[0] != (uint8_t)cfg.modbus_id || resp[1] != 0x06) {
        err_type = ErrType::InvalidResponse;
        err_msg = "unexpected write ack";
        return false;
    }
    uint16_t raddr = (uint16_t)resp[2] << 8 | (uint16_t)resp[3];
    uint16_t rval = (uint16_t)resp[4] << 8 | (uint16_t)resp[5];
    if (raddr != addr || rval != value) {
        err_type = ErrType::InvalidResponse;
        err_msg = "write ack mismatch";
        return false;
    }
    err_type = ErrType::None;
    err_msg.clear();
    return true;
}

static void apply_error(ErrType et, const std::string &msg) {
    std::lock_guard<std::mutex> lk(state_mutex);
    g_state.last_error = et;
    g_state.last_error_msg = msg;
    switch (et) {
        case ErrType::Timeout: g_state.timeouts++; break;
        case ErrType::CrcMismatch: g_state.crc_mismatches++; break;
        case ErrType::InvalidResponse: g_state.invalid_responses++; break;
        default: break;
    }
}

static void collector_loop(Config cfg) {
    SerialPort sp;

    {
        std::lock_guard<std::mutex> lk(state_mutex);
        g_state.port = cfg.serial_port;
        g_state.device_id = cfg.modbus_id;
        g_state.auto_reconnect = cfg.auto_reconnect;
        g_state.connection_state = ConnState::Disconnected;
    }

    int backoff = cfg.retry_base_ms;

    while (g_running.load()) {
        if (!sp.is_open()) {
            {
                std::lock_guard<std::mutex> lk(state_mutex);
                g_state.connection_state = cfg.auto_reconnect ? ConnState::Reconnecting : ConnState::Disconnected;
                g_state.retries_in_progress++;
            }
            if (!cfg.auto_reconnect) {
                std::this_thread::sleep_for(std::chrono::milliseconds(200));
                continue;
            }
            log_info("Attempting to open serial port: " + cfg.serial_port);
            if (sp.open(cfg.serial_port, cfg.baud_rate, cfg.data_bits, cfg.parity, cfg.stop_bits)) {
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.connection_state = ConnState::Connected;
                    g_state.retries_in_progress = 0;
                }
                log_info("Serial port opened: " + cfg.serial_port);
                backoff = cfg.retry_base_ms; // reset backoff
                // Optional: set sample interval if requested
                if (cfg.set_sample_interval_present) {
                    ErrType et; std::string em;
                    int value = cfg.set_sample_interval_s;
                    log_info("Writing sample interval to device: " + std::to_string(value) + " s");
                    if (!write_single_register(sp, cfg, (uint16_t)cfg.reg_sample_addr, (uint16_t)value, et, em)) {
                        apply_error(et, em);
                        log_error("Failed to set sample interval: " + em);
                    } else {
                        log_info("Sample interval set successfully");
                    }
                }
            } else {
                apply_error(ErrType::Timeout, "serial open failed");
                log_error("Cannot open serial port; retrying in " + std::to_string(backoff) + " ms");
                std::this_thread::sleep_for(std::chrono::milliseconds(backoff));
                backoff = std::min(backoff * 2, cfg.retry_max_ms);
                continue;
            }
        }

        // When connected, perform polling
        if (sp.is_open()) {
            // Read sample interval
            ErrType et; std::string em; std::vector<uint8_t> bytes;
            if (!read_registers(sp, cfg, (uint16_t)cfg.reg_sample_addr, (uint16_t)cfg.reg_sample_len, bytes, et, em)) {
                apply_error(et, em);
                log_error("Sample interval read failed: " + em);
                // treat as connection failure -> close and backoff
                sp.close();
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.connection_state = ConnState::Reconnecting;
                }
                std::this_thread::sleep_for(std::chrono::milliseconds(backoff));
                backoff = std::min(backoff * 2, cfg.retry_max_ms);
                continue;
            } else {
                int si = -1;
                if (!bytes.empty()) {
                    // Use first register value as seconds
                    si = ((int)bytes[0] << 8) | (int)bytes[1];
                }
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.sample_interval_s = si;
                }
            }

            // Read temperature
            if (!read_registers(sp, cfg, (uint16_t)cfg.reg_temp_addr, (uint16_t)cfg.reg_temp_len, bytes, et, em)) {
                apply_error(et, em);
                log_error("Temperature read failed: " + em);
                sp.close();
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.connection_state = ConnState::Reconnecting;
                }
                std::this_thread::sleep_for(std::chrono::milliseconds(backoff));
                backoff = std::min(backoff * 2, cfg.retry_max_ms);
                continue;
            } else {
                double temp_raw = 0.0;
                if (cfg.reg_temp_len == 1 && bytes.size() >= 2) {
                    uint16_t v = (uint16_t)bytes[0] << 8 | (uint16_t)bytes[1];
                    temp_raw = (double)v;
                } else if (cfg.reg_temp_len == 2 && bytes.size() >= 4) {
                    uint32_t v = (uint32_t)bytes[0] << 24 | (uint32_t)bytes[1] << 16 | (uint32_t)bytes[2] << 8 | (uint32_t)bytes[3];
                    temp_raw = (double)v;
                }
                double temp = temp_raw * cfg.temp_scale + cfg.temp_offset;
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.last_temp = temp;
                    g_state.last_seen = std::chrono::system_clock::now();
                    g_state.last_update = g_state.last_seen;
                }
            }

            // Read humidity
            if (!read_registers(sp, cfg, (uint16_t)cfg.reg_hum_addr, (uint16_t)cfg.reg_hum_len, bytes, et, em)) {
                apply_error(et, em);
                log_error("Humidity read failed: " + em);
                sp.close();
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.connection_state = ConnState::Reconnecting;
                }
                std::this_thread::sleep_for(std::chrono::milliseconds(backoff));
                backoff = std::min(backoff * 2, cfg.retry_max_ms);
                continue;
            } else {
                double hum_raw = 0.0;
                if (cfg.reg_hum_len == 1 && bytes.size() >= 2) {
                    uint16_t v = (uint16_t)bytes[0] << 8 | (uint16_t)bytes[1];
                    hum_raw = (double)v;
                } else if (cfg.reg_hum_len == 2 && bytes.size() >= 4) {
                    uint32_t v = (uint32_t)bytes[0] << 24 | (uint32_t)bytes[1] << 16 | (uint32_t)bytes[2] << 8 | (uint32_t)bytes[3];
                    hum_raw = (double)v;
                }
                double hum = hum_raw * cfg.hum_scale + cfg.hum_offset;
                {
                    std::lock_guard<std::mutex> lk(state_mutex);
                    g_state.last_hum = hum;
                    g_state.last_seen = std::chrono::system_clock::now();
                    g_state.last_update = g_state.last_seen;
                }
                log_info("Sample: temp=" + std::to_string(g_state.last_temp) + " C, hum=" + std::to_string(g_state.last_hum) + " %RH");
            }

            // Sleep according to polling interval
            int ms = cfg.poll_interval_ms;
            if (ms < 1) ms = 1;
            for (int i = 0; i < ms && g_running.load(); i += 50) {
                std::this_thread::sleep_for(std::chrono::milliseconds(50));
            }
        }
    }

    if (sp.is_open()) {
        sp.close();
        log_info("Serial port closed");
    }
}

// ==========================
// HTTP Server (/status)
// ==========================
static void handle_client(int client_fd) {
    // Read simple HTTP request
    char buf[2048];
    ssize_t r = ::read(client_fd, buf, sizeof(buf));
    if (r <= 0) { ::close(client_fd); return; }
    std::string req(buf, buf + r);

    // Parse first line
    size_t pos = req.find("\r\n");
    std::string first = pos == std::string::npos ? req : req.substr(0, pos);
    // Expected: GET /status HTTP/1.1
    bool ok = false;
    if (first.rfind("GET ", 0) == 0) {
        size_t sp = first.find(' ');
        size_t sp2 = first.find(' ', sp + 1);
        if (sp != std::string::npos && sp2 != std::string::npos) {
            std::string path = first.substr(sp + 1, sp2 - (sp + 1));
            if (path == "/status") ok = true;
        }
    }

    std::string body;
    int status_code = 200;
    if (!ok) {
        status_code = 404;
        body = "{\"error\":\"not found\"}";
    } else {
        // Build status JSON from g_state
        DriverState snapshot;
        {
            std::lock_guard<std::mutex> lk(state_mutex);
            snapshot = g_state;
        }
        std::ostringstream oss;
        oss << "{";
        oss << "\"connection_state\":\"" << conn_state_str(snapshot.connection_state) << "\",";
        oss << "\"port\":\"" << snapshot.port << "\",";
        oss << "\"device_id\":" << snapshot.device_id << ",";
        if (snapshot.last_seen.time_since_epoch().count() != 0) {
            oss << "\"last_seen\":\"" << iso8601_utc(snapshot.last_seen) << "\",";
        } else {
            oss << "\"last_seen\":null,";
        }
        oss << "\"last_error\":{";
        oss << "\"type\":\"" << err_type_str(snapshot.last_error) << "\",";
        oss << "\"message\":\"";
        // escape quotes minimally
        for (char c : snapshot.last_error_msg) {
            if (c == '"') oss << "\\\""; else if (c == '\\') oss << "\\\\"; else if (c == '\n') oss << "\\n"; else oss << c;
        }
        oss << """};";
        oss << "\"error_counters\":{";
        oss << "\"timeouts\":" << snapshot.timeouts << ",";
        oss << "\"crc_mismatches\":" << snapshot.crc_mismatches << ",";
        oss << "\"invalid_responses\":" << snapshot.invalid_responses;
        oss << "},";
        oss << "\"retries_in_progress\":" << snapshot.retries_in_progress << ",";
        oss << "\"auto_reconnect\":\"" << (snapshot.auto_reconnect ? "enabled" : "disabled") << "\",";
        oss << "\"sample_interval_s\":" << snapshot.sample_interval_s;
        oss << "}";
        body = oss.str();
    }

    std::ostringstream resp;
    resp << "HTTP/1.1 " << status_code << (status_code == 200 ? " OK" : " Not Found") << "\r\n";
    resp << "Content-Type: application/json\r\n";
    resp << "Content-Length: " << body.size() << "\r\n";
    resp << "Connection: close\r\n\r\n";
    resp << body;

    std::string resp_str = resp.str();
    (void)::write(client_fd, resp_str.data(), resp_str.size());
    ::close(client_fd);
}

static int start_http_server(const std::string &host, int port) {
    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) {
        log_error("HTTP socket() failed: " + std::string(std::strerror(errno)));
        return -1;
    }
    int opt = 1;
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    struct sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(port);
    if (inet_pton(AF_INET, host.c_str(), &addr.sin_addr) != 1) {
        log_error("Invalid HTTP_HOST (IPv4 only): " + host);
        ::close(fd);
        return -1;
    }

    if (bind(fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        log_error("HTTP bind() failed: " + std::string(std::strerror(errno)));
        ::close(fd);
        return -1;
    }
    if (listen(fd, 16) < 0) {
        log_error("HTTP listen() failed: " + std::string(std::strerror(errno)));
        ::close(fd);
        return -1;
    }

    log_info("HTTP server listening on " + host + ":" + std::to_string(port));

    while (g_running.load()) {
        struct sockaddr_in cli{};
        socklen_t clilen = sizeof(cli);
        int cfd = accept(fd, (struct sockaddr *)&cli, &clilen);
        if (cfd < 0) {
            if (errno == EINTR) continue;
            if (!g_running.load()) break;
            log_error("HTTP accept() failed: " + std::string(std::strerror(errno)));
            continue;
        }
        std::thread th(handle_client, cfd);
        th.detach();
    }

    ::close(fd);
    return 0;
}

// ==========================
// Signal handling
// ==========================
static void signal_handler(int) {
    g_running.store(false);
}

// ==========================
// Main
// ==========================
int main() {
    Config cfg = load_config_or_exit();

    std::signal(SIGINT, signal_handler);
    std::signal(SIGTERM, signal_handler);

    std::thread collector([&cfg]() { collector_loop(cfg); });

    int http_rc = start_http_server(cfg.http_host, cfg.http_port);
    if (http_rc != 0) {
        log_error("HTTP server failed to start");
        g_running.store(false);
    }

    collector.join();
    log_info("Driver exited");
    return 0;
}
