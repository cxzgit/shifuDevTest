import os
import sys
from dataclasses import dataclass


@dataclass
class Config:
    http_host: str
    http_port: int
    modbus_host: str
    modbus_port: int
    modbus_unit_id: int
    sampling_interval_seconds: float
    retry_count: int
    timeout_ms: int
    backoff_initial_seconds: float
    backoff_max_seconds: float
    reg_temp_addr: int
    reg_hum_addr: int
    reg_status_addr: int
    alarm_temp_low: float
    alarm_temp_high: float
    alarm_hum_low: float
    alarm_hum_high: float
    log_level: str


def _require_env(name: str) -> str:
    v = os.getenv(name)
    if v is None or v == "":
        print(f"Missing required environment variable: {name}", file=sys.stderr)
        sys.exit(1)
    return v


def load_config() -> Config:
    # HTTP server
    http_host = _require_env("HTTP_HOST")
    http_port = int(_require_env("HTTP_PORT"))

    # Modbus TCP settings
    modbus_host = _require_env("MODBUS_HOST")
    modbus_port = int(_require_env("MODBUS_PORT"))
    modbus_unit_id = int(_require_env("MODBUS_UNIT_ID"))

    # Polling and resilience
    sampling_interval_seconds = float(_require_env("SAMPLING_INTERVAL_SECONDS"))
    retry_count = int(_require_env("RETRY_COUNT"))
    timeout_ms = int(_require_env("TIMEOUT_MS"))
    backoff_initial_seconds = float(_require_env("BACKOFF_INITIAL_SECONDS"))
    backoff_max_seconds = float(_require_env("BACKOFF_MAX_SECONDS"))

    # Register addresses
    reg_temp_addr = int(_require_env("REG_TEMP_ADDR"))
    reg_hum_addr = int(_require_env("REG_HUM_ADDR"))
    reg_status_addr = int(_require_env("REG_STATUS_ADDR"))

    # Alarm thresholds (driver-level)
    alarm_temp_low = float(_require_env("ALARM_TEMP_LOW"))
    alarm_temp_high = float(_require_env("ALARM_TEMP_HIGH"))
    alarm_hum_low = float(_require_env("ALARM_HUM_LOW"))
    alarm_hum_high = float(_require_env("ALARM_HUM_HIGH"))

    # Logging
    log_level = _require_env("LOG_LEVEL")

    # Basic validations
    if sampling_interval_seconds <= 0:
        print("SAMPLING_INTERVAL_SECONDS must be > 0", file=sys.stderr)
        sys.exit(1)
    if retry_count < 0:
        print("RETRY_COUNT must be >= 0", file=sys.stderr)
        sys.exit(1)
    if timeout_ms <= 0:
        print("TIMEOUT_MS must be > 0", file=sys.stderr)
        sys.exit(1)
    if backoff_initial_seconds <= 0:
        print("BACKOFF_INITIAL_SECONDS must be > 0", file=sys.stderr)
        sys.exit(1)
    if backoff_max_seconds < backoff_initial_seconds:
        print("BACKOFF_MAX_SECONDS must be >= BACKOFF_INITIAL_SECONDS", file=sys.stderr)
        sys.exit(1)
    if alarm_temp_low >= alarm_temp_high:
        print("ALARM_TEMP_LOW must be < ALARM_TEMP_HIGH", file=sys.stderr)
        sys.exit(1)
    if alarm_hum_low >= alarm_hum_high:
        print("ALARM_HUM_LOW must be < ALARM_HUM_HIGH", file=sys.stderr)
        sys.exit(1)

    return Config(
        http_host=http_host,
        http_port=http_port,
        modbus_host=modbus_host,
        modbus_port=modbus_port,
        modbus_unit_id=modbus_unit_id,
        sampling_interval_seconds=sampling_interval_seconds,
        retry_count=retry_count,
        timeout_ms=timeout_ms,
        backoff_initial_seconds=backoff_initial_seconds,
        backoff_max_seconds=backoff_max_seconds,
        reg_temp_addr=reg_temp_addr,
        reg_hum_addr=reg_hum_addr,
        reg_status_addr=reg_status_addr,
        alarm_temp_low=alarm_temp_low,
        alarm_temp_high=alarm_temp_high,
        alarm_hum_low=alarm_hum_low,
        alarm_hum_high=alarm_hum_high,
        log_level=log_level,
    )
