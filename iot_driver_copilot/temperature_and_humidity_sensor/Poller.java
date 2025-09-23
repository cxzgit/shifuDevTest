import java.io.IOException;
import java.time.Instant;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;

class Poller {
    private final Config config;
    private final AtomicBoolean running = new AtomicBoolean(false);
    private Thread thread;
    private final AtomicReference<LatestReading> latest = new AtomicReference<>(null);
    private final Status status = new Status();

    Poller(Config config) {
        this.config = config;
    }

    boolean start() {
        if (running.get()) return false;
        running.set(true);
        status.polling.set(true);
        thread = new Thread(this::runLoop, "poller");
        thread.setDaemon(true);
        thread.start();
        Driver.log("Polling started");
        return true;
    }

    boolean stop() {
        if (!running.get()) return false;
        running.set(false);
        status.polling.set(false);
        try { if (thread != null) thread.join(2000); } catch (InterruptedException ignored) { Thread.currentThread().interrupt(); }
        Driver.log("Polling stopped");
        return true;
    }

    LatestReading getLatest() { return latest.get(); }

    boolean isStale(long tsMs) {
        long now = System.currentTimeMillis();
        long threshold = Math.max(config.pollIntervalMs * 3L, config.requestTimeoutMs * 3L);
        return now - tsMs > threshold;
    }

    Status getStatus() { return status; }

    private void runLoop() {
        int backoff = config.backoffInitialMs;
        try (ModbusRTU modbus = new ModbusRTU(config.serialPort)) {
            while (running.get()) {
                status.lastPollTime.set(System.currentTimeMillis());
                try {
                    if (!modbus.isOpen()) {
                        modbus.open();
                        status.connected.set(true);
                        Driver.log("Serial port opened: " + config.serialPort);
                    }

                    int startReg = Math.min(config.humidityRegister, config.temperatureRegister);
                    int endReg = Math.max(config.humidityRegister, config.temperatureRegister);
                    int count = (endReg - startReg) + 1;
                    int[] regs = modbus.readRegisters(config.slaveId, config.modbusFunction, startReg, count, config.requestTimeoutMs);

                    int humRaw = regs[config.humidityRegister - startReg];
                    int tempRaw = regs[config.temperatureRegister - startReg];

                    double hum = humRaw / (double) config.valueDivisor;
                    // temperature can be signed 16-bit
                    if ((tempRaw & 0x8000) != 0) tempRaw = tempRaw - 0x10000;
                    double temp = tempRaw / (double) config.valueDivisor;

                    LatestReading reading = new LatestReading();
                    reading.humidity = hum;
                    reading.temperatureC = temp;
                    reading.timestampEpochMs = System.currentTimeMillis();
                    latest.set(reading);

                    status.lastSuccessTime.set(reading.timestampEpochMs);
                    status.lastErrorMessage.set(null);
                    status.consecutiveTimeouts.set(0);
                    backoff = config.backoffInitialMs; // reset backoff after success

                    long elapsed = System.currentTimeMillis() - status.lastPollTime.get();
                    long sleep = config.pollIntervalMs - elapsed;
                    if (sleep > 0) sleepQuiet(sleep);
                } catch (IOException ex) {
                    status.lastErrorMessage.set(ex.getMessage());
                    Driver.log("Poll error: " + ex.getMessage());
                    status.timeoutCount.incrementAndGet();
                    status.consecutiveTimeouts.incrementAndGet();
                    status.connected.set(false);
                    // Attempt to close and reopen on next loop
                    try { modbus.close(); } catch (Exception ignored) {}
                    // Backoff
                    sleepQuiet(backoff);
                    backoff = Math.min(config.backoffMaxMs, backoff * 2);
                } catch (Exception ex) {
                    status.lastErrorMessage.set(ex.getMessage());
                    Driver.log("Unexpected error: " + ex);
                    status.connected.set(false);
                    try { modbus.close(); } catch (Exception ignored) {}
                    sleepQuiet(backoff);
                    backoff = Math.min(config.backoffMaxMs, backoff * 2);
                }
            }
        } catch (Exception e) {
            Driver.log("Fatal poller error: " + e.getMessage());
        }
    }

    private void sleepQuiet(long ms) {
        try { Thread.sleep(ms); } catch (InterruptedException ignored) { Thread.currentThread().interrupt(); }
    }

    static class LatestReading {
        double temperatureC;
        double humidity;
        long timestampEpochMs;
    }

    static class Status {
        final AtomicBoolean polling = new AtomicBoolean(false);
        final AtomicBoolean connected = new AtomicBoolean(false);
        final AtomicLong lastPollTime = new AtomicLong(0);
        final AtomicLong lastSuccessTime = new AtomicLong(0);
        final AtomicReference<String> lastErrorMessage = new AtomicReference<>(null);
        final AtomicInteger timeoutCount = new AtomicInteger(0);
        final AtomicInteger consecutiveTimeouts = new AtomicInteger(0);
    }
}
