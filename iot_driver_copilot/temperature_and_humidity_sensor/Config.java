import java.util.Locale;

class Config {
    public final String httpHost;
    public final int httpPort;

    public final String serialPort;
    public final int slaveId;
    public final int pollIntervalMs;

    public final int modbusFunction; // 3=holding,4=input
    public final int humidityRegister;
    public final int temperatureRegister;
    public final int valueDivisor; // e.g., 10 => raw/10.0

    public final int requestTimeoutMs;
    public final int backoffInitialMs;
    public final int backoffMaxMs;

    private Config(String httpHost, int httpPort, String serialPort, int slaveId, int pollIntervalMs,
                   int modbusFunction, int humidityRegister, int temperatureRegister, int valueDivisor,
                   int requestTimeoutMs, int backoffInitialMs, int backoffMaxMs) {
        this.httpHost = httpHost;
        this.httpPort = httpPort;
        this.serialPort = serialPort;
        this.slaveId = slaveId;
        this.pollIntervalMs = pollIntervalMs;
        this.modbusFunction = modbusFunction;
        this.humidityRegister = humidityRegister;
        this.temperatureRegister = temperatureRegister;
        this.valueDivisor = valueDivisor;
        this.requestTimeoutMs = requestTimeoutMs;
        this.backoffInitialMs = backoffInitialMs;
        this.backoffMaxMs = backoffMaxMs;
    }

    public static Config fromEnv() {
        String httpHost = mustEnv("HTTP_HOST");
        int httpPort = mustInt("HTTP_PORT");
        String serialPort = mustEnv("SERIAL_PORT");
        int slaveId = mustIntInRange("MODBUS_SLAVE_ID", 1, 247);
        int pollIntervalMs = mustIntMin("POLL_INTERVAL_MS", 100);
        int modbusFunction = mustIntIn("MODBUS_FUNCTION", new int[]{3,4});
        int humidityRegister = mustIntInRange("HUMIDITY_REGISTER", 0, 65535);
        int temperatureRegister = mustIntInRange("TEMPERATURE_REGISTER", 0, 65535);
        int valueDivisor = mustIntMin("VALUE_DIVISOR", 1);
        int requestTimeoutMs = mustIntMin("SERIAL_TIMEOUT_MS", 50);
        int backoffInitialMs = mustIntMin("BACKOFF_INITIAL_MS", 50);
        int backoffMaxMs = mustIntMin("BACKOFF_MAX_MS", 100);
        return new Config(httpHost, httpPort, serialPort, slaveId, pollIntervalMs,
                modbusFunction, humidityRegister, temperatureRegister, valueDivisor,
                requestTimeoutMs, backoffInitialMs, backoffMaxMs);
    }

    private static String mustEnv(String key) {
        String v = System.getenv(key);
        if (v == null || v.isEmpty()) throw new IllegalArgumentException("Missing env " + key);
        return v;
    }

    private static int mustInt(String key) {
        String v = mustEnv(key);
        try { return Integer.parseInt(v); } catch (Exception e) { throw new IllegalArgumentException("Invalid int for env "+key+": "+v); }
    }

    private static int mustIntMin(String key, int min) {
        int val = mustInt(key);
        if (val < min) throw new IllegalArgumentException(String.format(Locale.US, "Env %s must be >= %d", key, min));
        return val;
    }

    private static int mustIntInRange(String key, int min, int max) {
        int val = mustInt(key);
        if (val < min || val > max) throw new IllegalArgumentException(String.format(Locale.US, "Env %s must be between %d and %d", key, min, max));
        return val;
    }

    private static int mustIntIn(String key, int[] allowed) {
        int val = mustInt(key);
        for (int a : allowed) if (a == val) return val;
        throw new IllegalArgumentException("Env "+key+" must be one of "+java.util.Arrays.toString(allowed));
    }
}
