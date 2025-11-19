import os
from dataclasses import dataclass


def _get_env_required(name: str) -> str:
    val = os.getenv(name)
    if val is None or val == '':
        raise ValueError(f'Missing required environment variable: {name}')
    return val


def _get_env_optional(name: str, default=None):
    return os.getenv(name, default)


def _parse_int(name: str, required: bool = True, default=None) -> int:
    raw = os.getenv(name)
    if raw is None or raw == '':
        if required:
            raise ValueError(f'Missing required environment variable: {name}')
        return default
    try:
        return int(raw)
    except Exception:
        raise ValueError(f'Invalid int for {name}: {raw}')


def _parse_float(name: str, required: bool = False, default=None):
    raw = os.getenv(name)
    if raw is None or raw == '':
        if required:
            raise ValueError(f'Missing required environment variable: {name}')
        return default
    try:
        return float(raw)
    except Exception:
        raise ValueError(f'Invalid float for {name}: {raw}')


@dataclass
class Config:
    http_host: str
    http_port: int

    modbus_port: str | None
    modbus_slave_id: int | None
    modbus_timeout_ms: int

    poll_interval_ms: int
    backoff_initial_ms: int
    backoff_max_ms: int
    retry_max: int

    reg_temp_addr: int
    reg_hum_addr: int
    reg_co2_addr: int

    reg_temp_scale: float | None
    reg_hum_scale: float | None
    reg_co2_scale: float | None


def load_config() -> Config:
    http_host = _get_env_required('HTTP_HOST')
    http_port = _parse_int('HTTP_PORT', required=True)

    # Optional: can be provided via POST /connect
    modbus_port = _get_env_optional('MODBUS_PORT', None)
    modbus_slave_id = None
    raw_slave = os.getenv('MODBUS_SLAVE_ID')
    if raw_slave is not None and raw_slave != '':
        try:
            modbus_slave_id = int(raw_slave)
        except Exception:
            raise ValueError('Invalid int for MODBUS_SLAVE_ID')

    modbus_timeout_ms = _parse_int('MODBUS_TIMEOUT_MS', required=True)

    poll_interval_ms = _parse_int('POLL_INTERVAL_MS', required=True)
    backoff_initial_ms = _parse_int('BACKOFF_INITIAL_MS', required=True)
    backoff_max_ms = _parse_int('BACKOFF_MAX_MS', required=True)
    retry_max = _parse_int('RETRY_MAX', required=True)

    reg_temp_addr = _parse_int('REG_TEMP_ADDR', required=True)
    reg_hum_addr = _parse_int('REG_HUM_ADDR', required=True)
    reg_co2_addr = _parse_int('REG_CO2_ADDR', required=True)

    reg_temp_scale = _parse_float('REG_TEMP_SCALE', required=False, default=None)
    reg_hum_scale = _parse_float('REG_HUM_SCALE', required=False, default=None)
    reg_co2_scale = _parse_float('REG_CO2_SCALE', required=False, default=None)

    return Config(
        http_host=http_host,
        http_port=http_port,
        modbus_port=modbus_port,
        modbus_slave_id=modbus_slave_id,
        modbus_timeout_ms=modbus_timeout_ms,
        poll_interval_ms=poll_interval_ms,
        backoff_initial_ms=backoff_initial_ms,
        backoff_max_ms=backoff_max_ms,
        retry_max=retry_max,
        reg_temp_addr=reg_temp_addr,
        reg_hum_addr=reg_hum_addr,
        reg_co2_addr=reg_co2_addr,
        reg_temp_scale=reg_temp_scale,
        reg_hum_scale=reg_hum_scale,
        reg_co2_scale=reg_co2_scale,
    )
