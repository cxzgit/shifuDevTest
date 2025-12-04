package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/goburrow/modbus"
)

// Sample holds the latest parsed sensor data.
type Sample struct {
	TemperatureC            float64   `json:"temperature_c"`
	HumidityPercent         float64   `json:"humidity_percent"`
	SamplingIntervalSeconds int       `json:"sampling_interval_sec"`
	LastUpdate              time.Time `json:"last_update"`
}

// Status provides connection and driver health information.
type Status struct {
	Connected     bool      `json:"connected"`
	LastSuccess   time.Time `json:"last_success"`
	LastError     string    `json:"last_error"`
	SerialPort    string    `json:"serial_port"`
	BaudRate      int       `json:"baud_rate"`
	DataBits      int       `json:"data_bits"`
	Parity        string    `json:"parity"`
	StopBits      int       `json:"stop_bits"`
	SlaveID       int       `json:"slave_id"`
	PollIntervalMs int      `json:"poll_interval_ms"`
	ReadTimeoutMs  int      `json:"read_timeout_ms"`
}

// Driver implements Modbus RTU polling and HTTP exposure.
type Driver struct {
	cfg     *Config

	handler *modbus.RTUClientHandler
	client  modbus.Client

	muSample sync.RWMutex
	latest   *Sample

	muStatus sync.RWMutex
	status   Status

	wg       sync.WaitGroup
}

func NewDriver(cfg *Config) *Driver {
	return &Driver{cfg: cfg}
}

func (d *Driver) connect() error {
	if d.handler != nil {
		// If a handler exists, ensure it's closed before reconnecting
		_ = d.handler.Close()
	}
	log.Printf("[modbus] connecting to %s baud=%d data=%d parity=%s stop=%d slave=%d", d.cfg.SerialPort, d.cfg.BaudRate, d.cfg.DataBits, d.cfg.Parity, d.cfg.StopBits, d.cfg.SlaveID)
	h := modbus.NewRTUClientHandler(d.cfg.SerialPort)
	h.BaudRate = d.cfg.BaudRate
	h.DataBits = d.cfg.DataBits
	h.Parity = d.cfg.Parity
	h.StopBits = d.cfg.StopBits
	h.SlaveId = byte(d.cfg.SlaveID)
	h.Timeout = d.cfg.ReadTimeout
	if err := h.Connect(); err != nil {
		return err
	}
	d.handler = h
	d.client = modbus.NewClient(h)

	d.muStatus.Lock()
	d.status.Connected = true
	d.status.LastError = ""
	d.muStatus.Unlock()

	log.Printf("[modbus] connected")
	return nil
}

func (d *Driver) disconnect() {
	if d.handler != nil {
		_ = d.handler.Close()
	}
	d.muStatus.Lock()
	d.status.Connected = false
	d.muStatus.Unlock()
	log.Printf("[modbus] disconnected")
}

func (d *Driver) readRegister(addr uint16) (uint16, error) {
	if d.client == nil {
		return 0, fmt.Errorf("client not connected")
	}
	results, err := d.client.ReadHoldingRegisters(addr, 1)
	if err != nil {
		return 0, err
	}
	if len(results) < 2 {
		return 0, fmt.Errorf("short read: expected 2 bytes, got %d", len(results))
	}
	return binary.BigEndian.Uint16(results), nil
}

func (d *Driver) pollOnce() error {
	tempRaw, err := d.readRegister(d.cfg.RegTempAddr)
	if err != nil {
		return fmt.Errorf("read temp reg 0x%X: %w", d.cfg.RegTempAddr, err)
	}
	humRaw, err := d.readRegister(d.cfg.RegHumAddr)
	if err != nil {
		return fmt.Errorf("read hum reg 0x%X: %w", d.cfg.RegHumAddr, err)
	}
	sampRaw, err := d.readRegister(d.cfg.RegSamplingAddr)
	if err != nil {
		return fmt.Errorf("read sampling reg 0x%X: %w", d.cfg.RegSamplingAddr, err)
	}

	temp := float64(tempRaw) * d.cfg.ScaleTemp
	hum := float64(humRaw) * d.cfg.ScaleHum
	samp := int(sampRaw) // seconds or device-defined unit controlled via register

	now := time.Now().UTC()

	d.muSample.Lock()
	d.latest = &Sample{
		TemperatureC:            temp,
		HumidityPercent:         hum,
		SamplingIntervalSeconds: samp,
		LastUpdate:              now,
	}
	d.muSample.Unlock()

	d.muStatus.Lock()
	d.status.LastSuccess = now
	d.status.LastError = ""
	d.muStatus.Unlock()

	log.Printf("[poll] updated: temp=%.3f°C hum=%.3f%% samp=%ds at %s", temp, hum, samp, now.Format(time.RFC3339))
	return nil
}

func (d *Driver) runPoller(ctx context.Context) {
	d.wg.Add(1)
	defer d.wg.Done()

	// Initialize status context
	d.muStatus.Lock()
	d.status.SerialPort = d.cfg.SerialPort
	d.status.BaudRate = d.cfg.BaudRate
	d.status.DataBits = d.cfg.DataBits
	d.status.Parity = d.cfg.Parity
	d.status.StopBits = d.cfg.StopBits
	d.status.SlaveID = d.cfg.SlaveID
	d.status.PollIntervalMs = int(d.cfg.PollInterval / time.Millisecond)
	d.status.ReadTimeoutMs = int(d.cfg.ReadTimeout / time.Millisecond)
	d.muStatus.Unlock()

	backoff := d.cfg.RetryInitial

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Ensure connection
		if d.handler == nil {
			if err := d.connect(); err != nil {
				log.Printf("[modbus] connect failed: %v", err)
				d.muStatus.Lock()
				d.status.LastError = err.Error()
				d.status.Connected = false
				d.muStatus.Unlock()
				// Backoff before retrying
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				// Exponential backoff increase
				backoff = time.Duration(minDuration(float64(backoff)*d.cfg.RetryMultiplier, d.cfg.RetryMax))
				continue
			}
			// Reset backoff after successful connect
			backoff = d.cfg.RetryInitial
		}

		// Poll once
		if err := d.pollOnce(); err != nil {
			log.Printf("[poll] error: %v", err)
			d.muStatus.Lock()
			d.status.LastError = err.Error()
			d.muStatus.Unlock()
			// Disconnect to force reconnect on next loop
			d.disconnect()
			// Wait backoff before attempting reconnect
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = time.Duration(minDuration(float64(backoff)*d.cfg.RetryMultiplier, d.cfg.RetryMax))
			continue
		}

		// Successful poll: wait for polling interval
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.cfg.PollInterval):
		}
	}
}

func minDuration(a float64, b time.Duration) time.Duration {
	ad := time.Duration(a)
	if ad > b {
		return b
	}
	return ad
}

func (d *Driver) shutdown() {
	d.disconnect()
	// Wait for background worker to exit
	d.wg.Wait()
}

func (d *Driver) handleReadings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	d.muSample.RLock()
	latest := d.latest
	d.muSample.RUnlock()
	if latest == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no data yet"})
		return
	}
	_ = json.NewEncoder(w).Encode(latest)
}

func (d *Driver) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	d.muStatus.RLock()
	st := d.status
	d.muStatus.RUnlock()
	_ = json.NewEncoder(w).Encode(st)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	drv := NewDriver(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go drv.runPoller(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/readings", drv.handleReadings)
	mux.HandleFunc("/status", drv.handleStatus)

	addr := net.JoinHostPort(cfg.HTTPHost, fmt.Sprintf("%d", cfg.HTTPPort))
	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("[http] listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("[main] shutdown signal received")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	drv.shutdown()
	log.Printf("[main] shutdown complete")
}
