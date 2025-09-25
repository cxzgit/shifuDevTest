'use strict';

function getEnv(name, def, required = false) {
  const v = process.env[name];
  if ((v === undefined || v === '') && required) {
    throw new Error(`Missing required environment variable: ${name}`);
  }
  if (v === undefined || v === '') return def;
  return v;
}

function toInt(name, def, required = false) {
  const v = getEnv(name, def !== undefined ? String(def) : undefined, required);
  if (v === undefined) return undefined;
  const n = parseInt(v, 10);
  if (Number.isNaN(n)) throw new Error(`Invalid integer for ${name}: ${v}`);
  return n;
}

function toFloat(name, def, required = false) {
  const v = getEnv(name, def !== undefined ? String(def) : undefined, required);
  if (v === undefined) return undefined;
  const n = parseFloat(v);
  if (Number.isNaN(n)) throw new Error(`Invalid number for ${name}: ${v}`);
  return n;
}

function toListFloat(name, count, defVal) {
  const v = process.env[name];
  if (!v) return Array.from({ length: count }, () => defVal);
  const parts = v.split(',').map(s => s.trim()).filter(s => s.length > 0);
  const arr = parts.map(p => {
    const n = parseFloat(p);
    if (Number.isNaN(n)) throw new Error(`Invalid float value in ${name}: ${p}`);
    return n;
  });
  // Pad or trim to count
  if (arr.length < count) {
    while (arr.length < count) arr.push(defVal);
  } else if (arr.length > count) {
    arr.length = count;
  }
  return arr;
}

const config = (() => {
  const HTTP_HOST = getEnv('HTTP_HOST', '0.0.0.0');
  const HTTP_PORT = toInt('HTTP_PORT', 8080);

  const MODBUS_HOST = getEnv('MODBUS_HOST', undefined, true);
  const MODBUS_PORT = toInt('MODBUS_PORT', 502);
  const MODBUS_ID = toInt('MODBUS_ID', 1);
  const READ_TIMEOUT_MS = toInt('READ_TIMEOUT_MS', 1000);

  const POLL_INTERVAL_MS = toInt('POLL_INTERVAL_MS', 100);
  const BACKOFF_MIN_MS = toInt('BACKOFF_MIN_MS', 500);
  const BACKOFF_MAX_MS = toInt('BACKOFF_MAX_MS', 10000);

  const ANALOG_CHANNELS = toInt('ANALOG_CHANNELS', 8);
  const ANALOG_REG_START = toInt('ANALOG_REG_START', 0);
  const ANALOG_REG_TYPE = getEnv('ANALOG_REG_TYPE', 'INPUT'); // INPUT or HOLDING

  const DIGITAL_INPUT_COUNT = toInt('DIGITAL_INPUT_COUNT', 4);
  const DIGITAL_INPUT_START = toInt('DIGITAL_INPUT_START', 0);

  const ANALOG_SCALE = toListFloat('ANALOG_SCALE', ANALOG_CHANNELS, 1.0);
  const ANALOG_OFFSET = toListFloat('ANALOG_OFFSET', ANALOG_CHANNELS, 0.0);

  return {
    HTTP_HOST,
    HTTP_PORT,
    MODBUS_HOST,
    MODBUS_PORT,
    MODBUS_ID,
    READ_TIMEOUT_MS,
    POLL_INTERVAL_MS,
    BACKOFF_MIN_MS,
    BACKOFF_MAX_MS,
    ANALOG_CHANNELS,
    ANALOG_REG_START,
    ANALOG_REG_TYPE: ANALOG_REG_TYPE.toUpperCase() === 'HOLDING' ? 'HOLDING' : 'INPUT',
    DIGITAL_INPUT_COUNT,
    DIGITAL_INPUT_START,
    ANALOG_SCALE,
    ANALOG_OFFSET,
  };
})();

module.exports = config;
