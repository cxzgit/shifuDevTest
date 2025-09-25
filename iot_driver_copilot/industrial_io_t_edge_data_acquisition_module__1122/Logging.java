import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;

public class Logging {
    public static void init() {
        // Default java.util.logging outputs to stderr; leave default for simplicity
        System.setProperty("java.util.logging.SimpleFormatter.format",
                "%1$tFT%1$tT.%1$tLZ %4$s %2$s - %5$s%6$s%n");
    }

    public static String json(Object... kv) {
        Map<String, Object> map = new LinkedHashMap<>();
        for (int i = 0; i + 1 < kv.length; i += 2) {
            map.put(String.valueOf(kv[i]), kv[i + 1]);
        }
        map.put("ts", Instant.now().toString());
        StringBuilder sb = new StringBuilder();
        sb.append("{");
        boolean first = true;
        for (Map.Entry<String, Object> e : map.entrySet()) {
            if (!first) sb.append(",");
            first = false;
            sb.append("\"").append(escape(String.valueOf(e.getKey()))).append("\":");
            Object val = e.getValue();
            if (val == null) {
                sb.append("null");
            } else if (val instanceof Number || val instanceof Boolean) {
                sb.append(String.valueOf(val));
            } else {
                sb.append("\"").append(escape(String.valueOf(val))).append("\"");
            }
        }
        sb.append("}");
        return sb.toString();
    }

    private static String escape(String s) {
        return s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n");
    }
}
