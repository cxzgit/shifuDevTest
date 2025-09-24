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
	"strings"
	"sync"
	"syscall"
	"time"

	serial "github.com/tarm/serial"
)

// Constants for Modbus RTU
const (
	funcReadInputRegisters = 0x04
	measurementRegAddr    = 0x0000
	statusRegAddr         = 0x0001
)

// Driver holds runtime state and configuration
type Driver struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup

	// Configurable parameters
	portName       string
	baudRate       int
	deviceAddress  byte
	pollInterval   time.Duration
	readTimeout    time.Duration
	retryInitial   time.Duration
	retryMax       time.Duration

	// Serial port
	port           *serial.Port

	// Background collection loop state
	connectionState string // connected, connecting, reconnecting, disconnected
	retryCount      int
	backoffDelay    time.Duration

	// Latest sample buffer
	lastSeen        time.Time
	measurementRaw  uint16
	deviceStatusRaw uint16
	lastError       string
	lastErrTime     time.Time

	// Control flags
	shouldRunCollector bool // whether collector should be running
	reconnectRequested  bool
}

// NewDriver constructs a Driver with env configuration
func NewDriver(cfg *Config) *Driver {
	ctx, cancel := context.WithCancel(context.Background())
	return &Driver{
		ctx:            ctx,
		cancel:         cancel,
		portName:       cfg.SerialPort,
		baudRate:       cfg.BaudRate,
		deviceAddress:  byte(cfg.ModbusAddress),
		pollInterval:   time.Duration(cfg.PollIntervalMs) * time.Millisecond,
		readTimeout:    time.Duration(cfg.ReadTimeoutMs) * time.Millisecond,
		retryInitial:    time.Duration(cfg.RetryInitialMs) * time.Millisecond,
		retryMax:        time.Duration(cfg.RetryMaxMs) * time.Millisecond,
		connectionState: "disconnected",
		backoffDelay:    time.Duration(cfg.RetryInitialMs) * time.Millisecond,
	}
}

// StartCollector starts the background collection loop
func (d *Driver) StartCollector() {
	d.mu.Lock()
	if d.shouldRunCollector {
		// already running
		d.mu.Unlock()
		return
	}
	d.shouldRunCollector = true
	d.mu.Unlock()

	d.wg.Add(1)
	go d.collectLoop()
}

// Stop gracefully stops the collector and closes the port
func (d *Driver) Stop() {
	d.cancel()
	d.wg.Wait()
	d.mu.Lock()
	if d.port != nil {
		_ = d.port.Close()
		d.port = nil
	}
	d.connectionState = "disconnected"
	d.mu.Unlock()
}

// collectLoop maintains the connection and polls the device
func (d *Driver) collectLoop() {
	defer d.wg.Done()
	log.Printf("collector: started")
	for {
		select {
		case <-d.ctx.Done():
			log.Printf("collector: stopping")
			return
		default:
		}

		// Ensure port is open
		if !d.isPortOpen() {
			d.setState("connecting")
			if err := d.openPort(); err != nil {
				d.onError("connect", err)
				d.setState("reconnecting")
				d.sleepBackoff()
				continue
			}
			d.setState("connected")
			d.resetBackoff()
			log.Printf("connected: port=%s baud=%d addr=%d", d.portName, d.baudRate, d.deviceAddress)
		}

		// Check for reconfiguration request
		if d.shouldReconnect() {
			log.Printf("reconfig: reconnect requested")
			d.closePort()
			d.setState("reconnecting")
			d.sleepBackoff()
			continue
		}

		// Poll measurement
		val, err := d.readInputRegister(measurementRegAddr)
		if err != nil {
			d.onError("poll-measurement", err)
			d.closePort()
			d.setState("reconnecting")
			d.sleepBackoff()
			continue
		}

		// Poll status
		st, err := d.readInputRegister(statusRegAddr)
		if err != nil {
			d.onError("poll-status", err)
			d.closePort()
			d.setState("reconnecting")
			d.sleepBackoff()
			continue
		}

		// Update buffer
		d.mu.Lock()
		d.measurementRaw = val
		d.deviceStatusRaw = st
		d.lastSeen = time.Now()
		d.mu.Unlock()
		log.Printf("sample: value=%d status=%d ts=%s", val, st, d.lastSeen.Format(time.RFC3339))

		t := d.pollInterval
		// In case poll interval < 200ms, cap to avoid busy-loop
		if t < 50*time.Millisecond {
			t = 50 * time.Millisecond
		}
		timer := time.NewTimer(t)
		select {
		case <-d.ctx.Done():
			_ = timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (d *Driver) isPortOpen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.port != nil
}

func (d *Driver) shouldReconnect() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reconnectRequested
}

func (d *Driver) setState(s string) {
	d.mu.Lock()
	d.connectionState = s
	d.mu.Unlock()
}

func (d *Driver) resetBackoff() {
	d.mu.Lock()
	d.backoffDelay = d.retryInitial
	d.retryCount = 0
	d.mu.Unlock()
}

func (d *Driver) sleepBackoff() {
	d.mu.Lock()
	bd := d.backoffDelay
	if bd <= 0 {
		bd = d.retryInitial
	}
	if bd > d.retryMax {
		bd = d.retryMax
	}
	d.retryCount++
	// Exponential increase for next attempt
	next := bd * 2
	if next > d.retryMax {
		next = d.retryMax
	}
	d.backoffDelay = next
	d.mu.Unlock()
	log.Printf("retry: count=%d sleeping=%s", d.retryCount, bd.String())
	timer := time.NewTimer(bd)
	select {
	case <-d.ctx.Done():
		_ = timer.Stop()
		return
	case <-timer.C:
	}
}

func (d *Driver) onError(stage string, err error) {
	msg := fmt.Sprintf("%s error: %v", stage, err)
	log.Printf("%s", msg)
	d.mu.Lock()
	d.lastError = msg
	d.lastErrTime = time.Now()
	d.mu.Unlock()
}

func (d *Driver) openPort() error {
	d.mu.Lock()
	name := d.portName
	baud := d.baudRate
	readTO := d.readTimeout
	d.mu.Unlock()

	if name == "" {
		return errors.New("serial port not specified")
	}
	if baud < 9600 || baud > 115200 {
		return fmt.Errorf("unsupported baud rate: %d", baud)
	}
	c := &serial.Config{
		Name:        name,
		Baud:        baud,
		ReadTimeout: readTO,
		Size:        8,
		Parity:      serial.ParityNone,
		StopBits:    serial.Stop1,
	}
	p, err := serial.OpenPort(c)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.port = p
	d.mu.Unlock()
	return nil
}

func (d *Driver) closePort() {
	d.mu.Lock()
	if d.port != nil {
		_ = d.port.Close()
		d.port = nil
	}
	d.mu.Unlock()
}

func (d *Driver) reconnect() {
	d.mu.Lock()
	d.reconnectRequested = true
	d.mu.Unlock()
}

func (d *Driver) clearReconnectRequest() {
	d.mu.Lock()
	d.reconnectRequested = false
	d.mu.Unlock()
}

// readInputRegister reads a single input register using Modbus function 0x04
func (d *Driver) readInputRegister(addr uint16) (uint16, error) {
	d.mu.Lock()
	p := d.port
	addrByte := d.deviceAddress
	readTO := d.readTimeout
	d.mu.Unlock()
	if p == nil {
		return 0, errors.New("port not open")
	}
	// Build request frame: [slave][func][addrHi][addrLo][qtyHi][qtyLo][CRCLo][CRCHi]
	frame := buildReadInputRegistersFrame(addrByte, addr, 1)
	if _, err := p.Write(frame); err != nil {
		return 0, err
	}
	// Expected response length = 5 + 2*qty = 7
	expected := 7
	deadline := time.Now().Add(readTO)
	buf := make([]byte, 0, expected)
	tmp := make([]byte, 64)
	for len(buf) < expected {
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("read timeout: got %d/%d bytes", len(buf), expected)
		}
		n, err := p.Read(tmp)
		if err != nil {
			// tarm/serial returns nil error on timeout; rely on deadline above
			if n == 0 {
				// brief sleep to avoid tight loop
				time.Sleep(5 * time.Millisecond)
				continue
			}
		}
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
	}
	// Validate response
	if len(buf) < expected {
		return 0, fmt.Errorf("short response: %d bytes", len(buf))
	}
	if buf[0] != addrByte {
		return 0, fmt.Errorf("invalid slave id: got %d want %d", buf[0], addrByte)
	}
	if buf[1] != funcReadInputRegisters {
		return 0, fmt.Errorf("invalid function: %d", buf[1])
	}
	byteCount := int(buf[2])
	if byteCount != 2 {
		return 0, fmt.Errorf("invalid byte count: %d", byteCount)
	}
	// CRC check
	respNoCRC := buf[:3+byteCount]
	crc := modbusCRC(respNoCRC)
	crcLo := byte(crc & 0x00FF)
	crcHi := byte(crc >> 8)
	if buf[3+byteCount] != crcLo || buf[3+byteCount+1] != crcHi {
		return 0, fmt.Errorf("CRC mismatch")
	}
	value := (uint16(buf[3]) << 8) | uint16(buf[4])
	return value, nil
}

// buildReadInputRegistersFrame creates a Modbus RTU request
func buildReadInputRegistersFrame(slave byte, addr uint16, qty uint16) []byte {
	b := make([]byte, 0, 8)
	b = append(b, slave)
	b = append(b, funcReadInputRegisters)
	b = append(b, byte(addr>>8), byte(addr&0xFF))
	b = append(b, byte(qty>>8), byte(qty&0xFF))
	crc := modbusCRC(b)
	b = append(b, byte(crc&0xFF), byte(crc>>8))
	return b
}

// modbusCRC computes Modbus RTU CRC16
func modbusCRC(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if (crc & 0x0001) != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// HTTP Handlers

type ConnectRequest struct {
	Port          string `json:"port"`
	BaudRate      int    `json:"baud_rate"`
	DeviceAddress int    `json:"device_address"`
}

type ConfigRequest struct {
	BaudRate      *int `json:"baud_rate"`
	DeviceAddress *int `json:"device_address"`
}

func (d *Driver) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid JSON"}`)
		return
	}
	if req.Port != "" {
		d.mu.Lock()
		d.portName = req.Port
		d.mu.Unlock()
	}
	if req.BaudRate != 0 {
		if req.BaudRate < 9600 || req.BaudRate > 115200 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"baud_rate out of range (9600-115200)"}`)
			return
		}
		d.mu.Lock()
		d.baudRate = req.BaudRate
		d.mu.Unlock()
	}
	if req.DeviceAddress != 0 {
		if req.DeviceAddress < 1 || req.DeviceAddress > 247 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"device_address out of range (1-247)"}`)
			return
		}
		d.mu.Lock()
		d.deviceAddress = byte(req.DeviceAddress)
		d.mu.Unlock()
	}

	// Start collector if not running
	d.StartCollector()
	// Try immediate open (non-blocking response)
	var err error
	if !d.isPortOpen() {
		if err = d.openPort(); err != nil {
			// map errors
			emsg := strings.ToLower(err.Error())
			if strings.Contains(emsg, "busy") || strings.Contains(emsg, "in use") {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprintf(w, `{"status":"conflict","message":"port busy"}`)
				return
			}
			if strings.Contains(emsg, "timeout") {
				w.WriteHeader(http.StatusGatewayTimeout)
				fmt.Fprintf(w, `{"status":"timeout"}`)
				return
			}
			// Leave collector to retry
			d.setState("reconnecting")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"status":"reconnecting"}`)
			return
		}
		// Success open
		d.setState("connected")
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"connected"}`)
}

func (d *Driver) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid JSON"}`)
		return
	}
	applied := false
	status := http.StatusOK
	msg := "applied"
	if req.BaudRate != nil {
		br := *req.BaudRate
		if br < 9600 || br > 115200 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"baud_rate out of range (9600-115200)"}`)
			return
		}
		d.mu.Lock()
		if d.baudRate != br {
			d.baudRate = br
			applied = true
		}
		d.mu.Unlock()
	}
	if req.DeviceAddress != nil {
		da := *req.DeviceAddress
		if da < 1 || da > 247 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"device_address out of range (1-247)"}`)
			return
		}
		d.mu.Lock()
		if int(d.deviceAddress) != da {
			d.deviceAddress = byte(da)
			applied = true
		}
		d.mu.Unlock()
	}
	if applied {
		// Requires reconnect
		d.reconnect()
		d.setState("reconnecting")
		status = http.StatusAccepted
		msg = "reconnecting"
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"status":"%s"}` , msg)
}

func (d *Driver) handleMeasurement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	state := d.connectionState
	val := d.measurementRaw
	seen := d.lastSeen
	lastErr := d.lastError
	lastErrTime := d.lastErrTime
	d.mu.Unlock()

	if state != "connected" {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"not connected"}`)
		return
	}
	if seen.IsZero() || time.Since(seen) > 5*d.pollInterval {
		// timeout
		w.WriteHeader(http.StatusGatewayTimeout)
		fmt.Fprintf(w, `{"error":"device timeout"}`)
		return
	}
	// If last error indicates CRC/invalid response, report 502
	if lastErr != "" && (strings.Contains(strings.ToLower(lastErr), "crc") || strings.Contains(strings.ToLower(lastErr), "invalid")) && time.Since(lastErrTime) < 2*d.pollInterval {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"invalid device response"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := map[string]interface{}{
		"value":     float64(val),
		"timestamp": seen.Format(time.RFC3339),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (d *Driver) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	status := map[string]interface{}{
		"connection_state": d.connectionState,
		"port":             d.portName,
		"baud_rate":        d.baudRate,
		"device_address":   int(d.deviceAddress),
		"last_seen":        d.lastSeen.Format(time.RFC3339),
		"retry_count":      d.retryCount,
		"last_error":       d.lastError,
		"device_status_raw": d.deviceStatusRaw,
	}
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}

func main() {
	cfg := LoadConfig()
	drv := NewDriver(cfg)

	// HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/connect", drv.handleConnect)
	mux.HandleFunc("/config", drv.handleConfig)
	mux.HandleFunc("/measurement", drv.handleMeasurement)
	mux.HandleFunc("/status", drv.handleStatus)

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort),
		Handler: mux,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("HTTP server listening on %s:%d", cfg.HTTPHost, cfg.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-done
	log.Printf("shutdown: received signal")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	drv.Stop()
	log.Printf("shutdown complete")
}
