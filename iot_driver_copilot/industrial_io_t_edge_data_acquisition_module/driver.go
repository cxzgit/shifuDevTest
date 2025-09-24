package main

import (
	"context"
	"encoding/binary"
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

	"github.com/goburrow/modbus"
	"github.com/goburrow/serial"
)

type Sample struct {
	Protocol string    `json:"protocol"`
	Table    string    `json:"table"`
	SlaveID  uint8     `json:"slave_id"`
	Address  uint16    `json:"address"`
	Quantity uint16    `json:"quantity"`
	Timestamp time.Time `json:"timestamp"`
	DataBools []bool    `json:"bools,omitempty"`
	DataRegs  []uint16  `json:"registers,omitempty"`
}

type Status struct {
	Connected  bool      `json:"connected"`
	LastError  string    `json:"last_error,omitempty"`
	LastUpdate time.Time `json:"last_update"`
	Retries    int       `json:"retries"`
}

type Buffer struct {
	mu     sync.RWMutex
	sample Sample
	status Status
}

func (b *Buffer) Set(sample Sample, status Status) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sample = sample
	b.status = status
}

func (b *Buffer) Get() (Sample, Status) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sample, b.status
}

type Poller struct {
	cfg    Config
	buf    *Buffer
	logger *log.Logger
}

func newPoller(cfg Config, buf *Buffer, logger *log.Logger) *Poller {
	return &Poller{cfg: cfg, buf: buf, logger: logger}
}

func (p *Poller) run(ctx context.Context) {
	backoff := p.cfg.RetryBaseBackoff
	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			p.logger.Printf("poller stopping")
			return
		default:
		}

		client, closer, err := p.makeClient()
		if err != nil {
			p.logger.Printf("modbus client create error: %v", err)
			consecutiveFailures++
			p.updateStatus(false, err, consecutiveFailures)
			backoff = nextBackoff(backoff, p.cfg.RetryBaseBackoff, p.cfg.RetryMaxBackoff)
			sleepWithCtx(ctx, backoff)
			continue
		}

		// Perform read
		start := time.Now()
		var dataBytes []byte
		var readErr error
		switch p.cfg.Table {
		case "coil":
			dataBytes, readErr = client.ReadCoils(uint16(p.cfg.Address), uint16(p.cfg.Quantity))
		case "discrete":
			dataBytes, readErr = client.ReadDiscreteInputs(uint16(p.cfg.Address), uint16(p.cfg.Quantity))
		case "input":
			dataBytes, readErr = client.ReadInputRegisters(uint16(p.cfg.Address), uint16(p.cfg.Quantity))
		case "holding":
			dataBytes, readErr = client.ReadHoldingRegisters(uint16(p.cfg.Address), uint16(p.cfg.Quantity))
		default:
			readErr = fmt.Errorf("unsupported table: %s", p.cfg.Table)
		}
		if readErr != nil {
			p.logger.Printf("modbus read error: %v", readErr)
			consecutiveFailures++
			p.updateStatus(false, readErr, consecutiveFailures)
			if closer != nil { closer() }
			backoff = nextBackoff(backoff, p.cfg.RetryBaseBackoff, p.cfg.RetryMaxBackoff)
			sleepWithCtx(ctx, backoff)
			if p.cfg.RetryMax > 0 && consecutiveFailures >= p.cfg.RetryMax {
				// After reaching max retries, give a longer pause at max backoff then reset counter
				sleepWithCtx(ctx, p.cfg.RetryMaxBackoff)
				consecutiveFailures = 0
				backoff = p.cfg.RetryBaseBackoff
			}
			continue
		}

		// Decode and update buffer
		sample := Sample{
			Protocol: p.cfg.Protocol,
			Table:    p.cfg.Table,
			SlaveID:  p.cfg.SlaveID,
			Address:  p.cfg.Address,
			Quantity: p.cfg.Quantity,
			Timestamp: time.Now(),
		}
		if p.cfg.Table == "coil" || p.cfg.Table == "discrete" {
			bools := bytesToBools(dataBytes, int(p.cfg.Quantity))
			sample.DataBools = bools
		} else {
			sample.DataRegs = bytesToU16(dataBytes)
		}
		status := Status{Connected: true, LastError: "", LastUpdate: sample.Timestamp, Retries: 0}
		p.buf.Set(sample, status)
		p.logger.Printf("modbus read ok: table=%s addr=%d qty=%d dur=%s", p.cfg.Table, p.cfg.Address, p.cfg.Quantity, time.Since(start))

		// Reset failure counters and backoff
		consecutiveFailures = 0
		backoff = p.cfg.RetryBaseBackoff

		// Sleep until next poll or context cancel
		sleepWithCtx(ctx, p.cfg.PollInterval)
		if closer != nil { closer() }
	}
}

func (p *Poller) updateStatus(connected bool, err error, retries int) {
	lastErr := ""
	if err != nil { lastErr = err.Error() }
	p.buf.Set(p.buf.sample, Status{Connected: connected, LastError: lastErr, LastUpdate: time.Now(), Retries: retries})
}

func (p *Poller) makeClient() (modbus.Client, func(), error) {
	if p.cfg.Protocol == "tcp" {
		addr := fmt.Sprintf("%s:%d", p.cfg.TCPHost, p.cfg.TCPPort)
		h := modbus.NewTCPClientHandler(addr)
		h.Timeout = p.cfg.Timeout
		h.SlaveId = p.cfg.SlaveID
		if err := h.Connect(); err != nil {
			return nil, nil, err
		}
		client := modbus.NewClient(h)
		closer := func() { _ = h.Close() }
		return client, closer, nil
	}
	// RTU
	parity := serial.ParityNone
	switch strings.ToUpper(p.cfg.Parity) {
	case "N": parity = serial.ParityNone
	case "E": parity = serial.ParityEven
	case "O": parity = serial.ParityOdd
	}
	stopBits := serial.Stop1
	if p.cfg.StopBits == 2 { stopBits = serial.Stop2 }

	h := modbus.NewRTUClientHandler(p.cfg.SerialPort)
	h.Timeout = p.cfg.Timeout
	h.SlaveId = p.cfg.SlaveID
	h.BaudRate = p.cfg.BaudRate
	h.DataBits = p.cfg.DataBits
	h.Parity = parity
	h.StopBits = stopBits
	if err := h.Connect(); err != nil {
		return nil, nil, err
	}
	client := modbus.NewClient(h)
	closer := func() { _ = h.Close() }
	return client, closer, nil
}

func bytesToBools(b []byte, quantity int) []bool {
	res := make([]bool, quantity)
	for i := 0; i < quantity; i++ {
		byteIdx := i / 8
		bitIdx := uint(i % 8)
		if byteIdx < len(b) {
			res[i] = (b[byteIdx] & (1 << bitIdx)) != 0
		}
	}
	return res
}

func bytesToU16(b []byte) []uint16 {
	n := len(b) / 2
	res := make([]uint16, n)
	for i := 0; i < n; i++ {
		res[i] = binary.BigEndian.Uint16(b[i*2 : i*2+2])
	}
	return res
}

func nextBackoff(cur, base, max time.Duration) time.Duration {
	next := cur * 2
	if next < base { next = base }
	if next > max { next = max }
	return next
}

func sleepWithCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		logger.Fatalf("config error: %v", err)
	}

	buf := &Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller := newPoller(cfg, buf, logger)
	go poller.run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/modbus/read", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		// If protocol provided in query, perform one-shot live read; else return cached sample
		if proto := strings.ToLower(q.Get("protocol")); proto == "tcp" || proto == "rtu" {
			resp, code := handleLiveRead(ctx, q, logger)
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		sample, status := buf.Get()
		if sample.Timestamp.IsZero() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "no data yet",
				"diagnostics": status,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"source": "cache",
			"sample": sample,
			"diagnostics": status,
		})
	})

	httpSrv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort),
		Handler: mux,
	}

	go func() {
		logger.Printf("http server starting at %s:%d", cfg.HTTPHost, cfg.HTTPPort)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("http server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Printf("signal received: %s, shutting down", sig.String())
	cancel()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	logger.Printf("shutdown complete")
}

func handleLiveRead(ctx context.Context, q map[string][]string, logger *log.Logger) (map[string]any, int) {
	protocol := strings.ToLower(getOne(q, "protocol"))
	if err := ValidateQueryProtocol(protocol); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}

	var (
		client modbus.Client
		closer func()
		err error
	)

	timeoutMs := parseIntDefault(getOne(q, "timeout_ms"), 0)
	if timeoutMs <= 0 {
		return map[string]any{"error": "timeout_ms must be > 0 for live reads"}, http.StatusBadRequest
	}
	if protocol == "tcp" {
		host := getOne(q, "host")
		portStr := getOne(q, "port")
		if host == "" || portStr == "" { return map[string]any{"error": "host and port required for tcp"}, http.StatusBadRequest }
		port, err := strconv.Atoi(portStr)
		if err != nil { return map[string]any{"error": "invalid port"}, http.StatusBadRequest }
		h := modbus.NewTCPClientHandler(fmt.Sprintf("%s:%d", host, port))
		h.Timeout = time.Duration(timeoutMs) * time.Millisecond
		h.SlaveId = uint8(parseIntDefault(getOne(q, "slave_id"), 1))
		if err = h.Connect(); err != nil {
			return map[string]any{"error": fmt.Sprintf("connect error: %v", err)}, http.StatusBadGateway
		}
		client = modbus.NewClient(h)
		closer = func() { _ = h.Close() }
	} else {
		serialPort := getOne(q, "serial_port")
		baud := parseIntDefault(getOne(q, "baud"), 0)
		parity := strings.ToUpper(getOne(q, "parity"))
		stopBits := parseIntDefault(getOne(q, "stop_bits"), 0)
		dataBits := parseIntDefault(getOne(q, "data_bits"), 8)
		if serialPort == "" || baud <= 0 || (parity != "N" && parity != "E" && parity != "O") || (stopBits != 1 && stopBits != 2) {
			return map[string]any{"error": "invalid rtu params: require serial_port, baud>0, parity N|E|O, stop_bits 1|2"}, http.StatusBadRequest
		}
		par := serial.ParityNone
		switch parity { case "N": par = serial.ParityNone; case "E": par = serial.ParityEven; case "O": par = serial.ParityOdd }
		stb := serial.Stop1; if stopBits == 2 { stb = serial.Stop2 }
		h := modbus.NewRTUClientHandler(serialPort)
		h.Timeout = time.Duration(timeoutMs) * time.Millisecond
		h.SlaveId = uint8(parseIntDefault(getOne(q, "slave_id"), 1))
		h.BaudRate = baud
		h.DataBits = dataBits
		h.Parity = par
		h.StopBits = stb
		if err = h.Connect(); err != nil {
			return map[string]any{"error": fmt.Sprintf("connect error: %v", err)}, http.StatusBadGateway
		}
		client = modbus.NewClient(h)
		closer = func() { _ = h.Close() }
	}
	defer func() { if closer != nil { closer() } }()

	slaveID := uint8(parseIntDefault(getOne(q, "slave_id"), 1))
	_ = slaveID // already set on handler

	table := strings.ToLower(getOne(q, "table"))
	if table != "coil" && table != "discrete" && table != "input" && table != "holding" {
		return map[string]any{"error": "table must be coil|discrete|input|holding"}, http.StatusBadRequest
	}
	address := parseIntDefault(getOne(q, "address"), 0)
	quantity := parseIntDefault(getOne(q, "quantity"), 1)
	if quantity <= 0 || quantity > 125 {
		return map[string]any{"error": "quantity must be 1-125"}, http.StatusBadRequest
	}

	var dataBytes []byte
	var readErr error
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	// Perform read (no separate context in goburrow; timeout set on handler)
	switch table {
	case "coil":
		dataBytes, readErr = client.ReadCoils(uint16(address), uint16(quantity))
	case "discrete":
		dataBytes, readErr = client.ReadDiscreteInputs(uint16(address), uint16(quantity))
	case "input":
		dataBytes, readErr = client.ReadInputRegisters(uint16(address), uint16(quantity))
	case "holding":
		dataBytes, readErr = client.ReadHoldingRegisters(uint16(address), uint16(quantity))
	}
	if readErr != nil {
		return map[string]any{"error": fmt.Sprintf("read error: %v", readErr)}, http.StatusBadGateway
	}

	sample := Sample{
		Protocol: protocol,
		Table:    table,
		SlaveID:  slaveID,
		Address:  uint16(address),
		Quantity: uint16(quantity),
		Timestamp: time.Now(),
	}
	if table == "coil" || table == "discrete" {
		sample.DataBools = bytesToBools(dataBytes, quantity)
	} else {
		sample.DataRegs = bytesToU16(dataBytes)
	}

	return map[string]any{
		"source": "live",
		"sample": sample,
		"diagnostics": map[string]any{
			"connected": true,
			"last_error": "",
			"last_update": sample.Timestamp,
			"retries": 0,
		},
	}, http.StatusOK
}

func getOne(q map[string][]string, key string) string {
	vals := q[key]
	if len(vals) > 0 { return vals[0] }
	return ""
}

func parseIntDefault(s string, def int) int {
	if s == "" { return def }
	v, err := strconv.Atoi(s)
	if err != nil { return def }
	return v
}
