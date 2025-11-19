#include "config.h"
#include <cstdlib>
#include <stdexcept>
#include <string>
#include <cerrno>
#include <climits>

static std::string get_env_required(const char* key) {
    const char* v = std::getenv(key);
    if (!v || std::string(v).empty()) {
        throw std::runtime_error(std::string("Missing required environment variable: ") + key);
    }
    return std::string(v);
}

static long parse_long(const std::string& s, const char* name) {
    errno = 0;
    char* end = nullptr;
    long val = std::strtol(s.c_str(), &end, 10);
    if (errno != 0 || end == s.c_str() || *end != '\0') {
        throw std::runtime_error(std::string("Invalid integer for ") + name + ": " + s);
    }
    return val;
}

static double parse_double(const std::string& s, const char* name) {
    errno = 0;
    char* end = nullptr;
    double val = std::strtod(s.c_str(), &end);
    if (errno != 0 || end == s.c_str() || *end != '\0') {
        throw std::runtime_error(std::string("Invalid float for ") + name + ": " + s);
    }
    return val;
}

Config load_config_from_env() {
    Config cfg{};

    cfg.http_host = get_env_required("HTTP_HOST");
    cfg.http_port = (int)parse_long(get_env_required("HTTP_PORT"), "HTTP_PORT");

    cfg.serial_port = get_env_required("SERIAL_PORT");
    cfg.modbus_slave_id = (int)parse_long(get_env_required("MODBUS_SLAVE_ID"), "MODBUS_SLAVE_ID");

    cfg.reg_temperature = (int)parse_long(get_env_required("REG_TEMPERATURE"), "REG_TEMPERATURE");
    cfg.reg_humidity = (int)parse_long(get_env_required("REG_HUMIDITY"), "REG_HUMIDITY");
    cfg.reg_co2 = (int)parse_long(get_env_required("REG_CO2"), "REG_CO2");

    cfg.scale_temperature = parse_double(get_env_required("SCALE_TEMPERATURE"), "SCALE_TEMPERATURE");
    cfg.scale_humidity = parse_double(get_env_required("SCALE_HUMIDITY"), "SCALE_HUMIDITY");
    cfg.scale_co2 = parse_double(get_env_required("SCALE_CO2"), "SCALE_CO2");

    cfg.poll_interval_ms = (int)parse_long(get_env_required("POLL_INTERVAL_MS"), "POLL_INTERVAL_MS");
    cfg.modbus_timeout_ms = (int)parse_long(get_env_required("MODBUS_TIMEOUT_MS"), "MODBUS_TIMEOUT_MS");
    cfg.backoff_initial_ms = (int)parse_long(get_env_required("BACKOFF_INITIAL_MS"), "BACKOFF_INITIAL_MS");
    cfg.backoff_max_ms = (int)parse_long(get_env_required("BACKOFF_MAX_MS"), "BACKOFF_MAX_MS");

    return cfg;
}
