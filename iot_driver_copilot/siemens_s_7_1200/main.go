package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	gos7 "github.com/robinson/gos7"
)

type PLCConfig struct {
	IP      string        `json:"ip"`
	Rack    int           `json:"rack"`
	Slot    int           `json:"slot"`
	Port    int           `json:"port"`        // informational only; S7comm uses 102
	Timeout time.Duration `json:"timeout_ms"` // per-op timeout
}

type PLCConfigPayload struct {
	IP        *string `json:"ip,omitempty"`
	Rack      *int    `json:"rack,omitempty"`
	Slot      *int    `json:"slot,omitempty"`
	Port      *int    `json:"port,omitempty"`
	TimeoutMS *int    `json:"timeout_ms,omitempty"`
}

type LastSample struct {
	DB        int       `json:"db"`
	Start     int       `json:"start"`
	Size      int       `json:"size"`
	DataHex   string    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

type Driver struct {
	mu              sync.RWMutex
	cfg             PLCConfig
	client          *gos7.Client
	desired         bool
	connected       bool
	lastError       string
	lastUpdate      time.Time
	maintainerOnce  sync.Once
	maintainerRun   bool
	stopCh          chan struct{}
	wg              sync.WaitGroup

	backoffInitial time.Duration
	backoffMax     time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration
	retryMax int

	lastSample LastSample
}

func NewDriver() *Driver {
	return &Driver{
		// defaults will be overridden by env or /plc/config
		cfg: PLCConfig{
			IP:      "",
			Rack:    0,
			Slot:    1,
			Port:    102,
			Timeout: 2000 * time.Millisecond,
		},
		backoffInitial: 500 * time.Millisecond,
		backoffMax:     10 * time.Second,
		readTimeout:    2000 * time.Millisecond,
		writeTimeout:   2000 * time.Millisecond,
		retryMax:        0, // 0 means unlimited
	}
}

func (d *Driver) applyEnv() {
	// Optional initial PLC config
	if v := os.Getenv("PLC_IP"); v != "" {
		d.cfg.IP = v
	}
	if v := getEnvInt("PLC_RACK"); v != nil {
		d.cfg.Rack = *v
	}
	if v := getEnvInt("PLC_SLOT"); v != nil {
		d.cfg.Slot = *v
	}
	if v := getEnvInt("PLC_TIMEOUT_MS"); v != nil {
		d.cfg.Timeout = time.Duration(*v) * time.Millisecond
	}

	// backoff and timeouts
	if v := getEnvInt("CONNECT_BACKOFF_MS_INITIAL"); v != nil {
		d.backoffInitial = time.Duration(*v) * time.Millisecond
	}
	if v := getEnvInt("CONNECT_BACKOFF_MS_MAX"); v != nil {
		d.backoffMax = time.Duration(*v) * time.Millisecond
	}
	if v := getEnvInt("READ_TIMEOUT_MS"); v != nil {
		d.readTimeout = time.Duration(*v) * time.Millisecond
	}
	if v := getEnvInt("WRITE_TIMEOUT_MS"); v != nil {
		d.writeTimeout = time.Duration(*v) * time.Millisecond
	}
	if v := getEnvInt("CONNECT_RETRY_MAX"); v != nil {
		d.retryMax = *v
	}
}

func (d *Driver) SetConfig(p PLCConfigPayload) PLCConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p.IP != nil {
		d.cfg.IP = *p.IP
	}
	if p.Rack != nil {
		d.cfg.Rack = *p.Rack
	}
	if p.Slot != nil {
		d.cfg.Slot = *p.Slot
	}
	if p.Port != nil {
		d.cfg.Port = *p.Port // informational only
	}
	if p.TimeoutMS != nil {
		d.cfg.Timeout = time.Duration(*p.TimeoutMS) * time.Millisecond
	}
	return d.cfg
}

func (d *Driver) getConfig() PLCConfig {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cfg
}

func (d *Driver) Connect() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.desired {
		log.Println("Connect requested: already desired")
		return nil
	}
	d.desired = true
	if d.stopCh == nil {
		d.stopCh = make(chan struct{})
	}
	if !d.maintainerRun {
		d.maintainerRun = true
		d.wg.Add(1)
		go d.maintainConnection()
	}
	return nil
}

func (d *Driver) Disconnect() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.desired = false
	if d.stopCh != nil {
		close(d.stopCh)
		d.stopCh = nil
	}
	if d.client != nil {
		_ = d.client.Disconnect()
		d.client = nil
	}
	d.connected = false
	d.lastUpdate = time.Now()
	log.Println("Disconnected from PLC")
	return nil
}

func (d *Driver) maintainConnection() {
	defer d.wg.Done()
	log.Println("Connection maintainer started")
	backoff := d.backoffInitial
	tries := 0

	for {
		d.mu.Lock()
		dDesired := d.desired
		cfg := d.cfg
		d.mu.Unlock()

		if !dDesired {
			log.Println("Maintainer: desired=false, exiting")
			return
		}

		if d.isConnected() {
			// already connected; sleep a bit and loop
			select {
			case <-time.After(2 * time.Second):
			case <-d.stopChan():
				log.Println("Maintainer stop signal received")
				return
			}
			continue
		}

		log.Printf("Attempting to connect to PLC %s rack=%d slot=%d\n", cfg.IP, cfg.Rack, cfg.Slot)
		client := gos7.NewClient()
		// connect without blocking shutdown forever
		connErr := make(chan error, 1)
		go func() {
			connErr <- client.ConnectTo(cfg.IP, cfg.Rack, cfg.Slot)
		}()

		select {
		case err := <-connErr:
			if err == nil {
				d.mu.Lock()
				d.client = client
				d.connected = true
				d.lastError = ""
				d.lastUpdate = time.Now()
				d.mu.Unlock()
				log.Printf("Connected to PLC %s\n", cfg.IP)
				// reset backoff and tries
				backoff = d.backoffInitial
				tries = 0
				// small sleep before next health loop
				select {
				case <-time.After(1 * time.Second):
				case <-d.stopChan():
					return
				}
				continue
			}
			// failed
			d.mu.Lock()
			d.lastError = err.Error()
			d.lastUpdate = time.Now()
			d.mu.Unlock()
			log.Printf("Connect failed: %v\n", err)
		case <-time.After(cfg.Timeout):
			// timeout trying to connect
			d.mu.Lock()
			d.lastError = fmt.Sprintf("connect timeout after %v", cfg.Timeout)
			d.lastUpdate = time.Now()
			d.mu.Unlock()
			log.Printf("Connect timeout after %v\n", cfg.Timeout)
		}

		tries++
		if d.retryMax > 0 && tries >= d.retryMax {
			log.Printf("Max retries (%d) reached, backing off for %v\n", d.retryMax, d.backoffMax)
			tries = 0
			backoff = d.backoffMax
		}

		select {
		case <-time.After(backoff):
		case <-d.stopChan():
			log.Println("Maintainer stop signal received")
			return
		}
		// exponential backoff
		backoff = minDur(backoff*2, d.backoffMax)
	}
}

func (d *Driver) stopChan() <-chan struct{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stopCh
}

func (d *Driver) isConnected() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.connected && d.client != nil
}

func (d *Driver) ensureConnected() error {
	if !d.isConnected() {
		return errors.New("not connected")
	}
	return nil
}

func (d *Driver) ReadDB(db, start, size int) ([]byte, error) {
	if err := d.ensureConnected(); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	d.mu.RLock()
	client := d.client
	readTimeout := d.readTimeout
	d.mu.RUnlock()

	done := make(chan error, 1)
	go func() {
		done <- client.ReadArea(gos7.S7AreaDB, db, start, size, gos7.S7WLByte, buf)
	}()

	select {
	case err := <-done:
		if err != nil {
			// mark disconnected and let maintainer reconnect
			d.mu.Lock()
			d.connected = false
			d.lastError = err.Error()
			d.lastUpdate = time.Now()
			d.mu.Unlock()
			return nil, err
		}
		// update last sample
		d.mu.Lock()
		d.lastSample = LastSample{
			DB:        db,
			Start:     start,
			Size:      size,
			DataHex:   hex.EncodeToString(buf),
			Timestamp: time.Now(),
		}
		d.lastUpdate = time.Now()
		d.mu.Unlock()
		return buf, nil
	case <-time.After(readTimeout):
		// timeout
		d.mu.Lock()
		d.connected = false // assume dead to trigger reconnect
		d.lastError = fmt.Sprintf("read timeout after %v", readTimeout)
		d.lastUpdate = time.Now()
		d.mu.Unlock()
		return nil, fmt.Errorf("read timeout after %v", readTimeout)
	}
}

func (d *Driver) WriteDB(db, start int, data []byte) error {
	if err := d.ensureConnected(); err != nil {
		return err
	}
	d.mu.RLock()
	client := d.client
	writeTimeout := d.writeTimeout
	d.mu.RUnlock()

	done := make(chan error, 1)
	go func() {
		done <- client.WriteArea(gos7.S7AreaDB, db, start, len(data), gos7.S7WLByte, data)
	}()

	select {
	case err := <-done:
		if err != nil {
			d.mu.Lock()
			d.connected = false
			d.lastError = err.Error()
			d.lastUpdate = time.Now()
			d.mu.Unlock()
			return err
		}
		d.mu.Lock()
		d.lastUpdate = time.Now()
		d.mu.Unlock()
		return nil
	case <-time.After(writeTimeout):
		d.mu.Lock()
		d.connected = false
		d.lastError = fmt.Sprintf("write timeout after %v", writeTimeout)
		d.lastUpdate = time.Now()
		d.mu.Unlock()
		return fmt.Errorf("write timeout after %v", writeTimeout)
	}
}

func (d *Driver) Status() map[string]any {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return map[string]any{
		"connected":  d.connected,
		"ip":         d.cfg.IP,
		"rack":       d.cfg.Rack,
		"slot":       d.cfg.Slot,
		"port":       d.cfg.Port,
		"timeout_ms": d.cfg.Timeout.Milliseconds(),
		"last_error": d.lastError,
		"last_update": d.lastUpdate.UTC().Format(time.RFC3339Nano),
	}
}

// HTTP Handlers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseIntQuery(r *http.Request, key string) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return 0, fmt.Errorf("missing query param: %s", key)
	}
	iv, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid int for %s", key)
	}
	return iv, nil
}

func handlePLCConfig(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var p PLCConfigPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		cfg := d.SetConfig(p)
		log.Printf("Config updated: ip=%s rack=%d slot=%d port=%d timeout=%v\n", cfg.IP, cfg.Rack, cfg.Slot, cfg.Port, cfg.Timeout)
		writeJSON(w, http.StatusOK, cfg)
	}
}

func handlePLCConnect(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if d.getConfig().IP == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "PLC ip not set; call /plc/config or set PLC_IP"})
			return
		}
		if err := d.Connect(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "connecting"})
	}
}

func handlePLCStatus(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, d.Status())
	}
}

func handlePLCDisconnect(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = d.Disconnect()
		writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
	}
}

func handleDBRead(d *Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		db, err := parseIntQuery(r, "db")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		start, err := parseIntQuery(r, "start")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		size, err := parseIntQuery(r, "size")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if size <= 0 || size > 2048 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "size must be 1..2048"})
			return
		}
		data, err := d.ReadDB(db, start, size)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		resp := map[string]any{
			"db":        db,
			"start":     start,
			"size":      size,
			"data":      hex.EncodeToString(data),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleDBWrite(d *Driver) http.HandlerFunc {
	type req struct {
		DB    int    `json:"db"`
		Start int    `json:"start"`
		Data  string `json:"data"` // hex-encoded
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var p req
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if p.DB < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "db must be >=1"})
			return
		}
		if p.Start < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start must be >=0"})
			return
		}
		if p.Data == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data (hex) required"})
			return
		}
		bytes, err := hex.DecodeString(p.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data must be hex"})
			return
		}
		if len(bytes) == 0 || len(bytes) > 2048 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data length must be 1..2048 bytes"})
			return
		}
		if err := d.WriteDB(p.DB, p.Start, bytes); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"written":   len(bytes),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
}

// Utilities

func getEnvInt(name string) *int {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	iv, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("WARN: invalid int for %s: %v\n", name, err)
		return nil
	}
	return &iv
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func mustGetEnv(name string) string {
	val := os.Getenv(name)
	if val == "" {
		log.Fatalf("Environment variable %s is required", name)
	}
	return val
}

func main() {
	// Required HTTP server configuration
	host := mustGetEnv("HTTP_HOST")
	port := mustGetEnv("HTTP_PORT")

	d := NewDriver()
	d.applyEnv()

	mux := http.NewServeMux()
	mux.HandleFunc("/plc/config", handlePLCConfig(d))
	mux.HandleFunc("/plc/connect", handlePLCConnect(d))
	mux.HandleFunc("/plc/status", handlePLCStatus(d))
	mux.HandleFunc("/plc/disconnect", handlePLCDisconnect(d))
	mux.HandleFunc("/db/read", handleDBRead(d))
	mux.HandleFunc("/db/write", handleDBWrite(d))

	srv := &http.Server{
		Addr:              host + ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-done
		log.Println("Shutting down HTTP server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = d.Disconnect()
	}()

	log.Printf("HTTP server listening on %s\n", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server error: %v", err)
	}
	log.Println("Server stopped")
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
