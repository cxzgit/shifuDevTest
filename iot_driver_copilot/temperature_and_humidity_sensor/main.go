package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/host/v3"
	"periph.io/x/host/v3/i2c/i2creg"
)

const (
	pollInterval       = 2 * time.Second // requirement: periodic polling every 2 seconds
	defaultMeasDelayMs = 20              // wait after triggering measurement
)

type Config struct {
	HTTPHost          string
	HTTPPort          int
	I2CBus            string
	I2CAddress        uint16
	CalTempOffset     float64
	CalHumOffset      float64
	BackoffInitial    time.Duration
	BackoffMax        time.Duration
	MeasurementDelay  time.Duration
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseUint16Addr(s string) (uint16, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty address")
	}
	// allow hex with 0x prefix or decimal
	base := 0
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		base = 0
	}
	v, err := strconv.ParseUint(s, base, 16)
	if err != nil {
		return 0, err
	}
	if v > 0x7F {
		return 0, fmt.Errorf("i2c 7-bit address out of range: 0x%X", v)
	}
	return uint16(v), nil
}

func loadConfig() (Config, error) {
	var cfg Config
	cfg.HTTPHost = getenvDefault("HTTP_HOST", "0.0.0.0")
	portStr := getenvDefault("HTTP_PORT", "8080")
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return cfg, fmt.Errorf("invalid HTTP_PORT: %s", portStr)
	}
	cfg.HTTPPort = p

	cfg.I2CBus = getenvDefault("I2C_BUS", "1")
	addrStr := getenvDefault("I2C_ADDRESS", "0x40")
	addr, err := parseUint16Addr(addrStr)
	if err != nil {
		return cfg, fmt.Errorf("invalid I2C_ADDRESS: %v", err)
	}
	if addr != 0x40 && addr != 0x41 {
		return cfg, fmt.Errorf("I2C_ADDRESS must be 0x40 or 0x41, got 0x%X", addr)
	}
	cfg.I2CAddress = addr

	if v := os.Getenv("CAL_TEMP_OFFSET"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return cfg, fmt.Errorf("invalid CAL_TEMP_OFFSET: %v", err)
		}
		cfg.CalTempOffset = f
	}
	if v := os.Getenv("CAL_HUM_OFFSET"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return cfg, fmt.Errorf("invalid CAL_HUM_OFFSET: %v", err)
		}
		cfg.CalHumOffset = f
	}

	bi := getenvDefault("BACKOFF_INITIAL_MS", "500")
	biv, err := strconv.Atoi(bi)
	if err != nil || biv <= 0 {
		return cfg, fmt.Errorf("invalid BACKOFF_INITIAL_MS: %s", bi)
	}
	cfg.BackoffInitial = time.Duration(biv) * time.Millisecond
	bm := getenvDefault("BACKOFF_MAX_MS", "10000")
	bmv, err := strconv.Atoi(bm)
	if err != nil || bmv <= 0 {
		return cfg, fmt.Errorf("invalid BACKOFF_MAX_MS: %s", bm)
	}
	cfg.BackoffMax = time.Duration(bmv) * time.Millisecond

	md := getenvDefault("MEAS_DELAY_MS", fmt.Sprintf("%d", defaultMeasDelayMs))
	mdv, err := strconv.Atoi(md)
	if err != nil || mdv < 1 {
		return cfg, fmt.Errorf("invalid MEAS_DELAY_MS: %s", md)
	}
	cfg.MeasurementDelay = time.Duration(mdv) * time.Millisecond

	return cfg, nil
}

type Reading struct {
	TemperatureC float64   `json:"temperature_c"`
	HumidityRH   float64   `json:"humidity_rh"`
	Timestamp    time.Time `json:"timestamp"`
}

type Driver struct {
	bus   i2c.BusCloser
	busMu sync.Mutex // serializes I2C transactions

	mu           sync.RWMutex // protects below fields
	addr         uint16
	tempOffset   float64
	humOffset    float64
	lastReading  Reading
	connected    bool
	lastError    string
	lastChangeTS time.Time

	measDelay time.Duration
}

func NewDriver(bus i2c.BusCloser, addr uint16, tempOffset, humOffset float64, measDelay time.Duration) *Driver {
	return &Driver{bus: bus, addr: addr, tempOffset: tempOffset, humOffset: humOffset, measDelay: measDelay}
}

func (d *Driver) SetAddress(addr uint16) {
	d.mu.Lock()
	d.addr = addr
	d.mu.Unlock()
	log.Printf("I2C address set to 0x%02X", addr)
}

func (d *Driver) GetAddress() uint16 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.addr
}

func (d *Driver) hexAddress() string {
	return fmt.Sprintf("0x%02X", d.GetAddress())
}

func (d *Driver) SetOffsets(tempOffset, humOffset *float64) (float64, float64) {
	d.mu.Lock()
	if tempOffset != nil {
		d.tempOffset = *tempOffset
	}
	if humOffset != nil {
		d.humOffset = *humOffset
	}
	to := d.tempOffset
	ho := d.humOffset
	d.mu.Unlock()
	log.Printf("Calibration offsets updated: temp=%.3f C, humidity=%.3f %%RH", to, ho)
	return to, ho
}

func (d *Driver) GetOffsets() (float64, float64) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.tempOffset, d.humOffset
}

func (d *Driver) setConnectedState(connected bool, errStr string) {
	d.mu.Lock()
	prev := d.connected
	d.connected = connected
	d.lastError = errStr
	if prev != connected {
		d.lastChangeTS = time.Now()
	}
	d.mu.Unlock()
	if prev != connected {
		if connected {
			log.Printf("Sensor connected at %s", d.hexAddress())
		} else {
			log.Printf("Sensor disconnected at %s: %s", d.hexAddress(), errStr)
		}
	}
}

func (d *Driver) setReading(t, h float64, ts time.Time) {
	d.mu.Lock()
	// apply calibration offsets
	oT := t + d.tempOffset
	oH := h + d.humOffset
	if oH < 0 {
		oH = 0
	}
	if oH > 100 {
		oH = 100
	}
	d.lastReading = Reading{TemperatureC: oT, HumidityRH: oH, Timestamp: ts}
	d.connected = true
	d.lastError = ""
	d.mu.Unlock()
}

func (d *Driver) GetReading() (Reading, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastReading, d.connected
}

func (d *Driver) Status() (connected bool, addr string, lastUpdate time.Time, lastErr string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.connected, fmt.Sprintf("0x%02X", d.addr), d.lastReading.Timestamp, d.lastError
}

// Read one measurement from SHT30/31/35-like sensor
func (d *Driver) readOnce(ctx context.Context, addr uint16) (float64, float64, error) {
	dev := &i2c.Dev{Bus: d.bus, Addr: uint16(addr)}
	// Commands: Single shot, high repeatability, clock stretching disabled => 0x24 0x00
	cmd := []byte{0x24, 0x00}

	// serialize I2C transactions to avoid bus contention from concurrent HTTP handlers (if any)
	d.busMu.Lock()
	err := dev.Tx(cmd, nil)
	d.busMu.Unlock()
	if err != nil {
		return 0, 0, fmt.Errorf("write cmd failed: %w", err)
	}

	select {
	case <-time.After(d.measDelay):
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	}

	buf := make([]byte, 6)
	d.busMu.Lock()
	err = dev.Tx(nil, buf)
	d.busMu.Unlock()
	if err != nil {
		return 0, 0, fmt.Errorf("read data failed: %w", err)
	}
	// CRC checks
	if crc8(buf[0:2]) != buf[2] {
		return 0, 0, errors.New("temp CRC mismatch")
	}
	if crc8(buf[3:5]) != buf[5] {
		return 0, 0, errors.New("humidity CRC mismatch")
	}
	tRaw := uint16(buf[0])<<8 | uint16(buf[1])
	hRaw := uint16(buf[3])<<8 | uint16(buf[4])
	// Conversion per Sensirion datasheet
	t := -45.0 + 175.0*(float64(tRaw)/65535.0)
	h := 100.0 * (float64(hRaw) / 65535.0)
	if h < 0 {
		h = 0
	}
	if h > 100 {
		h = 100
	}
	return t, h, nil
}

func crc8(data []byte) byte {
	crc := byte(0xFF)
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if (crc & 0x80) != 0 {
				crc = (crc << 1) ^ 0x31
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func (d *Driver) pollLoop(ctx context.Context, initialBackoff, maxBackoff time.Duration) {
	backoff := initialBackoff
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		addr := d.GetAddress()
		t, h, err := d.readOnce(ctx, addr)
		if err != nil {
			d.setConnectedState(false, err.Error())
			// Backoff on failure
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		// Success
		backoff = initialBackoff
		d.setReading(t, h, time.Now())

		// Sleep to maintain ~2s polling interval from start of cycle
		elapsed := time.Since(start)
		var sleepDur time.Duration
		if elapsed < pollInterval {
			sleepDur = pollInterval - elapsed
		} else {
			sleepDur = 0
		}
		select {
		case <-time.After(sleepDur):
		case <-ctx.Done():
			return
		}
	}
}

// HTTP Handlers
func statusHandler(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connected, addr, last, lastErr := d.Status()
		resp := map[string]any{
			"connected":    connected,
			"i2c_address":  addr,
			"last_update":  last.Format(time.RFC3339Nano),
		}
		if !connected && lastErr != "" {
			resp["error"] = lastErr
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func getI2CHandler(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"address": fmt.Sprintf("0x%02X", d.GetAddress())})
	}
}

func putI2CHandler(d *Driver) http.HandlerFunc {
	type req struct{ Address any `json:"address"` }
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		v, ok := body["address"]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing address"})
			return
		}
		var addrStr string
		switch x := v.(type) {
		case string:
			addrStr = x
		case float64:
			addrStr = fmt.Sprintf("%d", int(x))
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address must be string or number"})
			return
		}
		addr, err := parseUint16Addr(addrStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid address"})
			return
		}
		if addr != 0x40 && addr != 0x41 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address must be 0x40 or 0x41"})
			return
		}
		d.SetAddress(addr)
		writeJSON(w, http.StatusOK, map[string]any{"address": fmt.Sprintf("0x%02X", addr)})
	}
}

func getCalibrationHandler(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		to, ho := d.GetOffsets()
		writeJSON(w, http.StatusOK, map[string]any{
			"temp_offset":     to,
			"humidity_offset": ho,
		})
	}
}

func putCalibrationHandler(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		var (
			toPtr *float64
			hoPtr *float64
		)
		if v, ok := body["temp_offset"]; ok {
			sw, ok := v.(float64)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "temp_offset must be number"})
				return
			}
			to := sw
			toPtr = &to
		}
		if v, ok := body["humidity_offset"]; ok {
			sw, ok := v.(float64)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "humidity_offset must be number"})
				return
			}
			ho := sw
			hoPtr = &ho
		}
		newTO, newHO := d.SetOffsets(toPtr, hoPtr)
		writeJSON(w, http.StatusOK, map[string]any{
			"temp_offset":     newTO,
			"humidity_offset": newHO,
		})
	}
}

func readingsHandler(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reading, ok := d.GetReading()
		if !ok || reading.Timestamp.IsZero() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sensor disconnected"})
			return
		}
		writeJSON(w, http.StatusOK, reading)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func normalizeBusName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "/dev/i2c-") {
		return strings.TrimPrefix(name, "/dev/i2c-")
	}
	// allow e.g., i2c-1
	if strings.HasPrefix(strings.ToLower(name), "i2c-") {
		return name[4:]
	}
	return name
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	// Init periph host drivers
	if _, err := host.Init(); err != nil {
		log.Fatalf("periph host init failed: %v", err)
	}

	busName := normalizeBusName(cfg.I2CBus)
	bus, err := i2creg.Open(busName)
	if err != nil {
		log.Fatalf("failed to open I2C bus %q: %v", busName, err)
	}
	defer bus.Close()

	driver := NewDriver(bus, cfg.I2CAddress, cfg.CalTempOffset, cfg.CalHumOffset, cfg.MeasurementDelay)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go driver.pollLoop(ctx, cfg.BackoffInitial, cfg.BackoffMax)

	mux := http.NewServeMux()
	mux.HandleFunc("/status", statusHandler(driver))
	mux.HandleFunc("/i2c", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getI2CHandler(driver)(w, r)
		case http.MethodPut:
			putI2CHandler(driver)(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/calibration", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCalibrationHandler(driver)(w, r)
		case http.MethodPut:
			putCalibrationHandler(driver)(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/readings", readingsHandler(driver))

	addr := fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort)
	srv := &http.Server{Addr: addr, Handler: mux}

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("HTTP server listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-done
	log.Printf("shutdown signal received")
	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_ = srv.Shutdown(shutdownCtx)
	cancel() // stop poll loop
	log.Printf("shutdown complete")
}
