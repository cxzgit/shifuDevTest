#pragma once
#include <string>

struct Config {
    std::string http_host;
    int http_port;

    std::string serial_port;
    int modbus_slave_id;

    int reg_temperature;
    int reg_humidity;
    int reg_co2;

    double scale_temperature;
    double scale_humidity;
    double scale_co2;

    int poll_interval_ms;
    int modbus_timeout_ms;
    int backoff_initial_ms;
    int backoff_max_ms;
};

Config load_config_from_env();
