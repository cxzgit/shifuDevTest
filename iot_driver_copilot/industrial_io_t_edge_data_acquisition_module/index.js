'use strict';

const http = require('http');
const { URL } = require('url');
const ModbusRTU = require('modbus-serial');
const config = require('./config');

function nowISO() { return new Date().toISOString(); }
function sleep(ms) { return new Promise(res => setTimeout(res, ms)); }

class DeviceCollector {
  constructor(cfg) {
    this.cfg = cfg;
    this.client = new ModbusRTU();
    this.running = false;
    this.connected = false;
    this.loopPromise = null;
    this.backoff = this.cfg.BACKOFF_MIN_MS;
    this.lastError = null;

    this.buffer = {
      analog: {
        ts: null,
        values: Array.from({ length: this.cfg.ANALOG_CHANNELS }, () => null),
      },
      digital: {
        ts: null,
        values: Array.from({ length: this.cfg.DIGITAL_INPUT_COUNT }, () => null),
      }
    };
  }

  log(level, msg, meta) {
    const payload = meta ? ` ${JSON.stringify(meta)}` : '';
    // eslint-disable-next-line no-console
    console[level === 'error' ? 'error' : 'log'](`[${nowISO()}] [${level.toUpperCase()}] ${msg}${payload}`);
  }

  async connect() {
    if (this.connected) return;
    await new Promise((resolve, reject) => {
      try {
        this.client.connectTCP(this.cfg.MODBUS_HOST, { port: this.cfg.MODBUS_PORT }, (err) => {
          if (err) return reject(err);
          try {
            this.client.setID(this.cfg.MODBUS_ID);
            this.client.setTimeout(this.cfg.READ_TIMEOUT_MS);
            this.connected = true;
            this.log('info', 'Connected to Modbus TCP device', {
              host: this.cfg.MODBUS_HOST,
              port: this.cfg.MODBUS_PORT,
              unit_id: this.cfg.MODBUS_ID
            });
            resolve();
          } catch (e) {
            reject(e);
          }
        });
      } catch (e) {
        reject(e);
      }
    });
  }

  async disconnect() {
    try {
      if (this.client && this.client.isOpen) {
        await new Promise((resolve) => this.client.close(resolve));
      }
    } catch (e) {
      // ignore
    } finally {
      this.connected = false;
    }
  }

  async start() {
    if (this.running) return;
    this.running = true;
    this.loopPromise = this._runLoop();
  }

  async stop() {
    this.running = false;
    try {
      if (this.loopPromise) await this.loopPromise;
    } catch (e) {
      // ignore
    }
    await this.disconnect();
  }

  async _pollOnce() {
    if (!this.connected) return;

    // Read analog inputs
    try {
      let regRes;
      if (this.cfg.ANALOG_REG_TYPE === 'INPUT') {
        regRes = await this.client.readInputRegisters(this.cfg.ANALOG_REG_START, this.cfg.ANALOG_CHANNELS);
      } else {
        regRes = await this.client.readHoldingRegisters(this.cfg.ANALOG_REG_START, this.cfg.ANALOG_CHANNELS);
      }

      const raw = regRes.data || [];
      const scaled = raw.map((v, idx) => v * this.cfg.ANALOG_SCALE[idx] + this.cfg.ANALOG_OFFSET[idx]);
      this.buffer.analog.values = scaled;
      this.buffer.analog.ts = nowISO();
    } catch (e) {
      throw new Error(`Analog read failed: ${e.message || e}`);
    }

    // Read digital inputs (best-effort, do not fail overall if error)
    try {
      if (this.cfg.DIGITAL_INPUT_COUNT > 0) {
        const di = await this.client.readDiscreteInputs(this.cfg.DIGITAL_INPUT_START, this.cfg.DIGITAL_INPUT_COUNT);
        const vals = (di.data || []).map(b => (b ? 1 : 0));
        this.buffer.digital.values = vals;
        this.buffer.digital.ts = nowISO();
      }
    } catch (e) {
      this.log('warn', 'Digital input read failed', { error: String(e) });
    }
  }

  async _runLoop() {
    this.log('info', 'Collector loop starting', { poll_ms: this.cfg.POLL_INTERVAL_MS });
    while (this.running) {
      if (!this.connected) {
        try {
          await this.connect();
          this.backoff = this.cfg.BACKOFF_MIN_MS;
        } catch (e) {
          this.lastError = String(e);
          this.log('error', 'Connect failed, will retry', { error: this.lastError, backoff_ms: this.backoff });
          await sleep(this.backoff);
          this.backoff = Math.min(this.cfg.BACKOFF_MAX_MS, Math.max(this.cfg.BACKOFF_MIN_MS, Math.floor(this.backoff * 2)));
          continue;
        }
      }

      try {
        await this._pollOnce();
        await sleep(this.cfg.POLL_INTERVAL_MS);
      } catch (e) {
        this.lastError = String(e);
        this.log('error', 'Polling failed, disconnecting', { error: this.lastError });
        await this.disconnect();
        await sleep(this.backoff);
        this.backoff = Math.min(this.cfg.BACKOFF_MAX_MS, Math.max(this.cfg.BACKOFF_MIN_MS, Math.floor(this.backoff * 2)));
      }
    }
    this.log('info', 'Collector loop stopped');
  }

  getStatus() {
    return {
      device_reachable: this.connected,
      online: this.running,
      last_update_ts: this.buffer.analog.ts,
      analog_channels: this.cfg.ANALOG_CHANNELS,
      digital_input_count: this.cfg.DIGITAL_INPUT_COUNT,
      acquisition: {
        poll_interval_ms: this.cfg.POLL_INTERVAL_MS,
        read_timeout_ms: this.cfg.READ_TIMEOUT_MS,
        analog_reg_type: this.cfg.ANALOG_REG_TYPE,
        analog_reg_start: this.cfg.ANALOG_REG_START,
        digital_input_start: this.cfg.DIGITAL_INPUT_START
      },
      backoff_ms: this.backoff,
      modbus: {
        host: this.cfg.MODBUS_HOST,
        port: this.cfg.MODBUS_PORT,
        unit_id: this.cfg.MODBUS_ID
      }
    };
  }
}

const collector = new DeviceCollector(config);

function sendJson(res, code, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(code, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(body)
  });
  res.end(body);
}

const server = http.createServer(async (req, res) => {
  try {
    const u = new URL(req.url, `http://${req.headers.host}`);
    if (req.method !== 'GET') {
      return sendJson(res, 405, { error: 'method not allowed' });
    }

    if (u.pathname === '/inputs/analog') {
      const chStr = u.searchParams.get('channel_id');
      const buf = collector.buffer.analog;

      if (!buf.ts) {
        return sendJson(res, 503, { error: 'no data yet', reachable: collector.connected });
      }

      if (chStr !== null) {
        const ch = Number(chStr);
        if (!Number.isInteger(ch) || ch < 0 || ch >= config.ANALOG_CHANNELS) {
          return sendJson(res, 400, { error: 'invalid channel_id' });
        }
        const value = buf.values[ch];
        return sendJson(res, 200, {
          channel_id: ch,
          value,
          ts: buf.ts,
          reachable: collector.connected
        });
      }

      const channels = buf.values.map((v, idx) => ({ channel_id: idx, value: v }));
      return sendJson(res, 200, {
        channels,
        ts: buf.ts,
        reachable: collector.connected
      });
    }

    if (u.pathname === '/status') {
      return sendJson(res, 200, collector.getStatus());
    }

    return sendJson(res, 404, { error: 'not found' });
  } catch (e) {
    return sendJson(res, 500, { error: 'internal error', detail: String(e) });
  }
});

async function main() {
  process.on('SIGINT', async () => {
    console.log(`[${nowISO()}] [INFO] Caught SIGINT, shutting down...`);
    await shutdown();
  });
  process.on('SIGTERM', async () => {
    console.log(`[${nowISO()}] [INFO] Caught SIGTERM, shutting down...`);
    await shutdown();
  });

  await collector.start();

  server.listen(config.HTTP_PORT, config.HTTP_HOST, () => {
    console.log(`[${nowISO()}] [INFO] HTTP server listening on http://${config.HTTP_HOST}:${config.HTTP_PORT}`);
  });
}

let shuttingDown = false;
async function shutdown() {
  if (shuttingDown) return;
  shuttingDown = true;
  try {
    await collector.stop();
  } catch (e) {
    console.error(`[${nowISO()}] [ERROR] Error stopping collector: ${e}`);
  }
  server.close(() => {
    console.log(`[${nowISO()}] [INFO] HTTP server closed`);
    process.exit(0);
  });
  // Force exit if server does not close in time
  setTimeout(() => process.exit(0), 2000).unref();
}

main().catch(err => {
  console.error(`[${nowISO()}] [ERROR] Fatal: ${err && err.stack ? err.stack : err}`);
  process.exit(1);
});
