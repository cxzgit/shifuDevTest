import os
from dataclasses import dataclass


@dataclass
class Config:
    http_host: str
    http_port: int

    modbus_port: str
    slave_id: int

    poll_interval_ms: int
    modbus_timeout_s: float

    retry_max: int
    backoff_initial_ms: int
    backoff_max_ms: int

    reg_temperature_addr: int
    reg_humidity_addr: int
    reg_co2_addr: int

    scale_temperature: float
    scale_humidity: float
    scale_co2: float


def _require(name: str) -> str:
    val = os.getenv(name)
    if val is None or val == "":
        raise ValueError(f"Missing required environment variable: {name}")
    return val


def _parse_int(name: str) -> int:
    s = _require(name)
    try:
        return int(s)
    except ValueError as e:
        raise ValueError(f"Environment variable {name} must be an integer, got: {s}") from e


def _parse_float(name: str) -> float:
    s = _require(name)
    try:
        return float(s)
    except ValueError as e:
        raise ValueError(f"Environment variable {name} must be a float, got: {s}") from e


def load_config_from_env() -> Config:
    return Config(
        http_host=_require("HTTP_HOST"),
        http_port=_parse_int("HTTP_PORT"),

        modbus_port=_require("MODBUS_PORT"),
        slave_id=_parse_int("MODBUS_SLAVE_ID"),

        poll_interval_ms=_parse_int("POLL_INTERVAL_MS"),
        modbus_timeout_s=_parse_float("MODBUS_TIMEOUT_S"),

        retry_max=_parse_int("RETRY_MAX"),
        backoff_initial_ms=_parse_int("BACKOFF_INITIAL_MS"),
        backoff_max_ms=_parse_int("BACKOFF_MAX_MS"),

        reg_temperature_addr=_parse_int("REG_TEMPERATURE_ADDR"),
        reg_humidity_addr=_parse_int("REG_HUMIDITY_ADDR"),
        reg_co2_addr=_parse_int("REG_CO2_ADDR"),

        scale_temperature=_parse_float("SCALE_TEMPERATURE"),
        scale_humidity=_parse_float("SCALE_HUMIDITY"),
        scale_co2=_parse_float("SCALE_CO2"),
    )
