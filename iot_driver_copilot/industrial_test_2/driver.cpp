#include <iostream>
#include <csignal>
#include <atomic>
#include <thread>
#include <mutex>
#include <chrono>
#include <string>
#include <sstream>
#include <iomanip>
#include <vector>
#include <cstring>

#include "config.h"
#include "serial_port.h"
#include "modbus_rtu.h"
#include "http_server.h"

using namespace std::chrono;

static std::atomic<bool> g_should_stop{false};

static void signal_handler(int) {
    g_should_stop.store(true);
}

static std::string now_iso8601() {
    auto now = system_clock::now();
    std::time_t t_c = system_clock::to_time_t(now);
    std::tm tm{};
    gmtime_r(&t_c, &tm);
    auto ms = duration_cast<milliseconds>(now.time_since_epoch()) % 1000;

    std::ostringstream oss;
    oss << std::put_time(&tm, "%Y-%m-%dT%H:%M:%S");
    oss << "." << std::setw(3) << std::setfill('0') << ms.count() << "Z";
    return oss.str();
}

static void log_line(const std::string &level, const std::string &msg) {
    std::cerr << now_iso8601() << " [" << level << "] " << msg << std::endl;
}

static void log_info(const std::string &msg) { log_line("INFO", msg); }
static void log_warn(const std::string &msg) { log_line("WARN", msg); }
static void log_error(const std::string &msg) { log_line("ERROR", msg); }

struct SensorValue {
    double value{0};
    bool has{false};
    std::string ts;
};

struct DriverState {
    // Config snapshot
    Config cfg;

    // Status
    std::mutex status_mtx;
    bool serial_connected{false};
    bool polling_active{false};
    std::string last_poll_ts;
    std::string last_error;

    // Data
    std::mutex data_mtx;
    SensorValue temperature;
    SensorValue humidity;
    SensorValue co2;
};

class Poller {
public:
    Poller(DriverState* state)
    : state_(state), thread_(), running_(false) {}

    ~Poller() {
        stop();
    }

    void start() {
        running_.store(true);
        thread_ = std::thread(&Poller::run, this);
    }

    void stop() {
        running_.store(false);
        if (thread_.joinable()) thread_.join();
    }

private:
    DriverState* state_;
    std::thread thread_;
    std::atomic<bool> running_;

    void set_status(bool connected, const std::string& last_poll_ts, const std::string& last_error) {
        std::lock_guard<std::mutex> lk(state_->status_mtx);
        state_->serial_connected = connected;
        state_->last_poll_ts = last_poll_ts;
        state_->last_error = last_error;
    }

    void set_polling_active(bool active) {
        std::lock_guard<std::mutex> lk(state_->status_mtx);
        state_->polling_active = active;
    }

    void update_value(SensorValue &target, double value) {
        target.value = value;
        target.has = true;
        target.ts = now_iso8601();
    }

    void run() {
        set_polling_active(true);
        const auto initial_backoff = milliseconds(state_->cfg.backoff_initial_ms);
        const auto max_backoff = milliseconds(state_->cfg.backoff_max_ms);
        milliseconds backoff = initial_backoff;

        SerialPort serial;
        ModbusRTU modbus(&serial, state_->cfg.modbus_timeout_ms);

        while (running_.load() && !g_should_stop.load()) {
            if (!serial.isOpen()) {
                if (!serial.openPort(state_->cfg.serial_port.c_str(), 9600, 'N', 8, 1)) {
                    set_status(false, state_->last_poll_ts, "open serial failed: " + serial.lastError());
                    log_error("Failed to open serial port " + state_->cfg.serial_port + ": " + serial.lastError());
                    sleep_for_backoff(backoff);
                    backoff = std::min(backoff * 2, max_backoff);
                    continue;
                } else {
                    log_info("Opened serial port " + state_->cfg.serial_port + " at 9600 8N1");
                }
            }

            bool all_ok = true;
            std::string err;
            std::vector<uint16_t> regs;

            // temperature
            regs.clear();
            if (modbus.readHoldingRegisters((uint8_t)state_->cfg.modbus_slave_id, (uint16_t)state_->cfg.reg_temperature, 1, regs)) {
                double v = (double)regs[0] * state_->cfg.scale_temperature;
                {
                    std::lock_guard<std::mutex> lk(state_->data_mtx);
                    update_value(state_->temperature, v);
                }
            } else {
                err = "read temperature failed: " + modbus.lastError();
                all_ok = false;
            }

            // humidity
            regs.clear();
            if (all_ok && modbus.readHoldingRegisters((uint8_t)state_->cfg.modbus_slave_id, (uint16_t)state_->cfg.reg_humidity, 1, regs)) {
                double v = (double)regs[0] * state_->cfg.scale_humidity;
                {
                    std::lock_guard<std::mutex> lk(state_->data_mtx);
                    update_value(state_->humidity, v);
                }
            } else if (all_ok) {
                err = "read humidity failed: " + modbus.lastError();
                all_ok = false;
            }

            // co2
            regs.clear();
            if (all_ok && modbus.readHoldingRegisters((uint8_t)state_->cfg.modbus_slave_id, (uint16_t)state_->cfg.reg_co2, 1, regs)) {
                double v = (double)regs[0] * state_->cfg.scale_co2;
                {
                    std::lock_guard<std::mutex> lk(state_->data_mtx);
                    update_value(state_->co2, v);
                }
            } else if (all_ok) {
                err = "read CO2 failed: " + modbus.lastError();
                all_ok = false;
            }

            set_status(serial.isOpen(), now_iso8601(), all_ok ? "" : err);

            if (!all_ok) {
                log_warn(err);
                serial.closePort();
                sleep_for_backoff(backoff);
                backoff = std::min(backoff * 2, max_backoff);
                continue;
            }

            backoff = initial_backoff; // reset backoff after success

            // sleep poll interval or until stop
            auto until = steady_clock::now() + milliseconds(state_->cfg.poll_interval_ms);
            while (running_.load() && !g_should_stop.load() && steady_clock::now() < until) {
                std::this_thread::sleep_for(std::chrono::milliseconds(50));
            }
        }

        // Cleanup
        set_polling_active(false);
    }

    void sleep_for_backoff(std::chrono::milliseconds d) {
        auto until = steady_clock::now() + d;
        while (running_.load() && !g_should_stop.load() && steady_clock::now() < until) {
            std::this_thread::sleep_for(std::chrono::milliseconds(50));
        }
    }
};

// JSON helpers
static std::string json_escape(const std::string& s) {
    std::ostringstream oss;
    for (char c : s) {
        switch (c) {
            case '\\': oss << "\\\\"; break;
            case '"': oss << "\\\""; break;
            case '\n': oss << "\\n"; break;
            case '\r': oss << "\\r"; break;
            case '\t': oss << "\\t"; break;
            default:
                if (static_cast<unsigned char>(c) < 0x20) {
                    oss << "\\u" << std::hex << std::setw(4) << std::setfill('0') << (int)(unsigned char)c << std::dec;
                } else {
                    oss << c;
                }
        }
    }
    return oss.str();
}

static std::string build_status_json(DriverState* st) {
    std::lock_guard<std::mutex> lk(st->status_mtx);
    std::ostringstream oss;
    oss << "{";
    oss << "\"connected\":" << (st->serial_connected ? "true":"false") << ",";
    oss << "\"port\":\"" << json_escape(st->cfg.serial_port) << "\",";
    oss << "\"slave_id\":" << st->cfg.modbus_slave_id << ",";
    oss << "\"polling\":" << (st->polling_active ? "true":"false") << ",";
    oss << "\"interval_ms\":" << st->cfg.poll_interval_ms << ",";
    oss << "\"last_poll\":\"" << json_escape(st->last_poll_ts) << "\",";
    oss << "\"last_error\":\"" << json_escape(st->last_error) << "\"";
    oss << "}";
    return oss.str();
}

static std::string build_readings_json(DriverState* st) {
    std::lock_guard<std::mutex> lk(st->data_mtx);
    std::ostringstream oss;
    oss << "{";
    // temperature
    oss << "\"temperature\":{";
    if (st->temperature.has) {
        oss << "\"value\":" << st->temperature.value << ",";
        oss << "\"ts\":\"" << json_escape(st->temperature.ts) << "\"";
    } else {
        oss << "\"value\":null,\"ts\":null";
    }
    oss << "},";

    // humidity
    oss << "\"humidity\":{";
    if (st->humidity.has) {
        oss << "\"value\":" << st->humidity.value << ",";
        oss << "\"ts\":\"" << json_escape(st->humidity.ts) << "\"";
    } else {
        oss << "\"value\":null,\"ts\":null";
    }
    oss << "},";

    // co2
    oss << "\"co2\":{";
    if (st->co2.has) {
        oss << "\"value\":" << st->co2.value << ",";
        oss << "\"ts\":\"" << json_escape(st->co2.ts) << "\"";
    } else {
        oss << "\"value\":null,\"ts\":null";
    }
    oss << "}";

    oss << "}";
    return oss.str();
}

class DriverHttpHandler : public IHttpRequestHandler {
public:
    explicit DriverHttpHandler(DriverState* st) : st_(st) {}

    void handleRequest(const HttpRequest& req, HttpResponse& resp) override {
        if (req.method != "GET") {
            resp.status = 405;
            resp.status_text = "Method Not Allowed";
            resp.headers["Content-Type"] = "application/json";
            resp.body = "{\"error\":\"method not allowed\"}";
            return;
        }
        if (req.path == "/status") {
            resp.status = 200;
            resp.status_text = "OK";
            resp.headers["Content-Type"] = "application/json";
            resp.body = build_status_json(st_);
            return;
        } else if (req.path == "/readings") {
            resp.status = 200;
            resp.status_text = "OK";
            resp.headers["Content-Type"] = "application/json";
            resp.body = build_readings_json(st_);
            return;
        } else {
            resp.status = 404;
            resp.status_text = "Not Found";
            resp.headers["Content-Type"] = "application/json";
            resp.body = "{\"error\":\"not found\"}";
            return;
        }
    }
private:
    DriverState* st_;
};

int main() {
    std::signal(SIGINT, signal_handler);
    std::signal(SIGTERM, signal_handler);

    Config cfg;
    try {
        cfg = load_config_from_env();
    } catch (const std::exception& ex) {
        log_error(std::string("Configuration error: ") + ex.what());
        return 1;
    }

    DriverState state;
    state.cfg = cfg;

    // Start poller
    Poller poller(&state);
    poller.start();

    // Start HTTP server
    DriverHttpHandler handler(&state);
    HttpServer server(cfg.http_host, cfg.http_port, &handler);

    if (!server.start()) {
        log_error("Failed to start HTTP server on " + cfg.http_host + ":" + std::to_string(cfg.http_port));
        poller.stop();
        return 1;
    }

    log_info("HTTP server listening on " + cfg.http_host + ":" + std::to_string(cfg.http_port));
    log_info("Endpoints: GET /status, GET /readings");

    // Wait for stop
    while (!g_should_stop.load()) {
        std::this_thread::sleep_for(std::chrono::milliseconds(200));
    }

    log_info("Shutting down...");
    server.stop();
    poller.stop();
    log_info("Shutdown complete.");
    return 0;
}
