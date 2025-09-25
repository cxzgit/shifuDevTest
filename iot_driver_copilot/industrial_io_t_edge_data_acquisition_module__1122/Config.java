import java.util.Map;
import java.util.UUID;

public class Config {
    public final String httpHost;
    public final int httpPort;

    public final String modbusTcpHost;
    public final int modbusTcpPort;
    public final int modbusUnitId;
    public final int modbusTimeoutMs;

    public final int acqIntervalMs;

    public final int backoffInitialMs;
    public final int backoffMaxMs;

    public final int aiStart;
    public final int aiCount;
    public final int diStart;
    public final int diCount;

    public final String mqttHost;
    public final int mqttPort;
    public final String mqttClientId;
    public final String mqttUsername;
    public final String mqttPassword;
    public final int mqttKeepAliveSec;

    private Config(String httpHost,
                   int httpPort,
                   String modbusTcpHost,
                   int modbusTcpPort,
                   int modbusUnitId,
                   int modbusTimeoutMs,
                   int acqIntervalMs,
                   int backoffInitialMs,
                   int backoffMaxMs,
                   int aiStart,
                   int aiCount,
                   int diStart,
                   int diCount,
                   String mqttHost,
                   int mqttPort,
                   String mqttClientId,
                   String mqttUsername,
                   String mqttPassword,
                   int mqttKeepAliveSec) {
        this.httpHost = httpHost;
        this.httpPort = httpPort;
        this.modbusTcpHost = modbusTcpHost;
        this.modbusTcpPort = modbusTcpPort;
        this.modbusUnitId = modbusUnitId;
        this.modbusTimeoutMs = modbusTimeoutMs;
        this.acqIntervalMs = acqIntervalMs;
        this.backoffInitialMs = backoffInitialMs;
        this.backoffMaxMs = backoffMaxMs;
        this.aiStart = aiStart;
        this.aiCount = aiCount;
        this.diStart = diStart;
        this.diCount = diCount;
        this.mqttHost = mqttHost;
        this.mqttPort = mqttPort;
        this.mqttClientId = mqttClientId;
        this.mqttUsername = mqttUsername;
        this.mqttPassword = mqttPassword;
        this.mqttKeepAliveSec = mqttKeepAliveSec;
    }

    public static Config loadFromEnv() {
        Map<String, String> env = System.getenv();

        String httpHost = require(env, "HTTP_HOST");
        int httpPort = parseInt(require(env, "HTTP_PORT"), "HTTP_PORT");

        String modbusHost = require(env, "MODBUS_TCP_HOST");
        int modbusPort = parseInt(require(env, "MODBUS_TCP_PORT"), "MODBUS_TCP_PORT");
        int unitId = parseInt(require(env, "MODBUS_UNIT_ID"), "MODBUS_UNIT_ID");
        int modbusTimeoutMs = parseInt(require(env, "MODBUS_TIMEOUT_MS"), "MODBUS_TIMEOUT_MS");

        int acqIntervalMs = parseInt(require(env, "ACQ_INTERVAL_MS"), "ACQ_INTERVAL_MS");
        if (acqIntervalMs <= 0 || acqIntervalMs > 1000) {
            throw new IllegalArgumentException("ACQ_INTERVAL_MS must be >0 and <=1000");
        }

        int backoffInitialMs = parseInt(require(env, "BACKOFF_INITIAL_MS"), "BACKOFF_INITIAL_MS");
        int backoffMaxMs = parseInt(require(env, "BACKOFF_MAX_MS"), "BACKOFF_MAX_MS");
        if (backoffInitialMs <= 0 || backoffMaxMs < backoffInitialMs) {
            throw new IllegalArgumentException("Invalid backoff configuration");
        }

        int aiStart = parseInt(require(env, "MODBUS_READ_AI_START"), "MODBUS_READ_AI_START");
        int aiCount = parseInt(require(env, "MODBUS_READ_AI_COUNT"), "MODBUS_READ_AI_COUNT");
        int diStart = parseInt(require(env, "MODBUS_READ_DI_START"), "MODBUS_READ_DI_START");
        int diCount = parseInt(require(env, "MODBUS_READ_DI_COUNT"), "MODBUS_READ_DI_COUNT");

        // MQTT optional: if MQTT_HOST not set, disable
        String mqttHost = env.get("MQTT_HOST");
        int mqttPort = 0;
        String mqttClientId = null;
        String mqttUsername = null;
        String mqttPassword = null;
        int mqttKeepAliveSec = 0;
        if (mqttHost != null && !mqttHost.isEmpty()) {
            mqttPort = parseInt(require(env, "MQTT_PORT"), "MQTT_PORT");
            mqttClientId = require(env, "MQTT_CLIENT_ID");
            mqttUsername = env.get("MQTT_USERNAME");
            mqttPassword = env.get("MQTT_PASSWORD");
            mqttKeepAliveSec = parseInt(require(env, "MQTT_KEEPALIVE_SEC"), "MQTT_KEEPALIVE_SEC");
        }

        return new Config(httpHost, httpPort, modbusHost, modbusPort, unitId, modbusTimeoutMs, acqIntervalMs,
                backoffInitialMs, backoffMaxMs, aiStart, aiCount, diStart, diCount,
                mqttHost, mqttPort, mqttClientId, mqttUsername, mqttPassword, mqttKeepAliveSec);
    }

    private static String require(Map<String, String> env, String key) {
        String v = env.get(key);
        if (v == null || v.isEmpty()) {
            throw new IllegalArgumentException("Missing required env: " + key);
        }
        return v;
    }

    private static int parseInt(String v, String key) {
        try {
            return Integer.parseInt(v.trim());
        } catch (NumberFormatException e) {
            throw new IllegalArgumentException("Invalid integer for env " + key + ": " + v);
        }
    }

    public boolean mqttEnabled() {
        return mqttHost != null && !mqttHost.isEmpty();
    }
}
