import java.io.*;
import java.time.Instant;
import java.util.Arrays;

class ModbusRTU implements Closeable {
    private final String devicePath;
    private FileInputStream in;
    private FileOutputStream out;

    ModbusRTU(String devicePath) {
        this.devicePath = devicePath;
    }

    public synchronized void open() throws IOException {
        if (isOpen()) return;
        try {
            this.in = new FileInputStream(devicePath);
            this.out = new FileOutputStream(devicePath);
        } catch (FileNotFoundException e) {
            throw new IOException("Cannot open serial port: " + devicePath + ": " + e.getMessage(), e);
        }
    }

    public synchronized boolean isOpen() {
        return in != null && out != null;
    }

    public synchronized void close() {
        try { if (in != null) in.close(); } catch (Exception ignored) {}
        try { if (out != null) out.close(); } catch (Exception ignored) {}
        in = null; out = null;
    }

    public synchronized int[] readRegisters(int slaveId, int function, int startAddr, int count, int timeoutMs) throws IOException {
        if (!isOpen()) throw new IOException("serial not open");
        if (count <= 0 || count > 125) throw new IOException("invalid register count");

        // Build request ADU
        byte[] req = new byte[8];
        req[0] = (byte) (slaveId & 0xFF);
        req[1] = (byte) (function & 0xFF);
        req[2] = (byte) ((startAddr >> 8) & 0xFF);
        req[3] = (byte) (startAddr & 0xFF);
        req[4] = (byte) ((count >> 8) & 0xFF);
        req[5] = (byte) (count & 0xFF);
        int crc = crc16(req, 0, 6);
        req[6] = (byte) (crc & 0xFF);       // CRC Lo
        req[7] = (byte) ((crc >> 8) & 0xFF); // CRC Hi

        // Purge any stale bytes
        purgeInput();

        // send
        out.write(req);
        out.flush();

        // receive
        int expectedDataBytes = count * 2;
        int minLen = 3 + expectedDataBytes + 2; // addr + func + byteCount + data + crc
        byte[] buf = new byte[minLen];
        int read = 0;
        long deadline = System.currentTimeMillis() + timeoutMs;
        while (System.currentTimeMillis() < deadline && read < minLen) {
            int avail = 0;
            try { avail = in.available(); } catch (IOException ignored) {}
            if (avail <= 0) {
                try { Thread.sleep(5); } catch (InterruptedException ie) { Thread.currentThread().interrupt(); }
                continue;
            }
            int r = in.read(buf, read, Math.min(avail, minLen - read));
            if (r > 0) read += r;
        }

        if (read < minLen) {
            throw new IOException("timeout waiting for response ("+read+"/"+minLen+") at "+ Instant.now());
        }

        // Validate
        int p = 0;
        int respAddr = buf[p++] & 0xFF;
        int respFunc = buf[p++] & 0xFF;
        int byteCount = buf[p++] & 0xFF;
        if (respAddr != (slaveId & 0xFF)) throw new IOException("slave id mismatch resp="+respAddr);
        if (respFunc != (function & 0xFF)) throw new IOException("function mismatch resp="+respFunc);
        if (byteCount != expectedDataBytes) throw new IOException("byteCount mismatch resp="+byteCount+" expected="+expectedDataBytes);
        int dataEnd = 3 + byteCount;
        int crcResp = ((buf[dataEnd+1] & 0xFF) << 8) | (buf[dataEnd] & 0xFF);
        int crcCalc = crc16(buf, 0, dataEnd);
        if (crcResp != crcCalc) throw new IOException("CRC mismatch");

        int[] regs = new int[count];
        for (int i = 0; i < count; i++) {
            int hi = buf[3 + i*2] & 0xFF;
            int lo = buf[3 + i*2 + 1] & 0xFF;
            regs[i] = (hi << 8) | lo;
        }
        return regs;
    }

    private void purgeInput() {
        try {
            int avail = in.available();
            if (avail > 0) {
                byte[] tmp = new byte[Math.min(avail, 4096)];
                while (in.available() > 0) {
                    int r = in.read(tmp, 0, Math.min(tmp.length, in.available()));
                    if (r <= 0) break;
                }
            }
        } catch (IOException ignored) {}
    }

    static int crc16(byte[] data, int off, int len) {
        int crc = 0xFFFF;
        for (int i = off; i < off + len; i++) {
            crc ^= (data[i] & 0xFF);
            for (int j = 0; j < 8; j++) {
                if ((crc & 0x0001) != 0) {
                    crc >>= 1;
                    crc ^= 0xA001;
                } else {
                    crc >>= 1;
                }
            }
        }
        return crc & 0xFFFF;
    }
}
