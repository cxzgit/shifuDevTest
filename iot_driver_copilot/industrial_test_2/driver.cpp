#include <iostream>
#include <string>
#include <atomic>
#include <thread>
#include <mutex>
#include <vector>
#include <csignal>
#include <chrono>
#include <cmath>

#include "config.h"
#include "serial_port.h"
#include "modbus_rtu.h"
#include "http_server.h"

using namespace std;

static atomic<bool> g_running(true);

static string iso8601(const chrono::system_clock::time_point &tp) {
    time_t t = chrono::system_clock::to_time_t(tp);
    struct tm gmt;
    gmtime_r(&t, &gmt);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &gmt);
    return string(buf);
}

static void log_msg(const string &level, const string &msg) {
    auto now = chrono::system_clock::now();
    time_t t = chrono::system_clock::to_time_t(now);
    char buf[64];
    strftime(buf, sizeof(buf), "%Y-%m-%d %H:%M:%S", localtime(&t));
    cerr << "[" << buf << "] " << level << ": " << msg << endl;
}

struct SharedData {
    mutex mtx;
    // readings
    double temperature = NAN;
    double humidity = NAN;
    double co2 = NAN;

    chrono::system_clock::time_point temp_time{};
    chrono::system_clock::time_point humid_time{};
    chrono::system_clock::time_point co2_time{};
    chrono::system_clock::time_point last_poll_time{};

    // status
    bool serial_connected = false;
    bool polling_active = false;
    string port_name;
    int slave_id = 0;
    int poll_interval_ms = 0;
};

void handle_signal(int sig) {
    (void)sig;
    g_running.store(false);
}

string build_status_json(SharedData &shared) {
    lock_guard<mutex> lk(shared.mtx);
    string lastPoll = shared.last_poll_time.time_since_epoch().count() == 0 ? "" : iso8601(shared.last_poll_time);
    string json = string("{") +
        "\"serial_connected\":" + (shared.serial_connected ? "true" : "false") + 
        ",\"port\":\"" + shared.port_name + "\"" +
        ",\"slave_id\":" + to_string(shared.slave_id) +
        ",\"polling_active\":" + (shared.polling_active ? "true" : "false") +
        ",\"poll_interval_ms\":" + to_string(shared.poll_interval_ms) +
        ",\"last_poll_time\":\"" + lastPoll + "\"" +
        "}";
    return json;
}

string build_readings_json(SharedData &shared) {
    lock_guard<mutex> lk(shared.mtx);
    auto fmt = [&](const chrono::system_clock::time_point &tp) {
        return tp.time_since_epoch().count() == 0 ? string("") : iso8601(tp);
    };
    string json = "{";
    if (std::isnan(shared.temperature)) json += "\"temperature\":null"; else json += "\"temperature\":" + to_string(shared.temperature);
    json += ",";
    if (std::isnan(shared.humidity)) json += "\"humidity\":null"; else json += "\"humidity\":" + to_string(shared.humidity);
    json += ",";
    if (std::isnan(shared.co2)) json += "\"co2\":null"; else json += "\"co2\":" + to_string(shared.co2);
    json += ",\"temperature_time\":\"" + fmt(shared.temp_time) + "\"";
    json += ",\"humidity_time\":\"" + fmt(shared.humid_time) + "\"";
    json += ",\"co2_time\":\"" + fmt(shared.co2_time) + "\"";
    json += ",\"last_poll_time\":\"" + fmt(shared.last_poll_time) + "\"";
    json += "}";
    return json;
}

void poll_loop(const Config &cfg, SharedData &shared) {
    SerialPort serial;
    ModbusRTU modbus;

    int backoff = cfg.backoff_initial_ms;

    while (g_running.load()) {
        if (!serial.isOpen()) {
            log_msg("INFO", string("Attempting serial connect to ") + cfg.serial_port);
            if (!serial.open(cfg.serial_port, cfg.serial_baud, cfg.serial_parity, cfg.serial_data_bits, cfg.serial_stop_bits)) {
                shared.serial_connected = false;
                shared.polling_active = false;
                log_msg("ERROR", "Failed to open serial port. Retrying with backoff: " + to_string(backoff) + " ms");
                this_thread::sleep_for(chrono::milliseconds(backoff));
                backoff = min(backoff * 2, cfg.backoff_max_ms);
                continue;
            }
            shared.serial_connected = true;
            log_msg("INFO", "Serial port connected.");
            backoff = cfg.backoff_initial_ms;
        }

        shared.polling_active = true;
        // read temperature
        vector<uint16_t> regs;
        bool ok = modbus.readHoldingRegisters(serial, (uint8_t)cfg.modbus_slave_id, (uint16_t)cfg.reg_temp, 1, regs, cfg.read_timeout_ms);
        if (!ok) {
            log_msg("ERROR", "Modbus read (temperature) failed. Disconnecting and backing off.");
            serial.close();
            shared.serial_connected = false;
            shared.polling_active = false;
            this_thread::sleep_for(chrono::milliseconds(backoff));
            backoff = min(backoff * 2, cfg.backoff_max_ms);
            continue;
        } else {
            double val = (double)regs[0] * cfg.temp_scale;
            {
                lock_guard<mutex> lk(shared.mtx);
                shared.temperature = val;
                shared.temp_time = chrono::system_clock::now();
            }
        }

        // read humidity
        regs.clear();
        ok = modbus.readHoldingRegisters(serial, (uint8_t)cfg.modbus_slave_id, (uint16_t)cfg.reg_humid, 1, regs, cfg.read_timeout_ms);
        if (!ok) {
            log_msg("ERROR", "Modbus read (humidity) failed. Disconnecting and backing off.");
            serial.close();
            shared.serial_connected = false;
            shared.polling_active = false;
            this_thread::sleep_for(chrono::milliseconds(backoff));
            backoff = min(backoff * 2, cfg.backoff_max_ms);
            continue;
        } else {
            double val = (double)regs[0] * cfg.humid_scale;
            {
                lock_guard<mutex> lk(shared.mtx);
                shared.humidity = val;
                shared.humid_time = chrono::system_clock::now();
            }
        }

        // read CO2
        regs.clear();
        ok = modbus.readHoldingRegisters(serial, (uint8_t)cfg.modbus_slave_id, (uint16_t)cfg.reg_co2, 1, regs, cfg.read_timeout_ms);
        if (!ok) {
            log_msg("ERROR", "Modbus read (CO2) failed. Disconnecting and backing off.");
            serial.close();
            shared.serial_connected = false;
            shared.polling_active = false;
            this_thread::sleep_for(chrono::milliseconds(backoff));
            backoff = min(backoff * 2, cfg.backoff_max_ms);
            continue;
        } else {
            double val = (double)regs[0] * cfg.co2_scale;
            {
                lock_guard<mutex> lk(shared.mtx);
                shared.co2 = val;
                shared.co2_time = chrono::system_clock::now();
                shared.last_poll_time = shared.co2_time; // last poll after finishing all
            }
        }

        // sleep until next poll
        this_thread::sleep_for(chrono::milliseconds(cfg.poll_interval_ms));
    }

    if (serial.isOpen()) {
        serial.close();
    }
    shared.polling_active = false;
    shared.serial_connected = false;
}

int main() {
    signal(SIGINT, handle_signal);
    signal(SIGTERM, handle_signal);

    Config cfg;
    if (!load_config(cfg)) {
        log_msg("ERROR", "Invalid configuration. Exiting.");
        return 1;
    }

    SharedData shared;
    shared.port_name = cfg.serial_port;
    shared.slave_id = cfg.modbus_slave_id;
    shared.poll_interval_ms = cfg.poll_interval_ms;

    HttpServer server(cfg.http_host, cfg.http_port,
        [&](const string &path){
            if (path == "/status") return build_status_json(shared);
            if (path == "/readings") return build_readings_json(shared);
            return string("");
        }
    );

    if (!server.start()) {
        log_msg("ERROR", "Failed to start HTTP server.");
        return 1;
    }

    thread pollThread([&](){ poll_loop(cfg, shared); });

    log_msg("INFO", "Driver running. Endpoints: /status, /readings");

    // Wait until signal
    while (g_running.load()) {
        this_thread::sleep_for(chrono::milliseconds(200));
    }

    log_msg("INFO", "Shutting down...");

    server.stop();
    if (pollThread.joinable()) pollThread.join();

    log_msg("INFO", "Shutdown complete.");
    return 0;
}
