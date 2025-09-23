import java.util.HashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

class SimpleJson {
    private static final Pattern STRING_PROP = Pattern.compile("\"([^"]+)\"\s*:\s*\"([^\"]*)\"");
    private static final Pattern NUMBER_PROP = Pattern.compile("\"([^"]+)\"\s*:\s*([-]?[0-9]+)");

    public static Map<String,String> pick(String json, String[] keys) {
        Map<String,String> out = new HashMap<>();
        if (json == null) return out;
        Matcher m1 = STRING_PROP.matcher(json);
        while (m1.find()) {
            String k = m1.group(1);
            String v = m1.group(2);
            for (String want : keys) if (want.equals(k)) out.put(k, v);
        }
        Matcher m2 = NUMBER_PROP.matcher(json);
        while (m2.find()) {
            String k = m2.group(1);
            String v = m2.group(2);
            for (String want : keys) if (want.equals(k)) out.put(k, v);
        }
        return out;
    }
}
