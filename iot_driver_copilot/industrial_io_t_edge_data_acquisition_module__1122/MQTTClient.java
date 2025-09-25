import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.logging.Level;
import java.util.logging.Logger;

public class MQTTClient {
    private static final Logger LOGGER = Logger.getLogger("mqtt");

    private final String host;
    private final int port;
    private final String clientId;
    private final String username;
    private final String password;
    private final int keepAliveSec;

    private Socket socket;
    private InputStream in;
    private OutputStream out;

    private final AtomicBoolean running = new AtomicBoolean(false);
    private final AtomicBoolean connected = new AtomicBoolean(false);

    private final ExecutorService executor = Executors.newSingleThreadExecutor(r -> {
        Thread t = new Thread(r, "mqtt-loop");
        t.setDaemon(true);
        return t;
    });

    private AcquisitionService acquisitionService;

    public MQTTClient(String host, int port, String clientId, String username, String password, int keepAliveSec) {
        this.host = host;
        this.port = port;
        this.clientId = clientId;
        this.username = username;
        this.password = password;
        this.keepAliveSec = keepAliveSec;
    }

    public void attachAcquisition(AcquisitionService svc) {
        this.acquisitionService = svc;
    }

    public void start() {
        if (running.compareAndSet(false, true)) {
            executor.submit(this::loop);
        }
    }

    public void close() {
        running.set(false);
        try { disconnect(); } catch (Exception ignored) {}
        executor.shutdownNow();
        try { executor.awaitTermination(2, TimeUnit.SECONDS); } catch (InterruptedException ignored) {}
    }

    public boolean isConnected() { return connected.get(); }

    private void loop() {
        int backoffMs = 500;
        while (running.get()) {
            try {
                if (!connected.get()) {
                    connect();
                    backoffMs = 500;
                }
                if (keepAliveSec > 0) {
                    // Send periodic PINGREQ
                    sendPingReq();
                    sleep(Math.max(1000, (keepAliveSec * 1000) / 2));
                } else {
                    sleep(1000);
                }
            } catch (Exception e) {
                LOGGER.log(Level.WARNING, Logging.json("event", "mqtt_error", "error", e.getMessage()));
                connected.set(false);
                if (acquisitionService != null) acquisitionService.bindMqttState(false);
                try { disconnect(); } catch (Exception ignored) {}
                sleep(backoffMs);
                backoffMs = Math.min(backoffMs * 2, 5000);
            }
        }
    }

    private void connect() throws IOException {
        socket = new Socket();
        socket.connect(new InetSocketAddress(host, port), 3000);
        socket.setSoTimeout(3000);
        in = socket.getInputStream();
        out = socket.getOutputStream();

        byte[] connectPacket = buildConnectPacket();
        out.write(connectPacket);
        out.flush();

        // Read CONNACK
        int type = in.read();
        if (type != 0x20) throw new IOException("Expected CONNACK (0x20), got: " + type);
        int remLen = readRemainingLength(in);
        byte[] payload = new byte[remLen];
        readFully(in, payload);
        int rc = payload[1] & 0xFF;
        if (rc != 0x00) throw new IOException("MQTT ConnAck return code=" + rc);
        connected.set(true);
        LOGGER.info(Logging.json("event", "mqtt_connected", "host", host, "port", String.valueOf(port), "client_id", clientId));
        if (acquisitionService != null) acquisitionService.bindMqttState(true);
    }

    private void disconnect() throws IOException {
        if (socket != null && !socket.isClosed()) {
            try {
                out.write(new byte[]{(byte) 0xE0, 0x00}); // DISCONNECT
                out.flush();
            } catch (Exception ignored) {}
            try { socket.close(); } catch (Exception ignored) {}
        }
        connected.set(false);
        LOGGER.info(Logging.json("event", "mqtt_closed"));
    }

    private void sendPingReq() throws IOException {
        if (!connected.get()) return;
        out.write(new byte[]{(byte) 0xC0, 0x00});
        out.flush();
        // PINGRESP (might be ignored)
        int type = in.read();
        if (type != 0xD0) {
            throw new IOException("Expected PINGRESP (0xD0), got: " + type);
        }
        int remLen = readRemainingLength(in);
        for (int i = 0; i < remLen; i++) in.read();
    }

    private byte[] buildConnectPacket() {
        byte[] protoName = encodeString("MQTT");
        byte protoLevel = 0x04; // 3.1.1
        byte connectFlags = 0x00;
        if (username != null && !username.isEmpty()) connectFlags |= 0x80;
        if (password != null && !password.isEmpty()) connectFlags |= 0x40;
        connectFlags |= 0x02; // Clean Session
        byte[] keepAlive = new byte[]{(byte) ((keepAliveSec >> 8) & 0xFF), (byte) (keepAliveSec & 0xFF)};
        byte[] clientIdBytes = encodeString(clientId);

        int varHeaderLen = protoName.length + 1 + 1 + 2; // ProtocolName + Level + Flags + KeepAlive
        int payloadLen = clientIdBytes.length
                + (username != null && !username.isEmpty() ? encodeString(username).length : 0)
                + (password != null && !password.isEmpty() ? encodeString(password).length : 0);
        int remainingLength = varHeaderLen + payloadLen;

        byte[] fixedHeader = buildFixedHeader((byte) 0x10, remainingLength);
        byte[] packet = new byte[fixedHeader.length + remainingLength];
        int p = 0;
        System.arraycopy(fixedHeader, 0, packet, p, fixedHeader.length); p += fixedHeader.length;
        // Variable header
        System.arraycopy(protoName, 0, packet, p, protoName.length); p += protoName.length;
        packet[p++] = protoLevel;
        packet[p++] = connectFlags;
        System.arraycopy(keepAlive, 0, packet, p, keepAlive.length); p += keepAlive.length;
        // Payload
        System.arraycopy(clientIdBytes, 0, packet, p, clientIdBytes.length); p += clientIdBytes.length;
        if (username != null && !username.isEmpty()) {
            byte[] u = encodeString(username);
            System.arraycopy(u, 0, packet, p, u.length); p += u.length;
        }
        if (password != null && !password.isEmpty()) {
            byte[] pwd = encodeString(password);
            System.arraycopy(pwd, 0, packet, p, pwd.length); p += pwd.length;
        }
        return packet;
    }

    private static byte[] buildFixedHeader(byte type, int remainingLength) {
        int remLenBytesCount = 0;
        int x = remainingLength;
        byte[] remLenBytes = new byte[4];
        do {
            int encodedByte = x % 128;
            x = x / 128;
            if (x > 0) encodedByte |= 128;
            remLenBytes[remLenBytesCount++] = (byte) encodedByte;
        } while (x > 0);
        byte[] header = new byte[1 + remLenBytesCount];
        header[0] = type;
        System.arraycopy(remLenBytes, 0, header, 1, remLenBytesCount);
        return header;
    }

    private static int readRemainingLength(InputStream in) throws IOException {
        int multiplier = 1;
        int value = 0;
        int encodedByte;
        do {
            encodedByte = in.read();
            if (encodedByte < 0) throw new IOException("EOF");
            value += (encodedByte & 127) * multiplier;
            multiplier *= 128;
        } while ((encodedByte & 128) != 0);
        return value;
    }

    private static byte[] encodeString(String s) {
        byte[] utf8 = s.getBytes(StandardCharsets.UTF_8);
        byte[] out = new byte[2 + utf8.length];
        out[0] = (byte) ((utf8.length >> 8) & 0xFF);
        out[1] = (byte) (utf8.length & 0xFF);
        System.arraycopy(utf8, 0, out, 2, utf8.length);
        return out;
    }

    private static void readFully(InputStream in, byte[] buf) throws IOException {
        int off = 0;
        while (off < buf.length) {
            int r = in.read(buf, off, buf.length - off);
            if (r < 0) throw new IOException("EOF");
            off += r;
        }
    }

    private void sleep(int ms) {
        try { Thread.sleep(ms); } catch (InterruptedException ignored) {}
    }
}
