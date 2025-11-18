"use strict";
const http = require("http");
const https = require("https");
const { URL } = require("url");
const net = require("net");
const config = require("./config");
const pkg = require("./package.json");

function log(level, msg, details) {
  const entry = {
    ts: new Date().toISOString(),
    level,
    msg,
  };
  if (details && Object.keys(details).length) entry.details = details;
  const line = JSON.stringify(entry);
  if (level === "error" || level === "warn") console.error(line);
  else console.log(line);
}

function sleep(ms) {
  return new Promise((res) => setTimeout(res, ms));
}

// Shared state for /status endpoint
const state = {
  driver_version: pkg.version,
  started_at: new Date().toISOString(),
  device_online: false,
  active_protocol: config.device.protocol,
  last_success_read: null,
  last_error: null,
  current_backoff_ms: 0,
  poll_interval_ms: config.device.pollIntervalMs,
  timeout_ms: config.device.timeoutMs,
};

class HttpDevicePoller {
  constructor(cfg, onSample, onError) {
    this.cfg = cfg;
    this.onSample = onSample;
    this.onError = onError;
    this.running = false;
    this.backoff = this.cfg.backoffMinMs;
  }

  async start() {
    this.running = true;
    log("info", "HTTP device poller starting", { url: this.cfg.http.url });
    while (this.running) {
      try {
        const data = await this.fetchOnce();
        this.onSample({ protocol: "HTTP", data });
        this.backoff = this.cfg.backoffMinMs;
        await sleep(this.cfg.pollIntervalMs);
      } catch (err) {
        this.onError(err);
        const wait = Math.min(this.backoff, this.cfg.backoffMaxMs);
        state.current_backoff_ms = wait;
        log("warn", "HTTP device poll failed, backing off", { wait_ms: wait, error: String(err && err.message || err) });
        await sleep(wait);
        this.backoff = Math.min(this.cfg.backoffMaxMs, Math.max(this.cfg.backoffMinMs, Math.floor(this.backoff * 2)));
      }
    }
    log("info", "HTTP device poller stopped");
  }

  stop() {
    this.running = false;
  }

  async fetchOnce() {
    if (!this.cfg.http.url) throw new Error("DEV_HTTP_URL not set");
    const urlObj = new URL(this.cfg.http.url);
    const isHttps = urlObj.protocol === "https:";
    const lib = isHttps ? https : http;

    const headers = { Accept: "application/json" };
    if (this.cfg.http.basicUser || this.cfg.http.basicPass) {
      const token = Buffer.from(`${this.cfg.http.basicUser}:${this.cfg.http.basicPass}`).toString("base64");
      headers["Authorization"] = `Basic ${token}`;
    }

    const options = {
      protocol: urlObj.protocol,
      hostname: urlObj.hostname,
      port: urlObj.port || (isHttps ? 443 : 80),
      path: urlObj.pathname + (urlObj.search || ""),
      method: "GET",
      headers,
      timeout: this.cfg.timeoutMs,
      rejectUnauthorized: isHttps ? !this.cfg.http.insecure : undefined,
    };

    return new Promise((resolve, reject) => {
      const req = lib.request(options, (res) => {
        const { statusCode } = res;
        if (statusCode < 200 || statusCode >= 300) {
          res.resume();
          reject(new Error(`HTTP device responded with status ${statusCode}`));
          return;
        }
        let body = "";
        res.setEncoding("utf8");
        res.on("data", (chunk) => (body += chunk));
        res.on("end", () => {
          try {
            const data = JSON.parse(body);
            resolve(data);
          } catch (e) {
            reject(new Error("Invalid JSON payload from device"));
          }
        });
      });

      req.on("timeout", () => {
        req.destroy(new Error("HTTP request timeout"));
      });
      req.on("error", (err) => reject(err));
      req.end();
    });
  }
}

class ModbusTcpPoller {
  constructor(cfg, onSample, onError) {
    this.cfg = cfg;
    this.onSample = onSample;
    this.onError = onError;
    this.running = false;
    this.backoff = this.cfg.backoffMinMs;
    this.socket = null;
    this.connected = false;
    this.recvBuffer = Buffer.alloc(0);
    this.expectedFrameLength = null;
    this.currentRequest = null; // { transId, resolve, reject, timeout }
    this.transId = 1;
  }

  async start() {
    this.running = true;
    log("info", "Modbus TCP poller starting", { host: this.cfg.modbus.host, port: this.cfg.modbus.port, unitId: this.cfg.modbus.unitId });
    while (this.running) {
      try {
        if (!this.connected) {
          await this.connect();
        }
        const { startAddr, quantity } = this.computeRange();
        const regs = await this.readHoldingRegisters(startAddr, quantity, this.cfg.timeoutMs);
        const sample = this.parseRegisters(regs);
        this.onSample({ protocol: "MODBUS_TCP", data: sample });
        this.backoff = this.cfg.backoffMinMs;
        await sleep(this.cfg.pollIntervalMs);
      } catch (err) {
        this.onError(err);
        this.disconnect();
        const wait = Math.min(this.backoff, this.cfg.backoffMaxMs);
        state.current_backoff_ms = wait;
        log("warn", "Modbus poll failed, backing off", { wait_ms: wait, error: String(err && err.message || err) });
        await sleep(wait);
        this.backoff = Math.min(this.cfg.backoffMaxMs, Math.max(this.cfg.backoffMinMs, Math.floor(this.backoff * 2)));
      }
    }
    this.disconnect();
    log("info", "Modbus TCP poller stopped");
  }

  stop() {
    this.running = false;
    this.disconnect();
  }

  connect() {
    if (!this.cfg.modbus.host) return Promise.reject(new Error("MB_HOST not set"));
    return new Promise((resolve, reject) => {
      const socket = net.createConnection({ host: this.cfg.modbus.host, port: this.cfg.modbus.port }, () => {
        this.socket = socket;
        this.connected = true;
        this.recvBuffer = Buffer.alloc(0);
        this.expectedFrameLength = null;
        socket.setKeepAlive(true, 10000);
        log("info", "Modbus TCP connected", { host: this.cfg.modbus.host, port: this.cfg.modbus.port });
        resolve();
      });
      socket.on("error", (err) => {
        if (!this.connected) {
          reject(err);
        } else {
          log("error", "Modbus socket error", { error: String(err && err.message || err) });
        }
      });
      socket.on("close", () => {
        if (this.connected) log("warn", "Modbus TCP disconnected");
        this.connected = false;
        this.socket = null;
        if (this.currentRequest && this.currentRequest.reject) {
          this.currentRequest.reject(new Error("Socket closed"));
          this.currentRequest = null;
        }
      });
      socket.on("data", (chunk) => this.onData(chunk));
    });
  }

  disconnect() {
    if (this.socket) {
      try { this.socket.destroy(); } catch (_) {}
    }
    this.connected = false;
    this.socket = null;
    this.recvBuffer = Buffer.alloc(0);
    this.expectedFrameLength = null;
    if (this.currentRequest && this.currentRequest.reject) {
      this.currentRequest.reject(new Error("Disconnected"));
      this.currentRequest = null;
    }
  }

  computeRange() {
    const startAddr = this.cfg.modbus.startAddr | 0;
    if (this.cfg.modbus.numRegs && this.cfg.modbus.numRegs > 0) {
      return { startAddr, quantity: this.cfg.modbus.numRegs };
    }
    // Compute from mapping if present
    let maxOffset = 0;
    if (this.cfg.modbus.map && typeof this.cfg.modbus.map === "object") {
      for (const k of Object.keys(this.cfg.modbus.map)) {
        const item = this.cfg.modbus.map[k];
        if (!item || typeof item.offset !== "number") continue;
        let width = 1;
        const type = String(item.type || "uint16").toLowerCase();
        if (["int32", "uint32", "float32"].includes(type)) width = 2;
        maxOffset = Math.max(maxOffset, item.offset + width);
      }
    } else {
      maxOffset = 10; // default minimal range
    }
    return { startAddr, quantity: maxOffset };
  }

  buildReadHoldingRegsFrame(transId, unitId, startAddr, quantity) {
    const pdu = Buffer.alloc(5);
    pdu[0] = 0x03; // function code
    pdu[1] = (startAddr >> 8) & 0xff;
    pdu[2] = startAddr & 0xff;
    pdu[3] = (quantity >> 8) & 0xff;
    pdu[4] = quantity & 0xff;
    const len = 1 + pdu.length; // unitId + pdu
    const mbap = Buffer.alloc(7);
    mbap[0] = (transId >> 8) & 0xff;
    mbap[1] = transId & 0xff;
    mbap[2] = 0x00; // protocol id high
    mbap[3] = 0x00; // protocol id low
    mbap[4] = (len >> 8) & 0xff;
    mbap[5] = len & 0xff;
    mbap[6] = unitId & 0xff;
    return Buffer.concat([mbap, pdu]);
  }

  onData(chunk) {
    this.recvBuffer = Buffer.concat([this.recvBuffer, chunk]);
    while (true) {
      if (this.recvBuffer.length < 7) return; // need MBAP
      const lenField = (this.recvBuffer[4] << 8) | this.recvBuffer[5];
      const totalLen = 6 + lenField; // MBAP (6) + remaining length
      if (this.recvBuffer.length < totalLen) return; // wait more
      const frame = this.recvBuffer.slice(0, totalLen);
      const rest = this.recvBuffer.slice(totalLen);
      this.recvBuffer = rest;
      this.handleFrame(frame);
    }
  }

  handleFrame(frame) {
    if (!this.currentRequest) return; // unexpected
    const transId = (frame[0] << 8) | frame[1];
    const unitId = frame[6];
    const func = frame[7];
    if (transId !== this.currentRequest.transId || unitId !== this.cfg.modbus.unitId) {
      // ignore mismatched frame
      return;
    }
    if (func === 0x83) {
      // exception response for function 0x03
      const exCode = frame[8];
      const err = new Error(`Modbus exception ${exCode}`);
      const req = this.currentRequest; this.currentRequest = null;
      clearTimeout(req.timeout);
      return req.reject(err);
    }
    if (func !== 0x03) {
      const err = new Error(`Unexpected function code ${func}`);
      const req = this.currentRequest; this.currentRequest = null;
      clearTimeout(req.timeout);
      return req.reject(err);
    }
    const byteCount = frame[8];
    const data = frame.slice(9, 9 + byteCount);
    const req = this.currentRequest; this.currentRequest = null;
    clearTimeout(req.timeout);
    return req.resolve(data);
  }

  readHoldingRegisters(startAddr, quantity, timeoutMs) {
    return new Promise((resolve, reject) => {
      if (!this.socket || !this.connected) return reject(new Error("Not connected"));
      const transId = (this.transId = (this.transId + 1) & 0xffff) || 1;
      const frame = this.buildReadHoldingRegsFrame(transId, this.cfg.modbus.unitId, startAddr, quantity);
      const timeout = setTimeout(() => {
        if (this.currentRequest) {
          const req = this.currentRequest; this.currentRequest = null;
          reject(new Error("Modbus request timeout"));
        }
      }, timeoutMs);
      this.currentRequest = { transId, resolve: (data) => {
        try {
          if (data.length !== quantity * 2) {
            reject(new Error("Invalid data length"));
            return;
          }
          const regs = [];
          for (let i = 0; i < quantity; i++) {
            regs.push((data[i * 2] << 8) | data[i * 2 + 1]);
          }
          resolve(regs);
        } finally {
          clearTimeout(timeout);
        }
      }, reject: (err) => { clearTimeout(timeout); reject(err); }, timeout };
      try {
        this.socket.write(frame);
      } catch (e) {
        clearTimeout(timeout);
        this.currentRequest = null;
        reject(e);
      }
    });
  }

  parseRegisters(regs) {
    const m = this.cfg.modbus.map;
    if (!m || typeof m !== "object") {
      return { raw_registers: regs };
    }
    const wordOrder = (this.cfg.modbus.wordOrder || "BE").toUpperCase();
    const getInt16 = (v) => (v & 0x8000 ? v - 0x10000 : v);
    const rd32 = (offset) => {
      // returns 4-byte Buffer based on word order
      const hi = regs[offset] || 0;
      const lo = regs[offset + 1] || 0;
      const buf = Buffer.alloc(4);
      if (wordOrder === "LE") {
        // low word first
        buf[0] = (lo >> 8) & 0xff; buf[1] = lo & 0xff; buf[2] = (hi >> 8) & 0xff; buf[3] = hi & 0xff;
      } else {
        // BE word order: high word first
        buf[0] = (hi >> 8) & 0xff; buf[1] = hi & 0xff; buf[2] = (lo >> 8) & 0xff; buf[3] = lo & 0xff;
      }
      return buf;
    };
    const out = {};
    for (const key of Object.keys(m)) {
      const item = m[key];
      if (!item || typeof item.offset !== "number") continue;
      const type = String(item.type || "uint16").toLowerCase();
      const scale = typeof item.scale === "number" ? item.scale : 1;
      if (type === "int16") {
        const raw = regs[item.offset] || 0;
        out[key] = getInt16(raw) * scale;
      } else if (type === "uint16") {
        const raw = regs[item.offset] || 0;
        out[key] = raw * scale;
      } else if (type === "int32") {
        const buf = rd32(item.offset);
        let val = buf.readInt32BE(0);
        out[key] = val * scale;
      } else if (type === "uint32") {
        const buf = rd32(item.offset);
        let val = buf.readUInt32BE(0);
        out[key] = val * scale;
      } else if (type === "float32") {
        const buf = rd32(item.offset);
        const val = buf.readFloatBE(0);
        out[key] = val * scale;
      } else if (type === "bits16") {
        const raw = regs[item.offset] || 0;
        if (item.labels && typeof item.labels === "object") {
          const flags = {};
          for (const [lname, bit] of Object.entries(item.labels)) {
            const b = Number(bit);
            if (Number.isFinite(b)) flags[lname] = ((raw >> b) & 1) === 1;
          }
          out[key] = flags;
        } else {
          out[key] = raw; // bitmask
        }
      } else {
        // default to uint16
        const raw = regs[item.offset] || 0;
        out[key] = raw * scale;
      }
    }
    return out;
  }
}

class DeviceManager {
  constructor(config) {
    this.cfg = config;
    this.poller = null;
    this.lastSample = null;
  }

  async start() {
    const onSample = ({ protocol, data }) => {
      this.lastSample = { protocol, data, at: new Date().toISOString() };
      state.device_online = true;
      state.last_success_read = this.lastSample.at;
      state.last_error = null;
      state.active_protocol = protocol;
      state.current_backoff_ms = 0;
      log("info", "Device sample updated", { protocol, at: this.lastSample.at });
    };
    const onError = (err) => {
      const now = new Date().toISOString();
      state.device_online = false;
      state.last_error = { message: String(err && err.message || err), at: now };
      log("error", "Device poll error", { error: state.last_error.message });
    };

    if (this.cfg.device.protocol === "HTTP") {
      this.poller = new HttpDevicePoller(this.cfg.device, onSample, onError);
      this.poller.start();
    } else if (this.cfg.device.protocol === "MODBUS_TCP") {
      this.poller = new ModbusTcpPoller(this.cfg.device, onSample, onError);
      this.poller.start();
    } else {
      log("error", "Unsupported DEVICE_PROTOCOL", { protocol: this.cfg.device.protocol });
      throw new Error(`Unsupported DEVICE_PROTOCOL: ${this.cfg.device.protocol}`);
    }
  }

  async stop() {
    if (this.poller && typeof this.poller.stop === "function") {
      this.poller.stop();
    }
  }
}

function createServer(host, port) {
  const server = http.createServer((req, res) => {
    if (req.method === "GET" && req.url) {
      try {
        const url = new URL(req.url, `http://${req.headers.host}`);
        if (url.pathname === "/status") {
          const body = JSON.stringify({
            device_online: state.device_online,
            active_protocol: state.active_protocol,
            last_success_read: state.last_success_read,
            last_error: state.last_error,
            current_backoff_ms: state.current_backoff_ms,
            poll_interval_ms: state.poll_interval_ms,
            timeout_ms: state.timeout_ms,
            driver_version: state.driver_version,
            started_at: state.started_at,
            now: new Date().toISOString(),
          });
          res.statusCode = 200;
          res.setHeader("Content-Type", "application/json");
          res.setHeader("Cache-Control", "no-store");
          res.end(body);
          return;
        }
      } catch (_) {}
    }
    res.statusCode = 404;
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ error: "Not Found" }));
  });

  server.on("clientError", (err, socket) => {
    try { socket.end("HTTP/1.1 400 Bad Request\r\n\r\n"); } catch (_) {}
    log("warn", "Client error", { error: String(err && err.message || err) });
  });

  server.listen(port, host, () => {
    log("info", "HTTP server listening", { host, port });
  });

  return server;
}

(async () => {
  const manager = new DeviceManager(config);
  const server = createServer(config.server.host, config.server.port);

  const shutdown = async (signal) => {
    log("info", `Shutting down on ${signal}`);
    try { await manager.stop(); } catch (_) {}
    try {
      server.close(() => {
        log("info", "HTTP server closed");
        process.exit(0);
      });
      // Fallback exit in case close callback not invoked
      setTimeout(() => process.exit(0), 2000).unref();
    } catch (_) {
      process.exit(0);
    }
  };

  process.on("SIGINT", () => shutdown("SIGINT"));
  process.on("SIGTERM", () => shutdown("SIGTERM"));

  try {
    await manager.start();
  } catch (e) {
    log("error", "Failed to start device manager", { error: String(e && e.message || e) });
  }
})();
