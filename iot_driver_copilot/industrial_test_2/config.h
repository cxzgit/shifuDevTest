#ifndef CONFIG_H
#define CONFIG_H

#include <string>
#include <cstdlib>
#include <iostream>

struct Config {
    // HTTP
    std::string http_host;
    int http_port;

    // Serial/Modbus
    std::string serial_port;
    int serial_baud;
    char serial_parity; // 'N', 'E', 'O'
    int serial_data_bits; // 7 or 8
    int serial_stop_bits; // 1 or 2

    int modbus_slave_id; // 1..247

    // Registers
    int reg_temp;
    int reg_humid;
    int reg_co2;

    // Scaling
    double temp_scale;
    double humid_scale;
    double co2_scale;

    // Timing
    int poll_interval_ms;
    int read_timeout_ms;
    int backoff_initial_ms;
    int backoff_max_ms;
};

static bool get_env_str(const char *name, std::string &out) {
    const char *v = std::getenv(name);
    if (!v) {
        std::cerr << "Missing environment variable: " << name << std::endl;
        return false;
    }
    out = v;
    return true;
}

static bool get_env_int(const char *name, int &out) {
    std::string s;
    if (!get_env_str(name, s)) return false;
    try {
        size_t idx = 0;
        int val = std::stoi(s, &idx, 10);
        if (idx != s.size()) throw std::invalid_argument("extra chars");
        out = val;
        return true;
    } catch (...) {
        std::cerr << "Invalid integer for " << name << ": " << s << std::endl;
        return false;
    }
}

static bool get_env_double(const char *name, double &out) {
    std::string s;
    if (!get_env_str(name, s)) return false;
    try {
        size_t idx = 0;
        double val = std::stod(s, &idx);
        if (idx != s.size()) throw std::invalid_argument("extra chars");
        out = val;
        return true;
    } catch (...) {
        std::cerr << "Invalid double for " << name << ": " << s << std::endl;
        return false;
    }
}

static bool load_config(Config &cfg) {
    // HTTP
    if (!get_env_str("HTTP_HOST", cfg.http_host)) return false;
    if (!get_env_int("HTTP_PORT", cfg.http_port)) return false;
    if (cfg.http_port <= 0 || cfg.http_port > 65535) {
        std::cerr << "HTTP_PORT out of range" << std::endl;
        return false;
    }

    // Serial
    if (!get_env_str("SERIAL_PORT", cfg.serial_port)) return false;
    if (!get_env_int("SERIAL_BAUD", cfg.serial_baud)) return false;
    std::string parityStr;
    if (!get_env_str("SERIAL_PARITY", parityStr)) return false;
    if (parityStr.size() != 1 || (parityStr[0] != 'N' && parityStr[0] != 'E' && parityStr[0] != 'O')) {
        std::cerr << "SERIAL_PARITY must be one of N/E/O" << std::endl;
        return false;
    }
    cfg.serial_parity = parityStr[0];
    if (!get_env_int("SERIAL_DATA_BITS", cfg.serial_data_bits)) return false;
    if (!(cfg.serial_data_bits == 7 || cfg.serial_data_bits == 8)) {
        std::cerr << "SERIAL_DATA_BITS must be 7 or 8" << std::endl;
        return false;
    }
    if (!get_env_int("SERIAL_STOP_BITS", cfg.serial_stop_bits)) return false;
    if (!(cfg.serial_stop_bits == 1 || cfg.serial_stop_bits == 2)) {
        std::cerr << "SERIAL_STOP_BITS must be 1 or 2" << std::endl;
        return false;
    }

    if (!get_env_int("MODBUS_SLAVE_ID", cfg.modbus_slave_id)) return false;
    if (cfg.modbus_slave_id < 1 || cfg.modbus_slave_id > 247) {
        std::cerr << "MODBUS_SLAVE_ID must be in 1..247" << std::endl;
        return false;
    }

    // Registers
    if (!get_env_int("REG_TEMP", cfg.reg_temp)) return false;
    if (!get_env_int("REG_HUMID", cfg.reg_humid)) return false;
    if (!get_env_int("REG_CO2", cfg.reg_co2)) return false;
    if (cfg.reg_temp < 0 || cfg.reg_humid < 0 || cfg.reg_co2 < 0) {
        std::cerr << "Register addresses must be non-negative" << std::endl;
        return false;
    }

    // Scale
    if (!get_env_double("TEMP_SCALE", cfg.temp_scale)) return false;
    if (!get_env_double("HUMID_SCALE", cfg.humid_scale)) return false;
    if (!get_env_double("CO2_SCALE", cfg.co2_scale)) return false;

    // Timing
    if (!get_env_int("POLL_INTERVAL_MS", cfg.poll_interval_ms)) return false;
    if (!get_env_int("READ_TIMEOUT_MS", cfg.read_timeout_ms)) return false;
    if (!get_env_int("BACKOFF_INITIAL_MS", cfg.backoff_initial_ms)) return false;
    if (!get_env_int("BACKOFF_MAX_MS", cfg.backoff_max_ms)) return false;

    if (cfg.poll_interval_ms <= 0) { std::cerr << "POLL_INTERVAL_MS must be > 0" << std::endl; return false; }
    if (cfg.read_timeout_ms <= 0) { std::cerr << "READ_TIMEOUT_MS must be > 0" << std::endl; return false; }
    if (cfg.backoff_initial_ms <= 0 || cfg.backoff_max_ms < cfg.backoff_initial_ms) {
        std::cerr << "BACKOFF_INITIAL_MS must be > 0 and BACKOFF_MAX_MS >= BACKOFF_INITIAL_MS" << std::endl; return false;
    }

    return true;
}

#endif // CONFIG_H
