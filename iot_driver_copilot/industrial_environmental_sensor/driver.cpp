#include <iostream>
#include <string>
#include <vector>
#include <thread>
#include <mutex>
#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdlib>
#include <cstring>
#include <ctime>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <termios.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/select.h>

// ===================== Utility: Logging =====================
static std::mutex g_logMutex;
static void logf(const std::string &msg) {
    std::lock_guard<std::mutex> lk(g_logMutex);
    auto now = std::chrono::system_clock::now();
    std::time_t tt = std::chrono::system_clock::to_time_t(now);
    char buf[64];
    std::tm tm{};
#if defined(__unix__) || defined(__APPLE__)
    gmtime_r(&tt, &tm);
#else
    tm = *std::gmtime(&tt);
#endif
    std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    std::cerr << "[" << buf << "] " << msg << std::endl;
}

// ===================== Environment Helpers =====================
static std::string getEnvStr(const char *name, const std::string &def) {
    const char *v = std::getenv(name);
    return v ? std::string(v) : def;
}
static int getEnvInt(const char *name, int def) {
    const char *v = std::getenv(name);
    if (!v) return def;
    try {
        return std::stoi(v);
    } catch (...) {
        return def;
    }
}
static bool getEnvBool(const char *name, bool def) {
    const char *v = std::getenv(name);
    if (!v) return def;
    std::string s(v);
    for (auto &c : s) c = std::tolower(c);
    if (s == "1" || s == "true" || s == "yes" || s == "on") return true;
    if (s == "0" || s == "false" || s == "no" || s == "off") return false;
    return def;
}

// ===================== Time Formatting =====================
static std::string formatTime(std::chrono::system_clock::time_point tp) {
    std::time_t tt = std::chrono::system_clock::to_time_t(tp);
    char buf[64];
    std::tm tm{};
#if defined(__unix__) || defined(__APPLE__)
    gmtime_r(&tt, &tm);
#else
    tm = *std::gmtime(&tt);
#endif
    std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buf);
}

// ===================== CRC16 (Modbus) =====================
static uint16_t modbus_crc16(const uint8_t *data, size_t len) {
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

// ===================== Serial Port =====================
class SerialPort {
public:
    SerialPort() : fd_(-1) {}
    ~SerialPort() { close(); }

    bool open(const std::string &port, int baud, int dataBits, char parity, int stopBits) {
        close();
        fd_ = ::open(port.c_str(), O_RDWR | O_NOCTTY | O_SYNC);
        if (fd_ < 0) {
            logf("Serial open failed: " + port + ", errno=" + std::to_string(errno));
            return false;
        }
        struct termios tty{};
        if (tcgetattr(fd_, &tty) != 0) {
            logf("tcgetattr failed, errno=" + std::to_string(errno));
            close();
            return false;
        }
        cfmakeraw(&tty);
        // Baud rate
        speed_t speed = mapBaud(baud);
        cfsetispeed(&tty, speed);
        cfsetospeed(&tty, speed);
        // Data bits
        tty.c_cflag &= ~CSIZE;
        if (dataBits == 7) tty.c_cflag |= CS7; else tty.c_cflag |= CS8;
        // Parity
        parity = std::toupper(parity);
        if (parity == 'N') {
            tty.c_cflag &= ~PARENB;
        } else if (parity == 'E') {
            tty.c_cflag |= PARENB;
            tty.c_cflag &= ~PARODD;
        } else if (parity == 'O') {
            tty.c_cflag |= PARENB;
            tty.c_cflag |= PARODD;
        } else {
            tty.c_cflag &= ~PARENB; // default none
        }
        // Stop bits
        if (stopBits == 2) tty.c_cflag |= CSTOPB; else tty.c_cflag &= ~CSTOPB;
        tty.c_cflag |= (CLOCAL | CREAD);
        tty.c_cc[VMIN] = 0;
        tty.c_cc[VTIME] = 0; // we use select() for timeouts
        if (tcsetattr(fd_, TCSANOW, &tty) != 0) {
            logf("tcsetattr failed, errno=" + std::to_string(errno));
            close();
            return false;
        }
        tcflush(fd_, TCIOFLUSH);
        return true;
    }

    void close() {
        if (fd_ >= 0) {
            ::close(fd_);
            fd_ = -1;
        }
    }

    bool writeAll(const uint8_t *data, size_t len, int timeoutMs) {
        size_t written = 0;
        auto start = std::chrono::steady_clock::now();
        while (written < len) {
            // Optional timeout enforcement
            if (timeoutMs > 0) {
                auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - start).count();
                if (elapsed >= timeoutMs) return false;
            }
            ssize_t n = ::write(fd_, data + written, len - written);
            if (n < 0) {
                if (errno == EAGAIN || errno == EINTR) continue;
                return false;
            }
            written += (size_t)n;
        }
        return true;
    }

    bool readSome(uint8_t *buf, size_t maxLen, int timeoutMs, ssize_t &outRead) {
        outRead = 0;
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(fd_, &rfds);
        struct timeval tv{};
        tv.tv_sec = timeoutMs / 1000;
        tv.tv_usec = (timeoutMs % 1000) * 1000;
        int rv = select(fd_ + 1, &rfds, nullptr, nullptr, &tv);
        if (rv < 0) {
            if (errno == EINTR) return false;
            return false;
        } else if (rv == 0) {
            // timeout
            outRead = 0;
            return true;
        }
        ssize_t n = ::read(fd_, buf, maxLen);
        if (n < 0) {
            if (errno == EAGAIN || errno == EINTR) {
                outRead = 0;
                return true;
            }
            return false;
        }
        outRead = n;
        return true;
    }

    int fd() const { return fd_; }

private:
    int fd_;

    static speed_t mapBaud(int baud) {
        switch (baud) {
            case 1200: return B1200;
            case 2400: return B2400;
            case 4800: return B4800;
            case 9600: return B9600;
            case 19200: return B19200;
            case 38400: return B38400;
            case 57600: return B57600;
            case 115200: return B115200;
            default: return B9600;
        }
    }
};

// ===================== Modbus RTU Client =====================
class ModbusRTU {
public:
    ModbusRTU() : connected_(false) {}

    bool connect(const std::string &port, int baud, int dataBits, char parity, int stopBits) {
        if (serial_.open(port, baud, dataBits, parity, stopBits)) {
            connected_ = true;
            return true;
        }
        connected_ = false;
        return false;
    }

    void disconnect() {
        serial_.close();
        connected_ = false;
    }

    bool isConnected() const { return connected_; }

    // Read Holding Registers (FC=0x03)
    // Returns true on success and fills outRegs
    bool readHoldingRegisters(uint8_t slaveId, uint16_t startAddr, uint16_t count,
                              std::vector<uint16_t> &outRegs,
                              int readTimeoutMs,
                              std::string &errType, std::string &errMsg) {
        outRegs.clear();
        if (!connected_) { errType = "invalid_response"; errMsg = "Not connected"; return false; }
        uint8_t req[8];
        req[0] = slaveId;
        req[1] = 0x03;
        req[2] = (uint8_t)((startAddr >> 8) & 0xFF);
        req[3] = (uint8_t)(startAddr & 0xFF);
        req[4] = (uint8_t)((count >> 8) & 0xFF);
        req[5] = (uint8_t)(count & 0xFF);
        uint16_t crc = modbus_crc16(req, 6);
        req[6] = (uint8_t)(crc & 0xFF);
        req[7] = (uint8_t)((crc >> 8) & 0xFF);
        if (!serial_.writeAll(req, sizeof(req), readTimeoutMs)) {
            errType = "timeout";
            errMsg = "Write timeout";
            return false;
        }
        std::vector<uint8_t> resp;
        resp.reserve(5 + 2 * count);
        auto start = std::chrono::steady_clock::now();
        // Step 1: read at least 3 bytes (addr, func, byteCount) or 2 bytes for exception
        while ((int)resp.size() < 3) {
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(
                std::chrono::steady_clock::now() - start).count();
            int remain = readTimeoutMs - elapsed;
            if (remain <= 0) {
                errType = "timeout"; errMsg = "Response header timeout"; return false;
            }
            uint8_t buf[256];
            ssize_t n = 0;
            if (!serial_.readSome(buf, sizeof(buf), remain, n)) {
                errType = "invalid_response"; errMsg = "Read error"; return false;
            }
            if (n == 0) continue; // still waiting
            resp.insert(resp.end(), buf, buf + n);
        }
        // Check function code for exception
        if (resp.size() >= 2 && (resp[1] & 0x80)) {
            // Exception response: addr, func|0x80, excCode, crcLo, crcHi
            while (resp.size() < 5) {
                int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(
                    std::chrono::steady_clock::now() - start).count();
                int remain = readTimeoutMs - elapsed;
                if (remain <= 0) {
                    errType = "timeout"; errMsg = "Exception response incomplete"; return false;
                }
                uint8_t buf[256]; ssize_t n = 0;
                if (!serial_.readSome(buf, sizeof(buf), remain, n)) {
                    errType = "invalid_response"; errMsg = "Read error"; return false;
                }
                if (n == 0) continue;
                resp.insert(resp.end(), buf, buf + n);
            }
            // CRC check
            uint16_t crcCalc = modbus_crc16(resp.data(), resp.size() - 2);
            uint16_t crcRecv = (uint16_t)resp[resp.size()-2] | ((uint16_t)resp[resp.size()-1] << 8);
            if (crcCalc != crcRecv) { errType = "crc_mismatch"; errMsg = "CRC mismatch on exception response"; return false; }
            errType = "invalid_response";
            errMsg = "Modbus exception code: " + std::to_string(resp[2]);
            return false;
        }
        // Normal response: addr, func=0x03, byteCount=2*count, data..., crcLo, crcHi
        uint8_t byteCount = resp[2];
        size_t totalExpected = 3 + (size_t)byteCount + 2;
        while (resp.size() < totalExpected) {
            int elapsed = (int)std::chrono::duration_cast<std::chrono::milliseconds>(
                std::chrono::steady_clock::now() - start).count();
            int remain = readTimeoutMs - elapsed;
            if (remain <= 0) { errType = "timeout"; errMsg = "Response body timeout"; return false; }
            uint8_t buf[256]; ssize_t n = 0;
            if (!serial_.readSome(buf, sizeof(buf), remain, n)) {
                errType = "invalid_response"; errMsg = "Read error"; return false;
            }
            if (n == 0) continue;
            resp.insert(resp.end(), buf, buf + n);
        }
        // Validate address and function
        if (resp[0] != slaveId || resp[1] != 0x03) {
            errType = "invalid_response"; errMsg = "Unexpected addr/func"; return false;
        }
        if (byteCount != count * 2) {
            errType = "invalid_response"; errMsg = "ByteCount mismatch"; return false;
        }
        // CRC validation
        uint16_t crcCalc = modbus_crc16(resp.data(), resp.size() - 2);
        uint16_t crcRecv = (uint16_t)resp[resp.size()-2] | ((uint16_t)resp[resp.size()-1] << 8);
        if (crcCalc != crcRecv) { errType = "crc_mismatch"; errMsg = "CRC mismatch"; return false; }
        // Parse registers
        outRegs.resize(count);
        for (uint16_t i = 0; i < count; ++i) {
            uint8_t hi = resp[3 + 2*i];
            uint8_t lo = resp[3 + 2*i + 1];
            outRegs[i] = (uint16_t)(((uint16_t)hi << 8) | lo);
        }
        return true;
    }

private:
    SerialPort serial_;
    bool connected_;
};

// ===================== Driver State =====================
struct DriverConfig {
    std::string httpHost;
    int httpPort;

    std::string serialPort;
    int baudRate;
    int dataBits;
    char parity;
    int stopBits;

    uint8_t deviceId;
    int readTimeoutMs;

    int pollIntervalMs; // if 0, use sample interval

    int retryBaseMs;
    int retryMaxMs;
    int maxRetries; // 0 => infinite
    bool autoReconnect;

    uint16_t tempReg;
    uint16_t humReg;
    uint16_t sampleIntervalReg;

    int tempScaleNum;
    int tempScaleDen;
    int humScaleNum;
    int humScaleDen;
};

struct SampleData {
    double temperature = 0.0;
    double humidity = 0.0;
    std::chrono::system_clock::time_point ts = std::chrono::system_clock::time_point{};
};

struct DriverStatus {
    std::string connectionState = "disconnected"; // connected|disconnected|reconnecting
    std::string port;
    int deviceId = 0;
    std::chrono::system_clock::time_point lastSeen = std::chrono::system_clock::time_point{};
    bool hasLastError = false;
    std::string lastErrorType; // timeout|crc_mismatch|invalid_response
    std::string lastErrorMsg;
    int timeouts = 0;
    int crcMismatches = 0;
    int invalidResponses = 0;
    int retriesInProgress = 0;
    bool autoReconnect = true;
    int sampleIntervalS = 1; // from device register
};

static std::mutex g_stateMutex;
static SampleData g_sample;
static DriverStatus g_status;
static std::atomic<bool> g_stop{false};

// ===================== Collector Thread =====================
static void collectorLoop(DriverConfig cfg) {
    ModbusRTU modbus;
    int retries = 0;
    int backoffMs = cfg.retryBaseMs;

    auto setStatusConn = [&](const std::string &state){
        std::lock_guard<std::mutex> lk(g_stateMutex);
        g_status.connectionState = state;
    };

    auto setLastError = [&](const std::string &type, const std::string &msg){
        std::lock_guard<std::mutex> lk(g_stateMutex);
        g_status.hasLastError = true;
        g_status.lastErrorType = type;
        g_status.lastErrorMsg = msg;
        if (type == "timeout") g_status.timeouts++;
        else if (type == "crc_mismatch") g_status.crcMismatches++;
        else if (type == "invalid_response") g_status.invalidResponses++;
    };

    auto clearLastError = [&](){
        std::lock_guard<std::mutex> lk(g_stateMutex);
        g_status.hasLastError = false;
        g_status.lastErrorType.clear();
        g_status.lastErrorMsg.clear();
    };

    while (!g_stop.load()) {
        if (!modbus.isConnected()) {
            if (!cfg.autoReconnect) {
                setStatusConn("disconnected");
                std::this_thread::sleep_for(std::chrono::milliseconds(500));
                continue;
            }
            setStatusConn("reconnecting");
            {
                std::lock_guard<std::mutex> lk(g_stateMutex);
                g_status.retriesInProgress = retries;
            }
            logf("Attempting to connect serial: " + cfg.serialPort);
            if (modbus.connect(cfg.serialPort, cfg.baudRate, cfg.dataBits, cfg.parity, cfg.stopBits)) {
                std::lock_guard<std::mutex> lk(g_stateMutex);
                g_status.port = cfg.serialPort;
                g_status.deviceId = cfg.deviceId;
                setStatusConn("connected");
                logf("Serial connected");
                retries = 0;
                backoffMs = cfg.retryBaseMs;
                clearLastError();
            } else {
                setLastError("invalid_response", "Serial open failed");
                retries++;
                if (cfg.maxRetries > 0 && retries >= cfg.maxRetries) {
                    logf("Max retries reached, staying disconnected");
                    setStatusConn("disconnected");
                    std::this_thread::sleep_for(std::chrono::milliseconds(cfg.retryMaxMs));
                } else {
                    int waitMs = backoffMs;
                    backoffMs = std::min(backoffMs * 2, cfg.retryMaxMs);
                    logf("Reconnect backoff " + std::to_string(waitMs) + "ms");
                    std::this_thread::sleep_for(std::chrono::milliseconds(waitMs));
                }
                continue;
            }
        }

        // Connected: read sample interval, temperature, humidity
        std::string errType, errMsg;
        std::vector<uint16_t> regs;
        bool okSI = modbus.readHoldingRegisters(cfg.deviceId, cfg.sampleIntervalReg, 1, regs, cfg.readTimeoutMs, errType, errMsg);
        if (!okSI) {
            setLastError(errType, errMsg);
            logf("Read sample interval failed: " + errType + ": " + errMsg);
            modbus.disconnect();
            setStatusConn("reconnecting");
            continue;
        } else {
            int si = (int)regs[0];
            if (si < 1) si = 1; if (si > 60) si = 60;
            {
                std::lock_guard<std::mutex> lk(g_stateMutex);
                g_status.sampleIntervalS = si;
            }
        }
        // Read temperature
        bool okT = modbus.readHoldingRegisters(cfg.deviceId, cfg.tempReg, 1, regs, cfg.readTimeoutMs, errType, errMsg);
        if (!okT) {
            setLastError(errType, errMsg);
            logf("Read temperature failed: " + errType + ": " + errMsg);
            modbus.disconnect();
            setStatusConn("reconnecting");
            continue;
        }
        int rawT = (int)regs[0];
        double temp = (double)rawT * (double)cfg.tempScaleNum / (double)cfg.tempScaleDen;
        // Read humidity
        bool okH = modbus.readHoldingRegisters(cfg.deviceId, cfg.humReg, 1, regs, cfg.readTimeoutMs, errType, errMsg);
        if (!okH) {
            setLastError(errType, errMsg);
            logf("Read humidity failed: " + errType + ": " + errMsg);
            modbus.disconnect();
            setStatusConn("reconnecting");
            continue;
        }
        int rawH = (int)regs[0];
        double hum = (double)rawH * (double)cfg.humScaleNum / (double)cfg.humScaleDen;

        // Update sample and status
        {
            std::lock_guard<std::mutex> lk(g_stateMutex);
            g_sample.temperature = temp;
            g_sample.humidity = hum;
            g_sample.ts = std::chrono::system_clock::now();
            g_status.lastSeen = g_sample.ts;
            g_status.connectionState = "connected";
            g_status.retriesInProgress = 0;
            g_status.hasLastError = false;
            g_status.lastErrorType.clear();
            g_status.lastErrorMsg.clear();
        }
        logf("Sample updated: T=" + std::to_string(temp) + ", H=" + std::to_string(hum));

        // Sleep until next poll
        int sleepMs = cfg.pollIntervalMs > 0 ? cfg.pollIntervalMs : (g_status.sampleIntervalS * 1000);
        if (sleepMs < 100) sleepMs = 100; // prevent busy loop
        for (int slept = 0; slept < sleepMs && !g_stop.load(); ) {
            int step = std::min(200, sleepMs - slept);
            std::this_thread::sleep_for(std::chrono::milliseconds(step));
            slept += step;
        }
    }
}

// ===================== HTTP Server =====================
static int createListenSocket(const std::string &host, int port) {
    int s = ::socket(AF_INET, SOCK_STREAM, 0);
    if (s < 0) return -1;
    int opt = 1;
    setsockopt(s, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
    struct sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(port);
    if (host.empty()) {
        addr.sin_addr.s_addr = INADDR_ANY;
    } else {
        if (::inet_pton(AF_INET, host.c_str(), &addr.sin_addr) != 1) {
            ::close(s); return -1;
        }
    }
    if (::bind(s, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
        ::close(s); return -1;
    }
    if (::listen(s, 16) < 0) { ::close(s); return -1; }
    return s;
}

static void sendHttpResponse(int cfd, int code, const std::string &status, const std::string &body, const std::string &contentType = "application/json") {
    std::string hdr = "HTTP/1.1 " + std::to_string(code) + " " + status + "\r\n";
    hdr += "Content-Type: " + contentType + "\r\n";
    hdr += "Connection: close\r\n";
    hdr += "Content-Length: " + std::to_string(body.size()) + "\r\n\r\n";
    ::send(cfd, hdr.data(), hdr.size(), 0);
    ::send(cfd, body.data(), body.size(), 0);
}

static std::string buildStatusJson() {
    std::lock_guard<std::mutex> lk(g_stateMutex);
    std::string lastSeenStr = g_status.lastSeen.time_since_epoch().count() == 0 ? "" : formatTime(g_status.lastSeen);
    std::string json = "{";
    json += "\"connection_state\":\"" + g_status.connectionState + "\",";
    json += "\"port\":\"" + g_status.port + "\",";
    json += "\"device_id\":" + std::to_string(g_status.deviceId) + ",";
    json += "\"last_seen\":\"" + lastSeenStr + "\",";
    if (g_status.hasLastError) {
        json += "\"last_error\":{\"type\":\"" + g_status.lastErrorType + "\",\"message\":\"";
        // Escape quotes in message (basic)
        std::string msg = g_status.lastErrorMsg;
        for (char &c : msg) { if (c == '"') c = '\''; }
        json += msg + "\"},";
    } else {
        json += "\"last_error\":null,";
    }
    json += "\"error_counters\":{\"timeouts\":" + std::to_string(g_status.timeouts) + ",";
    json += "\"crc_mismatches\":" + std::to_string(g_status.crcMismatches) + ",";
    json += "\"invalid_responses\":" + std::to_string(g_status.invalidResponses) + "},";
    json += "\"retries_in_progress\":" + std::to_string(g_status.retriesInProgress) + ",";
    json += "\"auto_reconnect\":\"" + std::string(g_status.autoReconnect ? "enabled" : "disabled") + "\",";
    json += "\"sample_interval_s\":" + std::to_string(g_status.sampleIntervalS);
    json += "}";
    return json;
}

static void httpServerLoop(const std::string &host, int port) {
    int s = createListenSocket(host, port);
    if (s < 0) {
        logf("Failed to start HTTP server on " + host + ":" + std::to_string(port));
        return;
    }
    logf("HTTP server listening on " + host + ":" + std::to_string(port));
    while (!g_stop.load()) {
        struct sockaddr_in cli{};
        socklen_t clilen = sizeof(cli);
        int cfd = ::accept(s, (struct sockaddr*)&cli, &clilen);
        if (cfd < 0) {
            if (errno == EINTR) continue;
            if (g_stop.load()) break;
            // brief sleep to avoid busy loop on errors
            std::this_thread::sleep_for(std::chrono::milliseconds(50));
            continue;
        }
        // Read request (simple)
        char buf[1024];
        ssize_t n = ::recv(cfd, buf, sizeof(buf)-1, 0);
        if (n <= 0) { ::close(cfd); continue; }
        buf[n] = '\0';
        std::string req(buf);
        // Parse first line
        size_t pos = req.find("\r\n");
        std::string line = pos != std::string::npos ? req.substr(0, pos) : req;
        // Expected: GET /status HTTP/1.1
        bool ok = false;
        if (line.rfind("GET ", 0) == 0) {
            size_t sp = line.find(' ');
            if (sp != std::string::npos) {
                size_t sp2 = line.find(' ', sp+1);
                std::string path = sp2 != std::string::npos ? line.substr(sp+1, sp2-(sp+1)) : "/";
                if (path == "/status") {
                    ok = true;
                }
            }
        }
        if (!ok) {
            std::string body = "{\"error\":\"not_found\"}";
            sendHttpResponse(cfd, 404, "Not Found", body);
            ::close(cfd);
            continue;
        }
        std::string body = buildStatusJson();
        sendHttpResponse(cfd, 200, "OK", body);
        ::close(cfd);
    }
    ::close(s);
}

// ===================== Signal Handler =====================
static void sigHandler(int) {
    g_stop.store(true);
}

// ===================== Main =====================
int main() {
    // Load configuration from environment
    DriverConfig cfg;
    cfg.httpHost = getEnvStr("HTTP_HOST", "0.0.0.0");
    cfg.httpPort = getEnvInt("HTTP_PORT", 8080);

    cfg.serialPort = getEnvStr("SERIAL_PORT", "/dev/ttyUSB0");
    cfg.baudRate = getEnvInt("BAUD_RATE", 9600);
    cfg.dataBits = getEnvInt("DATA_BITS", 8);
    {
        std::string p = getEnvStr("PARITY", "N");
        cfg.parity = p.empty() ? 'N' : p[0];
    }
    cfg.stopBits = getEnvInt("STOP_BITS", 1);

    cfg.deviceId = (uint8_t)getEnvInt("MODBUS_DEVICE_ID", 1);
    cfg.readTimeoutMs = getEnvInt("READ_TIMEOUT_MS", 500);

    cfg.pollIntervalMs = getEnvInt("POLL_INTERVAL_MS", 0); // 0 means follow device sample interval

    cfg.retryBaseMs = getEnvInt("RETRY_BASE_MS", 500);
    cfg.retryMaxMs = getEnvInt("RETRY_MAX_MS", 5000);
    cfg.maxRetries = getEnvInt("MAX_RETRIES", 0);
    cfg.autoReconnect = getEnvBool("AUTO_RECONNECT", true);

    cfg.tempReg = (uint16_t)getEnvInt("TEMP_REG", 0x0000);
    cfg.humReg = (uint16_t)getEnvInt("HUM_REG", 0x0001);
    cfg.sampleIntervalReg = (uint16_t)getEnvInt("SAMPLE_INTERVAL_REG", 0x0002);

    cfg.tempScaleNum = getEnvInt("TEMP_SCALE_NUM", 1);
    cfg.tempScaleDen = getEnvInt("TEMP_SCALE_DEN", 100);
    cfg.humScaleNum = getEnvInt("HUM_SCALE_NUM", 1);
    cfg.humScaleDen = getEnvInt("HUM_SCALE_DEN", 100);

    {
        std::lock_guard<std::mutex> lk(g_stateMutex);
        g_status.port = cfg.serialPort;
        g_status.deviceId = cfg.deviceId;
        g_status.autoReconnect = cfg.autoReconnect;
        g_status.sampleIntervalS = 1;
    }

    // Setup signal handlers for graceful shutdown
    std::signal(SIGINT, sigHandler);
    std::signal(SIGTERM, sigHandler);

    // Start threads
    std::thread collector([&](){ collectorLoop(cfg); });
    std::thread httpServer([&](){ httpServerLoop(cfg.httpHost, cfg.httpPort); });

    // Wait for stop
    while (!g_stop.load()) {
        std::this_thread::sleep_for(std::chrono::milliseconds(200));
    }

    logf("Shutting down...");
    if (collector.joinable()) collector.join();
    if (httpServer.joinable()) httpServer.join();
    logf("Shutdown complete");
    return 0;
}
