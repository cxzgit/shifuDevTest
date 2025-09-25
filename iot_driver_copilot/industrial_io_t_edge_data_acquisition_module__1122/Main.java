import java.io.IOException;
import java.util.logging.Level;
import java.util.logging.Logger;

public class Main {
    private static final Logger LOGGER = Logger.getLogger("driver");

    public static void main(String[] args) {
        Logging.init();
        Config config;
        try {
            config = Config.loadFromEnv();
        } catch (IllegalArgumentException e) {
            LOGGER.log(Level.SEVERE, "Configuration error: {0}", e.getMessage());
            System.exit(1);
            return;
        }

        ModbusTCPClient modbusClient = new ModbusTCPClient(
                config.modbusTcpHost,
                config.modbusTcpPort,
                config.modbusUnitId,
                config.modbusTimeoutMs
        );

        AcquisitionService acquisition = new AcquisitionService(modbusClient, config);
        MQTTClient mqttClient = null;
        if (config.mqttEnabled()) {
            mqttClient = new MQTTClient(
                    config.mqttHost,
                    config.mqttPort,
                    config.mqttClientId,
                    config.mqttUsername,
                    config.mqttPassword,
                    config.mqttKeepAliveSec
            );
        }

        HttpServerApp httpServer;
        try {
            httpServer = new HttpServerApp(config, acquisition, mqttClient);
        } catch (IOException e) {
            LOGGER.log(Level.SEVERE, "Failed to start HTTP server: {0}", e.getMessage());
            System.exit(1);
            return;
        }

        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            LOGGER.info(Logging.json("event", "shutdown", "msg", "Shutting down"));
            try {
                httpServer.stop();
            } catch (Exception ignored) {}
            try {
                acquisition.stop();
            } catch (Exception ignored) {}
            try {
                modbusClient.close();
            } catch (Exception ignored) {}
            try {
                if (mqttClient != null) mqttClient.close();
            } catch (Exception ignored) {}
        }));

        // Start services
        if (config.mqttEnabled()) {
            mqttClient.start();
        }
        acquisition.start();
        httpServer.start();

        LOGGER.info(Logging.json("event", "driver_started",
                "http_host", config.httpHost,
                "http_port", String.valueOf(config.httpPort),
                "modbus_host", config.modbusTcpHost,
                "modbus_port", String.valueOf(config.modbusTcpPort),
                "acq_interval_ms", String.valueOf(config.acqIntervalMs)));
    }
}
