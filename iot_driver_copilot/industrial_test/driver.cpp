#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <termios.h>
#include <time.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <functional>
#include <iomanip>
#include <iostream>
#include <map>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <utility>
#include <vector>

using namespace std::chrono_literals;

static std::atomic<bool> g_shutdown{false};

static std::string now_iso8601() {
    auto now = std::chrono::system_clock::now();
    std::time_t t = std::chrono::system_clock::to_time_t(now);
    std::tm tm{};
    localtime_r(&t, &tm);
    char buf[64];
    std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%S%z", &tm);
    return std::string(buf);
}

static void log_msg(const char* level, const char* fmt, ...) {
    char msg[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(msg, sizeof(msg), fmt, ap);
    va_end(ap);
    fprintf(stderr, "%s [%s] %s\n", now_iso8601().c_str(), level, msg);
    fflush(stderr);
}

static std::string getenv_str(const char* name, const char* defv) {
    const char* v = std::getenv(name);
    if (!v) return std::string(defv ? defv : "");
    return std::string(v);
}

static long getenv_long(const char* name, long defv) {
    const char* v = std::getenv(name);
    if (!v || *v == '\0') return defv;
    char* end = nullptr;
    long res = std::strtol(v, &end, 10);
    if (end == v) return defv;
    return res;
}

static double getenv_double(const char* name, double defv) {
    const char* v = std::getenv(name);
    if (!v || *v == '\0') return defv;
    char* end = nullptr;
    double res = std::strtod(v, &end);
    if (end == v) return defv;
    return res;
}

enum class DataType { UINT16, INT16, UINT32, INT32, FLOAT32 };

static bool parse_datatype(const std::string& s, DataType& out) {
    std::string t;
    t.resize(s.size());
    std::transform(s.begin(), s.end(), t.begin(), [](unsigned char c){ return std::tolower(c); });
    if (t == "uint16") { out = DataType::UINT16; return true; }
    if (t == "int16") { out = DataType::INT16; return true; }
    if (t == "uint32") { out = DataType::UINT32; return true; }
    if (t == "int32") { out = DataType::INT32; return true; }
    if (t == "float32") { out = DataType::FLOAT32; return true; }
    return false;
}

static bool parse_endian(const std::string& s, bool& big_endian) {
    std::string t;
    t.resize(s.size());
    std::transform(s.begin(), s.end(), t.begin(), [](unsigned char c){ return std::tolower(c); });
    if (t == "be" || t == "big" || t == "big-endian") { big_endian = true; return true; }
    if (t == "le" || t == "little" || t == "little-endian") { big_endian = false; return true; }
    return false;
}

struct RegMap {
    int addr = -1;
    int count = 1; // 1 or 2 registers
    DataType type = DataType::UINT16;
    bool big_endian_words = true; // for 32-bit types across registers
    double scale = 1.0;
};

struct Config {
    std::string http_host = "0.0.0.0";
    int http_port = 8080;

    std::string modbus_port; // required
    int slave_id = -1; // required

    int baud = 9600;
    int data_bits = 8;
    char parity = 'N';
    int stop_bits = 1;
    int timeout_ms = 1000;

    int poll_interval_ms = 1000; // initial
    int backoff_initial_ms = 500;
    int backoff_max_ms = 5000;

    RegMap temp;
    RegMap hum;
    RegMap co2;
};

static void load_config(Config& cfg) {
    cfg.http_host = getenv_str("HTTP_HOST", "0.0.0.0");
    cfg.http_port = (int)getenv_long("HTTP_PORT", 8080);

    cfg.modbus_port = getenv_str("MODBUS_PORT", "");
    cfg.slave_id = (int)getenv_long("MODBUS_SLAVE_ID", -1);

    cfg.baud = (int)getenv_long("MODBUS_BAUD", 9600);
    cfg.data_bits = (int)getenv_long("MODBUS_DATA_BITS", 8);
    std::string par = getenv_str("MODBUS_PARITY", "N");
    if (!par.empty()) cfg.parity = (char)std::toupper((unsigned char)par[0]);
    cfg.stop_bits = (int)getenv_long("MODBUS_STOP_BITS", 1);
    cfg.timeout_ms = (int)getenv_long("MODBUS_TIMEOUT_MS", 1000);

    cfg.poll_interval_ms = (int)getenv_long("POLL_INTERVAL_MS", 1000);
    cfg.backoff_initial_ms = (int)getenv_long("BACKOFF_INITIAL_MS", 500);
    cfg.backoff_max_ms = (int)getenv_long("BACKOFF_MAX_MS", 5000);

    // Temperature
    cfg.temp.addr = (int)getenv_long("TEMP_REG_ADDR", -1);
    cfg.temp.count = (int)getenv_long("TEMP_REG_COUNT", 1);
    {
        DataType dt;
        if (parse_datatype(getenv_str("TEMP_TYPE", "uint16"), dt)) cfg.temp.type = dt;
        bool be;
        if (parse_endian(getenv_str("TEMP_ENDIAN", "be"), be)) cfg.temp.big_endian_words = be;
    }
    cfg.temp.scale = getenv_double("TEMP_SCALE", 1.0);

    // Humidity
    cfg.hum.addr = (int)getenv_long("HUM_REG_ADDR", -1);
    cfg.hum.count = (int)getenv_long("HUM_REG_COUNT", 1);
    {
        DataType dt;
        if (parse_datatype(getenv_str("HUM_TYPE", "uint16"), dt)) cfg.hum.type = dt;
        bool be;
        if (parse_endian(getenv_str("HUM_ENDIAN", "be"), be)) cfg.hum.big_endian_words = be;
    }
    cfg.hum.scale = getenv_double("HUM_SCALE", 1.0);

    // CO2
    cfg.co2.addr = (int)getenv_long("CO2_REG_ADDR", -1);
    cfg.co2.count = (int)getenv_long("CO2_REG_COUNT", 1);
    {
        DataType dt;
        if (parse_datatype(getenv_str("CO2_TYPE", "uint16"), dt)) cfg.co2.type = dt;
        bool be;
        if (parse_endian(getenv_str("CO2_ENDIAN", "be"), be)) cfg.co2.big_endian_words = be;
    }
    cfg.co2.scale = getenv_double("CO2_SCALE", 1.0);
}

static speed_t map_baud(int baud) {
    switch (baud) {
        case 0: return B0;
        case 50: return B50;
        case 75: return B75;
        case 110: return B110;
        case 134: return B134;
        case 150: return B150;
        case 200: return B200;
        case 300: return B300;
        case 600: return B600;
        case 1200: return B1200;
        case 1800: return B1800;
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
        default: return (speed_t)0;
    }
}

class SerialPort {
public:
    SerialPort() : fd_(-1) {}
    ~SerialPort() { close_port(); }

    bool open_port(const Config& cfg, std::string& err) {
        if (fd_ != -1) return true;
        int fd = ::open(cfg.modbus_port.c_str(), O_RDWR | O_NOCTTY | O_SYNC);
        if (fd < 0) {
            err = std::string("open failed: ") + strerror(errno);
            return false;
        }
        struct termios tty{};
        if (tcgetattr(fd, &tty) != 0) {
            err = std::string("tcgetattr failed: ") + strerror(errno);
            ::close(fd);
            return false;
        }
        cfmakeraw(&tty);
        // Set baud rate
        speed_t spd = map_baud(cfg.baud);
        if (spd == 0) {
            ::close(fd);
            err = "Unsupported baud rate";
            return false;
        }
        cfsetispeed(&tty, spd);
        cfsetospeed(&tty, spd);

        // c_cflag
        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_cflag &= ~CSIZE;
        switch (cfg.data_bits) {
            case 5: tty.c_cflag |= CS5; break;
            case 6: tty.c_cflag |= CS6; break;
            case 7: tty.c_cflag |= CS7; break;
            case 8: default: tty.c_cflag |= CS8; break;
        }
        // Parity
        if (cfg.parity == 'N' || cfg.parity == 'n') {
            tty.c_cflag &= ~PARENB;
        } else if (cfg.parity == 'E' || cfg.parity == 'e') {
            tty.c_cflag |= PARENB;
            tty.c_cflag &= ~PARODD;
        } else if (cfg.parity == 'O' || cfg.parity == 'o') {
            tty.c_cflag |= PARENB;
            tty.c_cflag |= PARODD;
        } else {
            tty.c_cflag &= ~PARENB;
        }
        // Stop bits
        if (cfg.stop_bits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;

        // Non-canonical mode
        tty.c_iflag = 0;
        tty.c_oflag = 0;
        tty.c_lflag = 0;
        tty.c_cc[VMIN] = 0;
        tty.c_cc[VTIME] = 0;

        if (tcsetattr(fd, TCSANOW, &tty) != 0) {
            err = std::string("tcsetattr failed: ") + strerror(errno);
            ::close(fd);
            return false;
        }
        tcflush(fd, TCIOFLUSH);
        fd_ = fd;
        return true;
    }

    void close_port() {
        if (fd_ != -1) {
            ::close(fd_);
            fd_ = -1;
        }
    }

    bool is_open() const { return fd_ != -1; }

    bool write_all(const uint8_t* data, size_t len, int timeout_ms, std::string& err) {
        if (fd_ < 0) { err = "serial not open"; return false; }
        size_t total = 0;
        auto start = std::chrono::steady_clock::now();
        while (total < len) {
            ssize_t n = ::write(fd_, data + total, len - total);
            if (n < 0) {
                if (errno == EINTR) continue;
                err = std::string("write failed: ") + strerror(errno);
                return false;
            }
            if (n == 0) {
                // unlikely
                std::this_thread::sleep_for(1ms);
            }
            total += (size_t)n;
            auto now = std::chrono::steady_clock::now();
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
            if (elapsed > timeout_ms) {
                err = "write timeout";
                return false;
            }
        }
        if (tcdrain(fd_) != 0) {
            err = std::string("tcdrain failed: ") + strerror(errno);
            return false;
        }
        return true;
    }

    bool read_exact(uint8_t* buf, size_t len, int timeout_ms, std::string& err) {
        if (fd_ < 0) { err = "serial not open"; return false; }
        size_t total = 0;
        auto start = std::chrono::steady_clock::now();
        while (total < len) {
            auto now = std::chrono::steady_clock::now();
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(now - start).count();
            int remaining = timeout_ms - elapsed;
            if (remaining <= 0) {
                err = "read timeout";
                return false;
            }
            fd_set rfds;
            FD_ZERO(&rfds);
            FD_SET(fd_, &rfds);
            struct timeval tv;
            tv.tv_sec = remaining / 1000;
            tv.tv_usec = (remaining % 1000) * 1000;
            int r = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
            if (r < 0) {
                if (errno == EINTR) continue;
                err = std::string("select failed: ") + strerror(errno);
                return false;
            } else if (r == 0) {
                err = "read timeout";
                return false;
            }
            ssize_t n = ::read(fd_, buf + total, len - total);
            if (n < 0) {
                if (errno == EINTR) continue;
                if (errno == EAGAIN || errno == EWOULDBLOCK) continue;
                err = std::string("read failed: ") + strerror(errno);
                return false;
            }
            if (n == 0) continue;
            total += (size_t)n;
        }
        return true;
    }

    void flush_input() {
        if (fd_ >= 0) tcflush(fd_, TCIFLUSH);
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

class ModbusRTUClient {
public:
    ModbusRTUClient(SerialPort& sp, const Config& cfg) : sp_(sp), cfg_(cfg) {}

    bool readHoldingRegisters(uint8_t slave, uint16_t start, uint16_t count, std::vector<uint16_t>& out, std::string& err) {
        std::lock_guard<std::mutex> lk(io_mtx_);
        if (!sp_.is_open()) { err = "serial not open"; return false; }
        uint8_t req[8];
        req[0] = slave;
        req[1] = 0x03;
        req[2] = (uint8_t)(start >> 8);
        req[3] = (uint8_t)(start & 0xFF);
        req[4] = (uint8_t)(count >> 8);
        req[5] = (uint8_t)(count & 0xFF);
        uint16_t crc = modbus_crc16(req, 6);
        req[6] = (uint8_t)(crc & 0xFF);
        req[7] = (uint8_t)(crc >> 8);

        sp_.flush_input();
        if (!sp_.write_all(req, sizeof(req), cfg_.timeout_ms, err)) {
            return false;
        }
        // Response: addr, fc, bytecount, data..., crc_lo, crc_hi
        size_t bytecount = (size_t)count * 2;
        size_t resp_len = 3 + bytecount + 2;
        std::vector<uint8_t> resp(resp_len);
        if (!sp_.read_exact(resp.data(), resp.size(), cfg_.timeout_ms, err)) {
            return false;
        }
        // Validate basic fields
        if (resp[0] != slave) {
            err = "slave id mismatch";
            return false;
        }
        if (resp[1] != 0x03) {
            if (resp[1] & 0x80) {
                err = "exception response";
            } else {
                err = "function code mismatch";
            }
            return false;
        }
        if (resp[2] != bytecount) {
            err = "byte count mismatch";
            return false;
        }
        uint16_t crc_rx = (uint16_t)resp[resp.size() - 2] | ((uint16_t)resp[resp.size() - 1] << 8);
        uint16_t crc_calc = modbus_crc16(resp.data(), resp.size() - 2);
        if (crc_rx != crc_calc) {
            err = "CRC error";
            return false;
        }
        out.clear();
        out.reserve(count);
        for (size_t i = 0; i < count; ++i) {
            uint8_t hi = resp[3 + 2 * i];
            uint8_t lo = resp[3 + 2 * i + 1];
            uint16_t reg = ((uint16_t)hi << 8) | (uint16_t)lo;
            out.push_back(reg);
        }
        return true;
    }

private:
    SerialPort& sp_;
    const Config& cfg_;
    std::mutex io_mtx_;
};

static bool interpret_value(const std::vector<uint16_t>& regs, const RegMap& map, double& out_val) {
    if (map.count == 1) {
        uint16_t r0 = regs[0];
        switch (map.type) {
            case DataType::UINT16: out_val = (double)r0 * map.scale; return true;
            case DataType::INT16: out_val = (double)((int16_t)r0) * map.scale; return true;
            case DataType::UINT32: // invalid with 1 reg
            case DataType::INT32:
            case DataType::FLOAT32:
            default: return false;
        }
    } else if (map.count == 2) {
        uint32_t word0 = regs[0];
        uint32_t word1 = regs[1];
        uint32_t combined = 0;
        if (map.big_endian_words) {
            combined = (word0 << 16) | word1;
        } else {
            combined = (word1 << 16) | word0;
        }
        switch (map.type) {
            case DataType::UINT32: out_val = (double)combined * map.scale; return true;
            case DataType::INT32: out_val = (double)((int32_t)combined) * map.scale; return true;
            case DataType::FLOAT32: {
                float f;
                static_assert(sizeof(float) == 4, "float32 size");
                std::memcpy(&f, &combined, 4);
                out_val = (double)f * map.scale;
                return true;
            }
            case DataType::UINT16:
            case DataType::INT16:
            default: return false;
        }
    }
    return false;
}

struct Readings {
    bool available = false;
    double temperature = 0.0;
    double humidity = 0.0;
    double co2 = 0.0;
    std::chrono::system_clock::time_point ts;
};

class Driver {
public:
    Driver(const Config& cfg) : cfg_(cfg), modbus_(serial_, cfg_) {
        poll_interval_ms_.store(cfg_.poll_interval_ms);
    }

    ~Driver() { stop_polling(); disconnect(); }

    bool connect(std::string& err) {
        std::lock_guard<std::mutex> lk(conn_mtx_);
        if (connected_) return true;
        if (cfg_.modbus_port.empty() || cfg_.slave_id < 1 || cfg_.slave_id > 247) {
            err = "Missing or invalid MODBUS_PORT or MODBUS_SLAVE_ID";
            return false;
        }
        if (cfg_.temp.addr < 0 || cfg_.hum.addr < 0 || cfg_.co2.addr < 0) {
            log_msg("WARN", "Register addresses not fully set; polling will fail until configured");
        }
        std::string serr;
        if (!serial_.open_port(cfg_, serr)) {
            err = serr;
            connected_ = false;
            return false;
        }
        connected_ = true;
        log_msg("INFO", "Connected serial %s baud=%d %d%c%d", cfg_.modbus_port.c_str(), cfg_.baud, cfg_.data_bits, cfg_.parity, cfg_.stop_bits);
        return true;
    }

    void disconnect() {
        std::lock_guard<std::mutex> lk(conn_mtx_);
        if (!connected_) return;
        stop_polling_locked();
        serial_.close_port();
        connected_ = false;
        log_msg("INFO", "Disconnected serial");
    }

    bool start_polling(std::string& err) {
        std::lock_guard<std::mutex> lk(conn_mtx_);
        if (!connected_) { err = "Not connected"; return false; }
        if (polling_) return true;
        polling_ = true;
        poll_thread_ = std::thread(&Driver::poll_loop, this);
        log_msg("INFO", "Polling started");
        return true;
    }

    void stop_polling() {
        std::lock_guard<std::mutex> lk(conn_mtx_);
        stop_polling_locked();
    }

    void stop_polling_locked() {
        if (polling_) {
            polling_ = false;
            if (poll_thread_.joinable()) poll_thread_.join();
            log_msg("INFO", "Polling stopped");
        }
    }

    bool is_connected() const { return connected_; }
    bool is_polling() const { return polling_; }

    void set_poll_interval_ms(int ms) {
        if (ms < 10) ms = 10;
        poll_interval_ms_.store(ms);
        log_msg("INFO", "Poll interval set to %d ms", ms);
    }

    int get_poll_interval_ms() const { return poll_interval_ms_.load(); }

    Readings get_readings_copy() const {
        std::lock_guard<std::mutex> lk(read_mtx_);
        return latest_;
    }

    std::string get_last_error() const {
        std::lock_guard<std::mutex> lk(err_mtx_);
        return last_error_;
    }

    std::string get_status_json() const {
        Readings r = get_readings_copy();
        std::string last_ts = r.available ? timepoint_to_iso(r.ts) : std::string("");
        std::ostringstream oss;
        oss << "{";
        oss << "\"connected\":" << (connected_ ? "true" : "false") << ",";
        oss << "\"port\":\"" << escape_json(cfg_.modbus_port) << "\",";
        oss << "\"slave_id\":" << cfg_.slave_id << ",";
        oss << "\"polling\":" << (polling_ ? "true" : "false") << ",";
        oss << "\"poll_interval_ms\":" << get_poll_interval_ms() << ",";
        if (!last_ts.empty()) {
            oss << "\"last_success_ts\":\"" << escape_json(last_ts) << "\",";
        } else {
            oss << "\"last_success_ts\":null,";
        }
        std::string le = get_last_error();
        if (!le.empty()) {
            oss << "\"last_error\":\"" << escape_json(le) << "\"";
        } else {
            oss << "\"last_error\":null";
        }
        oss << "}";
        return oss.str();
    }

private:
    void poll_loop() {
        int consecutive_errors = 0;
        while (polling_ && !g_shutdown.load()) {
            bool ok_all = false;
            double tval = 0.0, hval = 0.0, cval = 0.0;
            std::string err;

            if (cfg_.temp.addr >= 0) {
                std::vector<uint16_t> regs;
                if (!modbus_.readHoldingRegisters((uint8_t)cfg_.slave_id, (uint16_t)cfg_.temp.addr, (uint16_t)cfg_.temp.count, regs, err)) {
                    set_last_error(err);
                    goto handle_error;
                }
                if (!interpret_value(regs, cfg_.temp, tval)) { set_last_error("interpret temperature failed"); goto handle_error; }
            } else { set_last_error("TEMP_REG_ADDR not set"); goto handle_error; }

            if (cfg_.hum.addr >= 0) {
                std::vector<uint16_t> regs;
                if (!modbus_.readHoldingRegisters((uint8_t)cfg_.slave_id, (uint16_t)cfg_.hum.addr, (uint16_t)cfg_.hum.count, regs, err)) {
                    set_last_error(err);
                    goto handle_error;
                }
                if (!interpret_value(regs, cfg_.hum, hval)) { set_last_error("interpret humidity failed"); goto handle_error; }
            } else { set_last_error("HUM_REG_ADDR not set"); goto handle_error; }

            if (cfg_.co2.addr >= 0) {
                std::vector<uint16_t> regs;
                if (!modbus_.readHoldingRegisters((uint8_t)cfg_.slave_id, (uint16_t)cfg_.co2.addr, (uint16_t)cfg_.co2.count, regs, err)) {
                    set_last_error(err);
                    goto handle_error;
                }
                if (!interpret_value(regs, cfg_.co2, cval)) { set_last_error("interpret CO2 failed"); goto handle_error; }
            } else { set_last_error("CO2_REG_ADDR not set"); goto handle_error; }

            ok_all = true;

            if (ok_all) {
                consecutive_errors = 0;
                update_readings(tval, hval, cval);
                // Sleep for polling interval, but interruptible
                sleep_interruptible(get_poll_interval_ms());
                continue;
            }

        handle_error:
            consecutive_errors++;
            int backoff = cfg_.backoff_initial_ms * (1 << std::min(consecutive_errors - 1, 10));
            if (backoff > cfg_.backoff_max_ms) backoff = cfg_.backoff_max_ms;
            log_msg("WARN", "Polling error (attempt %d): %s; backoff %d ms", consecutive_errors, get_last_error().c_str(), backoff);
            sleep_interruptible(backoff);
        }
    }

    void sleep_interruptible(int ms_total) {
        int step = 50;
        int waited = 0;
        while (waited < ms_total && polling_ && !g_shutdown.load()) {
            std::this_thread::sleep_for(std::chrono::milliseconds(step));
            waited += step;
        }
    }

    void update_readings(double t, double h, double c) {
        std::lock_guard<std::mutex> lk(read_mtx_);
        latest_.available = true;
        latest_.temperature = t;
        latest_.humidity = h;
        latest_.co2 = c;
        latest_.ts = std::chrono::system_clock::now();
        set_last_error("");
        log_msg("INFO", "Updated readings T=%.3f H=%.3f CO2=%.3f", t, h, c);
    }

    void set_last_error(const std::string& e) const {
        std::lock_guard<std::mutex> lk(err_mtx_);
        last_error_ = e;
    }

    static std::string timepoint_to_iso(const std::chrono::system_clock::time_point& tp) {
        std::time_t t = std::chrono::system_clock::to_time_t(tp);
        std::tm tm{};
        localtime_r(&t, &tm);
        char buf[64];
        std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%S%z", &tm);
        return std::string(buf);
    }

    static std::string escape_json(const std::string& s) {
        std::ostringstream o;
        for (auto c : s) {
            switch (c) {
                case '"': o << "\\\""; break;
                case '\\': o << "\\\\"; break;
                case '\b': o << "\\b"; break;
                case '\f': o << "\\f"; break;
                case '\n': o << "\\n"; break;
                case '\r': o << "\\r"; break;
                case '\t': o << "\\t"; break;
                default:
                    if ((unsigned char)c < 0x20) {
                        o << "\\u" << std::hex << std::setw(4) << std::setfill('0') << (int)c << std::dec;
                    } else {
                        o << c;
                    }
            }
        }
        return o.str();
    }

public:
    std::string readings_json() const {
        Readings r = get_readings_copy();
        std::ostringstream oss;
        oss << "{";
        if (r.available) {
            oss << "\"available\":true,";
            oss << std::fixed << std::setprecision(3);
            oss << "\"temperature\":" << r.temperature << ",";
            oss << "\"humidity\":" << r.humidity << ",";
            oss << "\"co2\":" << r.co2 << ",";
            oss << "\"timestamp\":\"" << escape_json(timepoint_to_iso(r.ts)) << "\"";
        } else {
            oss << "\"available\":false,";
            oss << "\"message\":\"No data available yet. Start polling via /poll/start\"";
        }
        oss << "}";
        return oss.str();
    }

    const Config& cfg() const { return cfg_; }

private:
    Config cfg_;
    SerialPort serial_;
    ModbusRTUClient modbus_;

    mutable std::mutex err_mtx_;
    mutable std::string last_error_;

    mutable std::mutex read_mtx_;
    Readings latest_;

    std::mutex conn_mtx_;
    std::atomic<bool> connected_{false};
    std::atomic<bool> polling_{false};
    std::thread poll_thread_;

    std::atomic<int> poll_interval_ms_;
};

class HTTPServer {
public:
    HTTPServer(Driver& driver, const Config& cfg) : driver_(driver), cfg_(cfg) {}

    bool start() {
        if (running_) return true;
        listen_fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        if (listen_fd_ < 0) {
            log_msg("ERROR", "socket failed: %s", strerror(errno));
            return false;
        }
        int opt = 1;
        setsockopt(listen_fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

        struct sockaddr_in addr{};
        addr.sin_family = AF_INET;
        addr.sin_port = htons(cfg_.http_port);
        if (cfg_.http_host == "*" || cfg_.http_host == "0.0.0.0") {
            addr.sin_addr.s_addr = INADDR_ANY;
        } else {
            if (cfg_.http_host == "localhost") {
                inet_pton(AF_INET, "127.0.0.1", &addr.sin_addr);
            } else if (inet_pton(AF_INET, cfg_.http_host.c_str(), &addr.sin_addr) != 1) {
                log_msg("ERROR", "Invalid HTTP_HOST: %s", cfg_.http_host.c_str());
                ::close(listen_fd_);
                listen_fd_ = -1;
                return false;
            }
        }
        if (::bind(listen_fd_, (struct sockaddr*)&addr, sizeof(addr)) != 0) {
            log_msg("ERROR", "bind failed: %s", strerror(errno));
            ::close(listen_fd_);
            listen_fd_ = -1;
            return false;
        }
        if (::listen(listen_fd_, 16) != 0) {
            log_msg("ERROR", "listen failed: %s", strerror(errno));
            ::close(listen_fd_);
            listen_fd_ = -1;
            return false;
        }
        running_ = true;
        accept_thread_ = std::thread(&HTTPServer::accept_loop, this);
        log_msg("INFO", "HTTP server listening on %s:%d", cfg_.http_host.c_str(), cfg_.http_port);
        return true;
    }

    void stop() {
        if (!running_) return;
        running_ = false;
        if (listen_fd_ >= 0) {
            ::shutdown(listen_fd_, SHUT_RDWR);
            ::close(listen_fd_);
            listen_fd_ = -1;
        }
        if (accept_thread_.joinable()) accept_thread_.join();
        log_msg("INFO", "HTTP server stopped");
    }

private:
    void accept_loop() {
        while (running_ && !g_shutdown.load()) {
            struct sockaddr_in cli{};
            socklen_t clilen = sizeof(cli);
            int cfd = ::accept(listen_fd_, (struct sockaddr*)&cli, &clilen);
            if (cfd < 0) {
                if (errno == EINTR) continue;
                if (!running_) break;
                continue;
            }
            std::thread(&HTTPServer::handle_client, this, cfd).detach();
        }
    }

    static bool recv_until(int fd, std::string& buffer, const std::string& delim, int max_bytes = 65536) {
        char tmp[2048];
        while (buffer.find(delim) == std::string::npos) {
            ssize_t n = ::recv(fd, tmp, sizeof(tmp), 0);
            if (n <= 0) return false;
            buffer.append(tmp, tmp + n);
            if ((int)buffer.size() > max_bytes) return false;
        }
        return true;
    }

    static std::string trim(const std::string& s) {
        size_t a = s.find_first_not_of(" \r\n\t");
        if (a == std::string::npos) return "";
        size_t b = s.find_last_not_of(" \r\n\t");
        return s.substr(a, b - a + 1);
    }

    static std::map<std::string, std::string> parse_headers(const std::string& header_block) {
        std::map<std::string, std::string> h;
        std::istringstream iss(header_block);
        std::string line;
        // first line is request-line, skip here
        std::getline(iss, line);
        while (std::getline(iss, line)) {
            if (line == "\r" || line.empty()) break;
            size_t p = line.find(":");
            if (p == std::string::npos) continue;
            std::string k = trim(line.substr(0, p));
            std::string v = trim(line.substr(p + 1));
            if (!v.empty() && v.back() == '\r') v.pop_back();
            // lowercase keys for simplicity
            std::string kl;
            kl.resize(k.size());
            std::transform(k.begin(), k.end(), kl.begin(), [](unsigned char c){ return std::tolower(c); });
            h[kl] = v;
        }
        return h;
    }

    static bool parse_request_line(const std::string& req, std::string& method, std::string& path, std::string& version) {
        std::istringstream iss(req);
        std::string line;
        if (!std::getline(iss, line)) return false;
        if (!line.empty() && line.back() == '\r') line.pop_back();
        std::istringstream l(line);
        if (!(l >> method >> path >> version)) return false;
        return true;
    }

    static std::string http_response(int code, const std::string& status, const std::string& body, const std::string& content_type = "application/json") {
        std::ostringstream oss;
        oss << "HTTP/1.1 " << code << " " << status << "\r\n";
        oss << "Content-Type: " << content_type << "\r\n";
        oss << "Content-Length: " << body.size() << "\r\n";
        oss << "Connection: close\r\n";
        oss << "\r\n";
        oss << body;
        return oss.str();
    }

    static bool parse_json_interval(const std::string& body, int& interval_ms_out) {
        // extremely small parser to find number after "interval_ms"
        auto kpos = body.find("\"interval_ms\"");
        if (kpos == std::string::npos) return false;
        auto colon = body.find(":", kpos);
        if (colon == std::string::npos) return false;
        auto p = colon + 1;
        while (p < body.size() && (body[p] == ' ' || body[p] == '\t')) p++;
        bool neg = false;
        if (p < body.size() && body[p] == '-') { neg = true; p++; }
        long val = 0;
        bool any = false;
        while (p < body.size() && std::isdigit((unsigned char)body[p])) {
            any = true;
            val = val * 10 + (body[p] - '0');
            p++;
        }
        if (!any) return false;
        if (neg) val = -val;
        interval_ms_out = (int)val;
        return true;
    }

    void handle_client(int cfd) {
        std::string buffer;
        if (!recv_until(cfd, buffer, "\r\n\r\n")) {
            ::close(cfd);
            return;
        }
        // Separate head and body start
        size_t header_end = buffer.find("\r\n\r\n");
        std::string headers_block = buffer.substr(0, header_end + 2); // include last \r\n for parser
        std::string method, path, version;
        if (!parse_request_line(headers_block, method, path, version)) {
            std::string resp = http_response(400, "Bad Request", "{\"error\":\"bad request\"}");
            ::send(cfd, resp.data(), resp.size(), 0);
            ::close(cfd);
            return;
        }
        auto headers = parse_headers(headers_block);
        size_t content_length = 0;
        if (headers.count("content-length")) {
            content_length = (size_t)std::strtoul(headers["content-length"].c_str(), nullptr, 10);
        }
        std::string body = buffer.substr(header_end + 4);
        // Receive remaining body if any
        while (body.size() < content_length) {
            char tmp[2048];
            ssize_t n = ::recv(cfd, tmp, sizeof(tmp), 0);
            if (n <= 0) break;
            body.append(tmp, tmp + n);
        }

        std::string response_body;
        int code = 200;
        std::string status = "OK";

        // Routing
        if (method == "POST" && path == "/connect") {
            std::string err;
            if (driver_.is_connected()) {
                response_body = std::string("{") +
                    "\"ok\":true,\"message\":\"Already connected\"," +
                    "\"port\":\"" + Driver::escape_json(driver_.cfg().modbus_port) + "\"," +
                    "\"slave_id\":" + std::to_string(driver_.cfg().slave_id) + "}";
            } else {
                if (driver_.connect(err)) {
                    response_body = std::string("{") +
                        "\"ok\":true,\"message\":\"Connected\"," +
                        "\"port\":\"" + Driver::escape_json(driver_.cfg().modbus_port) + "\"," +
                        "\"slave_id\":" + std::to_string(driver_.cfg().slave_id) + "}";
                } else {
                    code = 500; status = "Internal Server Error";
                    response_body = std::string("{\"ok\":false,\"error\":\"") + Driver::escape_json(err) + "\"}";
                }
            }
        } else if (method == "POST" && path == "/disconnect") {
            driver_.stop_polling();
            driver_.disconnect();
            response_body = "{\"ok\":true,\"message\":\"Disconnected\"}";
        } else if (method == "POST" && path == "/poll/start") {
            if (!driver_.is_connected()) {
                code = 400; status = "Bad Request";
                response_body = "{\"ok\":false,\"error\":\"Not connected. Call /connect first.\"}";
            } else {
                std::string err;
                if (driver_.start_polling(err)) {
                    response_body = "{\"ok\":true,\"message\":\"Polling started\"}";
                } else {
                    code = 500; status = "Internal Server Error";
                    response_body = std::string("{\"ok\":false,\"error\":\"") + Driver::escape_json(err) + "\"}";
                }
            }
        } else if (method == "POST" && path == "/poll/stop") {
            driver_.stop_polling();
            response_body = "{\"ok\":true,\"message\":\"Polling stopped\"}";
        } else if (method == "PUT" && path == "/config/polling") {
            int iv = -1;
            if (!parse_json_interval(body, iv) || iv <= 0) {
                code = 400; status = "Bad Request";
                response_body = "{\"ok\":false,\"error\":\"Invalid or missing interval_ms\"}";
            } else {
                driver_.set_poll_interval_ms(iv);
                response_body = std::string("{\"ok\":true,\"poll_interval_ms\":") + std::to_string(driver_.get_poll_interval_ms()) + "}";
            }
        } else if (method == "GET" && path == "/readings") {
            response_body = driver_.readings_json();
        } else if (method == "GET" && path == "/status") {
            response_body = driver_.get_status_json();
        } else {
            if (path == "/connect" || path == "/disconnect" || path == "/poll/start" || path == "/poll/stop" || path == "/config/polling" || path == "/readings" || path == "/status") {
                code = 405; status = "Method Not Allowed";
                response_body = "{\"error\":\"method not allowed\"}";
            } else {
                code = 404; status = "Not Found";
                response_body = "{\"error\":\"not found\"}";
            }
        }

        std::string resp = http_response(code, status, response_body);
        ::send(cfd, resp.data(), resp.size(), 0);
        ::close(cfd);
    }

    Driver& driver_;
    const Config& cfg_;
    int listen_fd_ = -1;
    std::atomic<bool> running_{false};
    std::thread accept_thread_;
};

static void handle_signal(int) {
    g_shutdown.store(true);
}

int main() {
    signal(SIGINT, handle_signal);
    signal(SIGTERM, handle_signal);

    Config cfg;
    load_config(cfg);

    Driver driver(cfg);
    HTTPServer http(driver, cfg);

    if (!http.start()) {
        log_msg("ERROR", "Failed to start HTTP server");
        return 1;
    }

    // Main loop waiting for shutdown
    while (!g_shutdown.load()) {
        std::this_thread::sleep_for(200ms);
    }

    log_msg("INFO", "Shutting down...");
    http.stop();
    driver.stop_polling();
    driver.disconnect();
    return 0;
}
