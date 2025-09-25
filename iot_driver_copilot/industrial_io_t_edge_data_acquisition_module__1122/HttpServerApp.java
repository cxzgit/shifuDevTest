import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.util.logging.Logger;

public class HttpServerApp {
    private static final Logger LOGGER = Logger.getLogger("http");

    private final HttpServer server;
    private final Config config;
    private final AcquisitionService acquisition;
    private final MQTTClient mqttClient;

    public HttpServerApp(Config config, AcquisitionService acquisition, MQTTClient mqttClient) throws IOException {
        this.config = config;
        this.acquisition = acquisition;
        this.mqttClient = mqttClient;
        if (mqttClient != null) mqttClient.attachAcquisition(acquisition);
        server = HttpServer.create(new InetSocketAddress(config.httpHost, config.httpPort), 0);
        server.createContext("/status", new StatusHandler());
        server.setExecutor(null); // default
    }

    public void start() {
        server.start();
        LOGGER.info(Logging.json("event", "http_started", "host", config.httpHost, "port", String.valueOf(config.httpPort)));
    }

    public void stop() {
        server.stop(0);
        LOGGER.info(Logging.json("event", "http_stopped"));
    }

    private class StatusHandler implements HttpHandler {
        @Override
        public void handle(HttpExchange exchange) throws IOException {
            if (!"GET".equalsIgnoreCase(exchange.getRequestMethod())) {
                exchange.sendResponseHeaders(405, -1);
                return;
            }
            AcquisitionService.Status s = acquisition.getStatus();
            String json = buildStatusJson(s);
            byte[] bytes = json.getBytes();
            exchange.getResponseHeaders().add("Content-Type", "application/json; charset=utf-8");
            exchange.sendResponseHeaders(200, bytes.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(bytes);
            }
        }

        private String buildStatusJson(AcquisitionService.Status s) {
            StringBuilder sb = new StringBuilder();
            sb.append("{");
            appendJsonPair(sb, "ethernet_link", s.ethernetLinkUp ? "\"UP\"" : "\"DOWN\""); sb.append(",");
            appendJsonPair(sb, "modbus_tcp_available", String.valueOf(s.modbusTcpConnected)); sb.append(",");
            appendJsonPair(sb, "modbus_rtu_available", String.valueOf(s.modbusRtuAvailable)); sb.append(",");
            boolean mqttConn = mqttClient != null && mqttClient.isConnected();
            appendJsonPair(sb, "mqtt_connected", String.valueOf(mqttConn)); sb.append(",");
            appendJsonPair(sb, "acquisition_enabled", String.valueOf(s.acquisitionEnabled)); sb.append(",");
            appendJsonPair(sb, "acquisition_interval_ms", String.valueOf(s.acquisitionIntervalMs)); sb.append(",");
            appendJsonPair(sb, "last_error", s.lastError == null ? "null" : ("\"" + escape(s.lastError) + "\"")); sb.append(",");
            appendJsonPair(sb, "last_update_ts", s.lastUpdateTs == null ? "null" : ("\"" + escape(s.lastUpdateTs) + "\""));
            sb.append("}");
            return sb.toString();
        }

        private void appendJsonPair(StringBuilder sb, String key, String value) {
            sb.append("\"").append(escape(key)).append("\":").append(value);
        }

        private String escape(String s) {
            return s.replace("\\", "\\\\").replace("\"", "\\\"");
        }
    }
}
