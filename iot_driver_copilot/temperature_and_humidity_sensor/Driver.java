import com.sun.net.httpserver.Headers;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;

import java.io.*;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

public class Driver {
    private static final DateTimeFormatter ISO = DateTimeFormatter.ISO_INSTANT.withZone(ZoneOffset.UTC);

    public static void main(String[] args) throws Exception {
        Config config = Config.fromEnv();
        log("Starting HTTP server on " + config.httpHost + ":" + config.httpPort);

        Poller poller = new Poller(config);

        HttpServer server = HttpServer.create(new InetSocketAddress(config.httpHost, config.httpPort), 0);
        server.createContext("/config", new ConfigHandler(config, poller));
        server.createContext("/status", new StatusHandler(poller));
        server.createContext("/polling/start", new StartHandler(poller));
        server.createContext("/polling/stop", new StopHandler(poller));
        server.createContext("/readings/latest", new LatestHandler(poller));
        server.setExecutor(Executors.newCachedThreadPool());

        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            log("Shutdown initiated");
            try {
                poller.stop();
            } catch (Exception ignored) {}
            server.stop(0);
            log("Shutdown complete");
        }));

        server.start();
        log("HTTP server started");
    }

    static void writeJson(HttpExchange exchange, int status, String json) throws IOException {
        byte[] bytes = json.getBytes(StandardCharsets.UTF_8);
        Headers h = exchange.getResponseHeaders();
        h.set("Content-Type", "application/json; charset=utf-8");
        exchange.sendResponseHeaders(status, bytes.length);
        try (OutputStream os = exchange.getResponseBody()) {
            os.write(bytes);
        }
    }

    static void methodAllowedOr405(HttpExchange ex, String method) throws IOException {
        if (!ex.getRequestMethod().equalsIgnoreCase(method)) {
            writeJson(ex, 405, "{\"error\":\"method not allowed\"}");
        }
    }

    static String iso(Instant t) { return t == null ? null : ISO.format(t); }

    static void log(String msg) {
        System.out.println(ISO.format(Instant.now()) + " [driver] " + msg);
    }

    // Handlers
    static class ConfigHandler implements HttpHandler {
        private final Config config;
        private final Poller poller;
        ConfigHandler(Config config, Poller poller) { this.config = config; this.poller = poller; }
        @Override public void handle(HttpExchange exchange) throws IOException {
            String method = exchange.getRequestMethod();
            if (method.equalsIgnoreCase("GET")) {
                String json = "{"+
                        "\"serial_port\":\""+escape(config.serialPort)+"\","+
                        "\"slave_id\":"+config.slaveId+","+
                        "\"poll_interval_ms\":"+config.pollIntervalMs+","+
                        "\"baud\":9600,"+
                        "\"data_bits\":8,"+
                        "\"parity\":\"N\","+
                        "\"stop_bits\":1,"+
                        "\"function\":"+config.modbusFunction+","+
                        "\"humidity_register\":"+config.humidityRegister+","+
                        "\"temperature_register\":"+config.temperatureRegister+","+
                        "\"value_divisor\":"+config.valueDivisor+
                        "}";
                writeJson(exchange, 200, json);
                return;
            }
            if (method.equalsIgnoreCase("PUT")) {
                // Configuration is immutable at runtime per hard requirement (env-only). Validate and enforce.
                String body = new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
                Map<String, String> m = SimpleJson.pick(body,
                        new String[]{"serial_port","slave_id","poll_interval_ms"});
                boolean matches = true;
                StringBuilder diff = new StringBuilder();
                if (m.get("serial_port") != null && !m.get("serial_port").equals(config.serialPort)) {
                    matches = false; diff.append("serial_port");
                }
                if (m.get("slave_id") != null) {
                    try { if (Integer.parseInt(m.get("slave_id")) != config.slaveId) { matches = false; diff.append(diff.length()>0?",":"").append("slave_id"); } }
                    catch (Exception e) { matches = false; diff.append(diff.length()>0?",":"").append("slave_id"); }
                }
                if (m.get("poll_interval_ms") != null) {
                    try { if (Integer.parseInt(m.get("poll_interval_ms")) != config.pollIntervalMs) { matches = false; diff.append(diff.length()>0?",":"").append("poll_interval_ms"); } }
                    catch (Exception e) { matches = false; diff.append(diff.length()>0?",":"").append("poll_interval_ms"); }
                }
                if (matches) {
                    writeJson(exchange, 200, "{\"message\":\"configuration unchanged\"}");
                } else {
                    writeJson(exchange, 409, "{\"error\":\"configuration is immutable; set environment variables and restart\",\"diff\":\""+escape(diff.toString())+"\"}");
                }
                return;
            }
            writeJson(exchange, 405, "{\"error\":\"method not allowed\"}");
        }
    }

    static class StatusHandler implements HttpHandler {
        private final Poller poller;
        StatusHandler(Poller p){ this.poller = p; }
        @Override public void handle(HttpExchange exchange) throws IOException {
            methodAllowedOr405(exchange, "GET");
            Poller.Status s = poller.getStatus();
            String lastErrMsg = s.lastErrorMessage.get();
            String json = "{"+
                    "\"polling\":"+s.polling.get()+","+
                    "\"connected\":"+s.connected.get()+","+
                    "\"last_poll_time\":"+(s.lastPollTime.get()==0?nullJson():quote(iso(Instant.ofEpochMilli(s.lastPollTime.get()))))+","+
                    "\"last_success_time\":"+(s.lastSuccessTime.get()==0?nullJson():quote(iso(Instant.ofEpochMilli(s.lastSuccessTime.get()))))+","+
                    "\"last_error\":"+(lastErrMsg==null?nullJson():quote(lastErrMsg))+","+
                    "\"timeout_count\":"+s.timeoutCount.get()+","+
                    "\"consecutive_timeouts\":"+s.consecutiveTimeouts.get()+
                    "}";
            writeJson(exchange, 200, json);
        }
    }

    static class StartHandler implements HttpHandler {
        private final Poller poller;
        StartHandler(Poller p){ this.poller = p; }
        @Override public void handle(HttpExchange exchange) throws IOException {
            methodAllowedOr405(exchange, "POST");
            boolean started = poller.start();
            writeJson(exchange, 200, started?"{\"message\":\"polling started\"}":"{\"message\":\"polling already running\"}");
        }
    }

    static class StopHandler implements HttpHandler {
        private final Poller poller;
        StopHandler(Poller p){ this.poller = p; }
        @Override public void handle(HttpExchange exchange) throws IOException {
            methodAllowedOr405(exchange, "POST");
            boolean stopped = poller.stop();
            writeJson(exchange, 200, stopped?"{\"message\":\"polling stopped\"}":"{\"message\":\"polling already stopped\"}");
        }
    }

    static class LatestHandler implements HttpHandler {
        private final Poller poller;
        LatestHandler(Poller p){ this.poller = p; }
        @Override public void handle(HttpExchange exchange) throws IOException {
            methodAllowedOr405(exchange, "GET");
            Poller.LatestReading r = poller.getLatest();
            if (r == null || r.timestampEpochMs == 0) {
                writeJson(exchange, 200, "{\"available\":false}");
                return;
            }
            boolean stale = poller.isStale(r.timestampEpochMs);
            String json = "{"+
                    "\"available\":true,"+
                    "\"temperature_c\":"+String.format(java.util.Locale.US, "%.3f", r.temperatureC)+","+
                    "\"relative_humidity\":"+String.format(java.util.Locale.US, "%.3f", r.humidity)+","+
                    "\"timestamp\":\""+iso(Instant.ofEpochMilli(r.timestampEpochMs))+"\","+
                    "\"stale\":"+stale+
                    "}";
            writeJson(exchange, 200, json);
        }
    }

    static String quote(String s){ return s == null ? nullJson() : ("\""+escape(s)+"\""); }
    static String nullJson(){ return "null"; }
    static String escape(String s){ return s.replace("\\","\\\\").replace("\"","\\\""); }
}
