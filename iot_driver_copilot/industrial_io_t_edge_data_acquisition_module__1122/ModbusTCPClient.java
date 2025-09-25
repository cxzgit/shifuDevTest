import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.util.Arrays;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.logging.Level;
import java.util.logging.Logger;

public class ModbusTCPClient {
    private static final Logger LOGGER = Logger.getLogger("modbus");

    private final String host;
    private final int port;
    private final int unitId;
    private final int timeoutMs;

    private final AtomicInteger transactionId = new AtomicInteger(1);
    private final AtomicBoolean connected = new AtomicBoolean(false);

    private Socket socket;
    private InputStream in;
    private OutputStream out;

    public ModbusTCPClient(String host, int port, int unitId, int timeoutMs) {
        this.host = host;
        this.port = port;
        this.unitId = unitId;
        this.timeoutMs = timeoutMs;
    }

    public synchronized void connect() throws IOException {
        if (connected.get()) return;
        socket = new Socket();
        socket.setTcpNoDelay(true);
        socket.setKeepAlive(true);
        socket.connect(new InetSocketAddress(host, port), timeoutMs);
        socket.setSoTimeout(timeoutMs);
        in = socket.getInputStream();
        out = socket.getOutputStream();
        connected.set(true);
        LOGGER.info(Logging.json("event", "modbus_connected", "host", host, "port", String.valueOf(port)));
    }

    public synchronized void close() {
        connected.set(false);
        if (socket != null) {
            try { socket.close(); } catch (IOException ignored) {}
        }
        socket = null;
        in = null;
        out = null;
        LOGGER.info(Logging.json("event", "modbus_closed"));
    }

    public boolean isConnected() {
        return connected.get();
    }

    private synchronized byte[] sendReceive(byte function, byte[] pduPayload) throws IOException {
        if (!connected.get()) throw new IOException("Not connected");
        int tid = transactionId.getAndUpdate(x -> (x + 1) & 0xFFFF);
        if (tid == 0) tid = 1;
        int pduLen = 1 + (pduPayload == null ? 0 : pduPayload.length);
        int lenField = 1 + pduLen; // unitId + PDU

        byte[] header = new byte[7];
        header[0] = (byte) ((tid >> 8) & 0xFF);
        header[1] = (byte) (tid & 0xFF);
        header[2] = 0; // protocol id hi
        header[3] = 0; // protocol id lo
        header[4] = (byte) ((lenField >> 8) & 0xFF);
        header[5] = (byte) (lenField & 0xFF);
        header[6] = (byte) (unitId & 0xFF);

        byte[] pdu = new byte[pduLen];
        pdu[0] = function;
        if (pduPayload != null) System.arraycopy(pduPayload, 0, pdu, 1, pduPayload.length);

        out.write(header);
        out.write(pdu);
        out.flush();

        // Read MBAP
        byte[] respHeader = readFully(7);
        int respLenField = ((respHeader[4] & 0xFF) << 8) | (respHeader[5] & 0xFF);
        if (respLenField < 2) throw new IOException("Invalid MBAP length");
        byte[] respRest = readFully(respLenField - 1); // minus unitId
        byte respFn = respRest[0];
        if ((respFn & 0x80) != 0) {
            int exCode = respRest[1] & 0xFF;
            throw new IOException("Modbus exception code=" + exCode);
        }
        if (respFn != function) throw new IOException("Function mismatch");
        return Arrays.copyOfRange(respRest, 1, respRest.length);
    }

    private byte[] readFully(int len) throws IOException {
        byte[] buf = new byte[len];
        int off = 0;
        while (off < len) {
            int r = in.read(buf, off, len - off);
            if (r < 0) throw new IOException("Stream closed");
            off += r;
        }
        return buf;
    }

    public synchronized boolean[] readCoils(int startAddress, int count) throws IOException {
        byte[] payload = new byte[4];
        payload[0] = (byte) ((startAddress >> 8) & 0xFF);
        payload[1] = (byte) (startAddress & 0xFF);
        payload[2] = (byte) ((count >> 8) & 0xFF);
        payload[3] = (byte) (count & 0xFF);
        byte[] resp = sendReceive((byte) 0x01, payload);
        int byteCount = resp[0] & 0xFF;
        boolean[] vals = new boolean[count];
        for (int i = 0; i < count; i++) {
            int b = resp[1 + (i / 8)] & 0xFF;
            vals[i] = ((b >> (i % 8)) & 0x01) != 0;
        }
        return vals;
    }

    public synchronized boolean[] readDiscreteInputs(int startAddress, int count) throws IOException {
        byte[] payload = new byte[4];
        payload[0] = (byte) ((startAddress >> 8) & 0xFF);
        payload[1] = (byte) (startAddress & 0xFF);
        payload[2] = (byte) ((count >> 8) & 0xFF);
        payload[3] = (byte) (count & 0xFF);
        byte[] resp = sendReceive((byte) 0x02, payload);
        int byteCount = resp[0] & 0xFF;
        boolean[] vals = new boolean[count];
        for (int i = 0; i < count; i++) {
            int b = resp[1 + (i / 8)] & 0xFF;
            vals[i] = ((b >> (i % 8)) & 0x01) != 0;
        }
        return vals;
    }

    public synchronized int[] readHoldingRegisters(int startAddress, int count) throws IOException {
        byte[] payload = new byte[4];
        payload[0] = (byte) ((startAddress >> 8) & 0xFF);
        payload[1] = (byte) (startAddress & 0xFF);
        payload[2] = (byte) ((count >> 8) & 0xFF);
        payload[3] = (byte) (count & 0xFF);
        byte[] resp = sendReceive((byte) 0x03, payload);
        int byteCount = resp[0] & 0xFF;
        int[] regs = new int[count];
        for (int i = 0; i < count; i++) {
            int hi = resp[1 + 2 * i] & 0xFF;
            int lo = resp[1 + 2 * i + 1] & 0xFF;
            regs[i] = (hi << 8) | lo;
        }
        return regs;
    }

    public synchronized int[] readInputRegisters(int startAddress, int count) throws IOException {
        byte[] payload = new byte[4];
        payload[0] = (byte) ((startAddress >> 8) & 0xFF);
        payload[1] = (byte) (startAddress & 0xFF);
        payload[2] = (byte) ((count >> 8) & 0xFF);
        payload[3] = (byte) (count & 0xFF);
        byte[] resp = sendReceive((byte) 0x04, payload);
        int byteCount = resp[0] & 0xFF;
        int[] regs = new int[count];
        for (int i = 0; i < count; i++) {
            int hi = resp[1 + 2 * i] & 0xFF;
            int lo = resp[1 + 2 * i + 1] & 0xFF;
            regs[i] = (hi << 8) | lo;
        }
        return regs;
    }

    public synchronized void writeSingleCoil(int address, boolean value) throws IOException {
        byte[] payload = new byte[4];
        payload[0] = (byte) ((address >> 8) & 0xFF);
        payload[1] = (byte) (address & 0xFF);
        int v = value ? 0xFF00 : 0x0000;
        payload[2] = (byte) ((v >> 8) & 0xFF);
        payload[3] = (byte) (v & 0xFF);
        sendReceive((byte) 0x05, payload);
    }

    public synchronized void writeSingleRegister(int address, int value) throws IOException {
        byte[] payload = new byte[4];
        payload[0] = (byte) ((address >> 8) & 0xFF);
        payload[1] = (byte) (address & 0xFF);
        payload[2] = (byte) ((value >> 8) & 0xFF);
        payload[3] = (byte) (value & 0xFF);
        sendReceive((byte) 0x06, payload);
    }
}
