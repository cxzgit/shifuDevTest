package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/goburrow/modbus"
)

// Simple leveled logger
const (
	levelDebug = iota
	levelInfo
	levelWarn
	levelError
)

type Logger struct {
	lvl int
	l   *log.Logger
}

func NewLogger(levelStr string) *Logger {
	lvl := levelInfo
	switch levelStr {
	case "debug":
		lvl = levelDebug
	case "info":
		lvl = levelInfo
	case "warn":
		lvl = levelWarn
	case "error":
		lvl = levelError
	}
	return &Logger{lvl: lvl, l: log.New(os.Stdout, "", log.LstdFlags)}
}

func (lg *Logger) Debugf(format string, v ...interface{}) { if lg.lvl <= levelDebug { lg.l.Printf("DEBUG "+format, v...) } }
func (lg *Logger) Infof(format string, v ...interface{})  { if lg.lvl <= levelInfo  { lg.l.Printf("INFO  "+format, v...) } }
func (lg *Logger) Warnf(format string, v ...interface{})  { if lg.lvl <= levelWarn  { lg.l.Printf("WARN  "+format, v...) } }
func (lg *Logger) Errorf(format string, v ...interface{}) { if lg.lvl <= levelError { lg.l.Printf("ERROR "+format, v...) } }

// DataBuffer holds the latest telemetry samples
type DataBuffer struct {
	mu            sync.RWMutex
	temp          float64
	tempUpdatedAt time.Time
	hum           float64
	humUpdatedAt  time.Time
	lastError     string
	lastErrorAt   time.Time
	connected     bool
}

func (b *DataBuffer) SetConnected(c bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connected = c
}

func (b *DataBuffer) SetError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.lastError = err.Error()
		b.lastErrorAt = time.Now()
	} else {
		b.lastError = ""
		b.lastErrorAt = time.Time{}
	}
}

func (b *DataBuffer) SetTemp(v float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.temp = v
	b.tempUpdatedAt = time.Now()
}

func (b *DataBuffer) SetHum(v float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hum = v
	b.humUpdatedAt = time.Now()
}

func (b *DataBuffer) GetTemp() (float64, time.Time) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.temp, b.tempUpdatedAt
}

func (b *DataBuffer) GetHum() (float64, time.Time) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.hum, b.humUpdatedAt
}

func (b *DataBuffer) Status() (connected bool, lastErr string, lastErrAt time.Time) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected, b.lastError, b.lastErrorAt
}

// Collector manages Modbus RTU polling
type Collector struct {
	cfg     Config
	log     *Logger
	buf     *DataBuffer
	ctx     context.Context
	cancel  context.CancelFunc
	handler *modbus.RTUClientHandler
	client  modbus.Client
	wg      sync.WaitGroup
}

func NewCollector(cfg Config, lg *Logger, buf *DataBuffer) *Collector {
	h := modbus.NewRTUClientHandler(cfg.SerialPort)
	h.BaudRate = cfg.BaudRate
	h.DataBits = cfg.DataBits
	h.Parity = cfg.Parity
	h.StopBits = cfg.StopBits
	h.SlaveId = byte(cfg.SlaveID)
	h.Timeout = cfg.Timeout
	ctx, cancel := context.WithCancel(context.Background())
	return &Collector{
		cfg:     cfg,
		log:     lg,
		buf:     buf,
		ctx:     ctx,
		cancel:  cancel,
		handler: h,
		client:  modbus.NewClient(h),
	}
}

func (c *Collector) Start() {
	c.wg.Add(1)
	go c.run()
}

func (c *Collector) Shutdown() {
	c.cancel()
	c.wg.Wait()
	_ = c.handler.Close()
}

func (c *Collector) run() {
	defer c.wg.Done()
	backoff := c.cfg.BackoffBase
	for {
		select {
		case <-c.ctx.Done():
			c.log.Infof("Collector shutdown")
			return
		default:
		}

		// Ensure connection
		if err := c.handler.Connect(); err != nil {
			c.buf.SetConnected(false)
			c.buf.SetError(err)
			c.log.Errorf("Connect failed: %v", err)
			time.Sleep(backoff)
			if backoff < c.cfg.BackoffMax {
				backoff *= 2
				if backoff > c.cfg.BackoffMax {
					backoff = c.cfg.BackoffMax
				}
			}
			continue
		}

		// Connected
		c.buf.SetConnected(true)
		backoff = c.cfg.BackoffBase
		c.log.Infof("Connected to %s (baud=%d, parity=%s, stop=%d, id=%d)", c.cfg.SerialPort, c.cfg.BaudRate, c.cfg.Parity, c.cfg.StopBits, c.cfg.SlaveID)

		// Poll loop while context not canceled
		pollUntil := time.Now().Add(c.cfg.Timeout)
		_ = pollUntil // placeholder; handler timeout handles per-request timeouts

		// Read temperature
		temp, err := c.readScaled(c.cfg.TempFunc, c.cfg.TempRegister, c.cfg.TempScale)
		if err != nil {
			c.buf.SetError(err)
			c.log.Warnf("Temp read error: %v", err)
			_ = c.handler.Close()
			c.buf.SetConnected(false)
			time.Sleep(backoff)
			if backoff < c.cfg.BackoffMax {
				backoff *= 2
				if backoff > c.cfg.BackoffMax {
					backoff = c.cfg.BackoffMax
				}
			}
			continue
		}
		c.buf.SetTemp(temp)
		c.buf.SetError(nil)
		c.log.Debugf("Temp updated: %.3f", temp)

		// Read humidity
		hum, err := c.readScaled(c.cfg.HumFunc, c.cfg.HumRegister, c.cfg.HumScale)
		if err != nil {
			c.buf.SetError(err)
			c.log.Warnf("Humidity read error: %v", err)
			_ = c.handler.Close()
			c.buf.SetConnected(false)
			time.Sleep(backoff)
			if backoff < c.cfg.BackoffMax {
				backoff *= 2
				if backoff > c.cfg.BackoffMax {
					backoff = c.cfg.BackoffMax
				}
			}
			continue
		}
		c.buf.SetHum(hum)
		c.buf.SetError(nil)
		c.log.Debugf("Humidity updated: %.3f", hum)

		// Sleep poll interval
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(c.cfg.PollInterval):
		}
	}
}

func (c *Collector) readScaled(funcType string, reg uint16, scale float64) (float64, error) {
	var results []byte
	var err error
	quantity := uint16(1)
	switch funcType {
	case "input":
		results, err = c.client.ReadInputRegisters(reg, quantity)
	case "holding":
		results, err = c.client.ReadHoldingRegisters(reg, quantity)
	default:
		return 0, fmt.Errorf("unsupported function: %s", funcType)
	}
	if err != nil {
		return 0, err
	}
	if len(results) < 2 {
		return 0, fmt.Errorf("invalid response length: %d", len(results))
	}
	raw := binary.BigEndian.Uint16(results)
	val := float64(raw) * scale
	return val, nil
}

// HTTP server and handlers

type ValueResponse struct {
	Value float64 `json:"value"`
}

type ErrorResponse struct {
	Error      string    `json:"error"`
	LastError  string    `json:"lastError,omitempty"`
	LastErrAt  time.Time `json:"lastErrorAt,omitempty"`
	Connected  bool      `json:"connected"`
	LastUpdate time.Time `json:"lastUpdate,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	lg := NewLogger(cfg.LogLevel)
	buf := &DataBuffer{}
	col := NewCollector(cfg, lg, buf)
	col.Start()

	mux := http.NewServeMux()
	mux.HandleFunc("/temperature", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		val, ts := buf.GetTemp()
		connected, lastErr, lastErrAt := buf.Status()
		if ts.IsZero() || time.Since(ts) > cfg.MaxStaleness {
			writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{
				Error:      "temperature unavailable or stale",
				LastError:  lastErr,
				LastErrAt:  lastErrAt,
				Connected:  connected,
				LastUpdate: ts,
			})
			return
		}
		writeJSON(w, http.StatusOK, ValueResponse{Value: val})
	})
	mux.HandleFunc("/humidity", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		val, ts := buf.GetHum()
		connected, lastErr, lastErrAt := buf.Status()
		if ts.IsZero() || time.Since(ts) > cfg.MaxStaleness {
			writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{
				Error:      "humidity unavailable or stale",
				LastError:  lastErr,
				LastErrAt:  lastErrAt,
				Connected:  connected,
				LastUpdate: ts,
			})
			return
		}
		writeJSON(w, http.StatusOK, ValueResponse{Value: val})
	})

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort), Handler: mux}
	go func() {
		lg.Infof("HTTP server listening on %s:%d", cfg.HTTPHost, cfg.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			lg.Errorf("HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	lg.Infof("Signal received: %v, shutting down", sig)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	col.Shutdown()
	lg.Infof("Shutdown complete")
}
