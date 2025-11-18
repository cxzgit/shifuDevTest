"use strict";

function parseBool(val, def) {
  if (val === undefined) return def;
  const s = String(val).toLowerCase().trim();
  if (["1", "true", "yes", "y", "on"].includes(s)) return true;
  if (["0", "false", "no", "n", "off"].includes(s)) return false;
  return def;
}

function parseIntEnv(name, def) {
  const v = process.env[name];
  if (v === undefined || v === "") return def;
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : def;
}

function parseJSONEnv(name, def) {
  const v = process.env[name];
  if (v === undefined || v === "") return def;
  try {
    return JSON.parse(v);
  } catch {
    return def;
  }
}

const config = {
  server: {
    host: process.env.HTTP_HOST || "0.0.0.0",
    port: parseIntEnv("HTTP_PORT", 8000),
  },
  device: {
    protocol: (process.env.DEVICE_PROTOCOL || "HTTP").toUpperCase(),
    pollIntervalMs: parseIntEnv("POLL_INTERVAL_MS", 2000),
    timeoutMs: parseIntEnv("TIMEOUT_MS", 5000),
    backoffMinMs: parseIntEnv("BACKOFF_MIN_MS", 1000),
    backoffMaxMs: parseIntEnv("BACKOFF_MAX_MS", 30000),
    http: {
      url: process.env.DEV_HTTP_URL || "",
      basicUser: process.env.DEV_HTTP_BASIC_USER || "",
      basicPass: process.env.DEV_HTTP_BASIC_PASS || "",
      insecure: parseBool(process.env.DEV_HTTP_INSECURE, false),
    },
    modbus: {
      host: process.env.MB_HOST || "",
      port: parseIntEnv("MB_PORT", 502),
      unitId: parseIntEnv("MB_UNIT_ID", 1),
      startAddr: parseIntEnv("MB_START_ADDR", 0),
      numRegs: process.env.MB_NUM_REGS ? parseIntEnv("MB_NUM_REGS", 0) : 0,
      wordOrder: (process.env.MB_WORD_ORDER || "BE").toUpperCase(),
      map: parseJSONEnv("MB_MAP_JSON", null),
    },
  },
};

module.exports = config;
