import java.time.Instant;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicReference;
import java.util.logging.Level;
import java.util.logging.Logger;

public class AcquisitionService {
    private static final Logger LOGGER = Logger.getLogger("acquisition");

    private final ModbusTCPClient modbus;
    private final Config config;

    private final ExecutorService executor = Executors.newSingleThreadExecutor(r -> {
        Thread t = new Thread(r, "acquisition-loop");
        t.setDaemon(true);
        return t;
    });

    private final AtomicBoolean running = new AtomicBoolean(false);

    public static class Status {
        public boolean ethernetLinkUp;
        public boolean modbusTcpConnected;
        public boolean modbusRtuAvailable; // not implemented, will be false
        public boolean mqttConnected;
        public boolean acquisitionEnabled;
        public int acquisitionIntervalMs;
        public String lastError;
        public String lastUpdateTs;
    }

    private final AtomicReference<Status> statusRef = new AtomicReference<>(new Status());

    public AcquisitionService(ModbusTCPClient modbus, Config config) {
        this.modbus = modbus;
        this.config = config;
        Status s = new Status();
        s.acquisitionEnabled = true;
        s.acquisitionIntervalMs = config.acqIntervalMs;
        s.modbusRtuAvailable = false; // RS-485 not supported in this driver
        s.mqttConnected = false;
        s.modbusTcpConnected = false;
        s.ethernetLinkUp = false;
        s.lastUpdateTs = null;
        statusRef.set(s);
    }

    public void bindMqttState(boolean connected) {
        Status s = statusRef.get();
        Status n = copy(s);
        n.mqttConnected = connected;
        statusRef.set(n);
    }

    public void start() {
        if (running.compareAndSet(false, true)) {
            executor.submit(this::acquisitionLoop);
        }
    }

    public void stop() {
        running.set(false);
        executor.shutdownNow();
        try {
            executor.awaitTermination(3, TimeUnit.SECONDS);
        } catch (InterruptedException ignored) {}
    }

    public Status getStatus() {
        return statusRef.get();
    }

    private void acquisitionLoop() {
        int backoff = config.backoffInitialMs;
        while (running.get()) {
            try {
                if (!modbus.isConnected()) {
                    try {
                        modbus.connect();
                        backoff = config.backoffInitialMs; // reset on success
                        updateConnState(true, null);
                    } catch (Exception e) {
                        updateConnState(false, e.getMessage());
                        LOGGER.log(Level.WARNING, Logging.json("event", "modbus_connect_failed", "error", e.getMessage()));
                        sleep(backoff);
                        backoff = Math.min(backoff * 2, config.backoffMaxMs);
                        continue;
                    }
                }

                // Perform reads only if counts > 0
                if (config.aiCount > 0) {
                    try {
                        modbus.readInputRegisters(config.aiStart, config.aiCount);
                    } catch (Exception e) {
                        handleReadError(e);
                        continue; // will reconnect
                    }
                }
                if (config.diCount > 0) {
                    try {
                        modbus.readDiscreteInputs(config.diStart, config.diCount);
                    } catch (Exception e) {
                        handleReadError(e);
                        continue;
                    }
                }

                // Successful cycle
                Status s = statusRef.get();
                Status n = copy(s);
                n.lastUpdateTs = Instant.now().toString();
                n.lastError = null;
                statusRef.set(n);

                sleep(config.acqIntervalMs);
            } catch (Exception e) {
                LOGGER.log(Level.SEVERE, Logging.json("event", "acq_loop_error", "error", e.getMessage()));
                sleep(config.acqIntervalMs);
            }
        }
    }

    private void handleReadError(Exception e) {
        LOGGER.log(Level.WARNING, Logging.json("event", "modbus_read_error", "error", e.getMessage()));
        updateConnState(false, e.getMessage());
        try { modbus.close(); } catch (Exception ignored) {}
    }

    private void updateConnState(boolean connected, String lastError) {
        Status s = statusRef.get();
        Status n = copy(s);
        n.modbusTcpConnected = connected;
        n.ethernetLinkUp = connected; // assumes ethernet link if TCP connected
        n.lastError = lastError;
        statusRef.set(n);
    }

    private void sleep(int ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException ignored) {}
    }

    private Status copy(Status s) {
        Status n = new Status();
        n.ethernetLinkUp = s.ethernetLinkUp;
        n.modbusTcpConnected = s.modbusTcpConnected;
        n.modbusRtuAvailable = s.modbusRtuAvailable;
        n.mqttConnected = s.mqttConnected;
        n.acquisitionEnabled = s.acquisitionEnabled;
        n.acquisitionIntervalMs = s.acquisitionIntervalMs;
        n.lastError = s.lastError;
        n.lastUpdateTs = s.lastUpdateTs;
        return n;
    }
}
