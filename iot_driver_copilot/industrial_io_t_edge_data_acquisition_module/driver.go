package main

import (
	"context"
	"encoding/json"
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
)

type ReadSample struct {
	Protocol   string        `json:"protocol"`
	SlaveID    int           `json:"slave_id"`
	Table      string        `json:"table"`
	Address    int           `json:"address"`
	Quantity   int           `json:"quantity"`
	Timestamp  time.Time     `json:"timestamp"`
	BoolValues []bool        `json:"bool_values,omitempty"`
	RegValues  []uint16      `json:"reg_values,omitempty"`
	Source     string        `json:"source"` // cache or live
}

type LatestBuffer struct {
	mu     sync.RWMutex
	sample *ReadSample
}

func (b *LatestBuffer) Get() *ReadSample {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.sample == nil { return nil }
	cp := *b.sample
	return &cp
}

func (b *LatestBuffer) Set(s *ReadSample) {
	b.mu.Lock()
	b.sample = s
	b.mu.Unlock()
}

// Global buffer for latest polled sample
var latest LatestBuffer

// Build a Modbus client based on provided parameters
func buildTCPClient(host string, port int, slaveID int, timeoutMs int) (*modbus.TCPClientHandler, modbus.Client, error) {
	h := modbus.NewTCPClientHandler(fmt.Sprintf("%s:%d", host, port))
	h.Timeout = time.Duration(timeoutMs) * time.Millisecond
	h.SlaveId = byte(slaveID)
	if err := h.Connect(); err != nil {
		return nil, nil, err
	}
	client := modbus.NewClient(h)
	return h, client, nil
}

func buildRTUClient(serialPort string, baud, dataBits, stopBits int, parity string, slaveID int, timeoutMs int) (*modbus.RTUClientHandler, modbus.Client, error) {
	h := modbus.NewRTUClientHandler(serialPort)
	h.BaudRate = baud
	h.DataBits = dataBits
	h.StopBits = stopBits
	h.Parity = parity
	h.SlaveId = byte(slaveID)
	h.Timeout = time.Duration(timeoutMs) * time.Millisecond
	if err := h.Connect(); err != nil {
		return nil, nil, err
	}
	client := modbus.NewClient(h)
	return h, client, nil
}

func decodeCoils(data []byte, quantity int) []bool {
	vals := make([]bool, 0, quantity)
	for i := 0; i < quantity; i++ {
		byteIndex := i / 8
		bitIndex := uint(i % 8)
		if byteIndex < len(data) {
			bit := (data[byteIndex] >> bitIndex) & 0x01
			vals = append(vals, bit == 1)
		} else {
			vals = append(vals, false)
		}
	}
	return vals
}

func decodeRegisters(data []byte) []uint16 {
	regs := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		regs = append(regs, uint16(data[i])<<8|uint16(data[i+1]))
	}
	return regs
}

func readOnce(client modbus.Client, table string, address, quantity int) (*ReadSample, error) {
	addr := uint16(address)
	qty := uint16(quantity)
	var (
		raw []byte
		err error
	)
	s := &ReadSample{Table: table, Address: address, Quantity: quantity, Timestamp: time.Now()}
	switch strings.ToLower(table) {
	case "coil":
		raw, err = client.ReadCoils(addr, qty)
		if err != nil { return nil, err }
		s.BoolValues = decodeCoils(raw, quantity)
	case "discrete":
		raw, err = client.ReadDiscreteInputs(addr, qty)
		if err != nil { return nil, err }
		s.BoolValues = decodeCoils(raw, quantity)
	case "input":
		raw, err = client.ReadInputRegisters(addr, qty)
		if err != nil { return nil, err }
		s.RegValues = decodeRegisters(raw)
	case "holding":
		raw, err = client.ReadHoldingRegisters(addr, qty)
		if err != nil { return nil, err }
		s.RegValues = decodeRegisters(raw)
	default:
		return nil, fmt.Errorf("invalid table: %s", table)
	}
	return s, nil
}

func startBackgroundCollector(ctx context.Context, cfg Config) {
	go func() {
		log.Printf("collector: starting with protocol=%s slave=%d poll=%s[%d,%d] interval=%dms", cfg.ModbusProtocol, cfg.SlaveID, cfg.PollTable, cfg.PollAddress, cfg.PollQuantity, cfg.PollIntervalMs)
		attempt := 0
		backoff := time.Duration(cfg.RetryInitialMs) * time.Millisecond
		maxBackoff := time.Duration(cfg.RetryMaxMs) * time.Millisecond
		var (
			tcpH *modbus.TCPClientHandler
			rtuH *modbus.RTUClientHandler
			client modbus.Client
		)
		connected := false

		connect := func() error {
			if cfg.ModbusProtocol == "tcp" {
				var err error
				tcpH, client, err = buildTCPClient(cfg.DeviceHost, cfg.DevicePort, cfg.SlaveID, cfg.TimeoutMs)
				if err != nil { return err }
			} else {
				var err error
				rtuH, client, err = buildRTUClient(cfg.SerialPort, cfg.SerialBaud, cfg.SerialDataBits, cfg.SerialStopBits, cfg.SerialParity, cfg.SlaveID, cfg.TimeoutMs)
				if err != nil { return err }
			}
			connected = true
			attempt = 0
			backoff = time.Duration(cfg.RetryInitialMs) * time.Millisecond
			log.Printf("collector: connected")
			return nil
		}

		closeConn := func() {
			if tcpH != nil { tcpH.Close(); tcpH = nil }
			if rtuH != nil { rtuH.Close(); rtuH = nil }
			client = nil
			connected = false
			log.Printf("collector: disconnected")
		}

		for {
			select {
			case <-ctx.Done():
				closeConn()
				log.Printf("collector: stopped")
				return
			default:
			}

			if !connected {
				if err := connect(); err != nil {
					attempt++
					log.Printf("collector: connect failed (attempt %d): %v", attempt, err)
					time.Sleep(backoff)
					backoff *= 2
					if backoff > maxBackoff { backoff = maxBackoff }
					continue
				}
			}

			// Perform read
			s, err := readOnce(client, cfg.PollTable, cfg.PollAddress, cfg.PollQuantity)
			if err != nil {
				log.Printf("collector: read error: %v", err)
				closeConn()
				attempt++
				time.Sleep(backoff)
				backoff *= 2
				if backoff > maxBackoff { backoff = maxBackoff }
				continue
			}
			s.Protocol = cfg.ModbusProtocol
			s.SlaveID = cfg.SlaveID
			s.Source = "cache"
			latest.Set(s)
			log.Printf("collector: updated %s[%d,%d] at %s", s.Table, s.Address, s.Quantity, s.Timestamp.Format(time.RFC3339))
			time.Sleep(time.Duration(cfg.PollIntervalMs) * time.Millisecond)
		}
	}()
}

func parseQueryInt(r *http.Request, key string) (int, bool, error) {
	v := r.URL.Query().Get(key)
	if v == "" { return 0, false, nil }
	iv, err := strconv.Atoi(v)
	if err != nil { return 0, false, err }
	return iv, true, nil
}

func modbusReadHandler(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		protocol := q.Get("protocol")
		if protocol == "" { protocol = cfg.ModbusProtocol }
		protocol = strings.ToLower(protocol)
		if protocol != "tcp" && protocol != "rtu" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "{\"error\":\"invalid protocol\"}")
			return
		}

		// Defaults from env
		host := cfg.DeviceHost
		port := cfg.DevicePort
		serialPort := cfg.SerialPort
		slaveID := cfg.SlaveID
		timeoutMs := cfg.TimeoutMs

		if hv := q.Get("host"); hv != "" { host = hv }
		if pv, ok, err := parseQueryInt(r, "port"); err == nil && ok { port = pv }
		if sp := q.Get("serial_port"); sp != "" { serialPort = sp }
		if sid, ok, err := parseQueryInt(r, "slave_id"); err == nil && ok { slaveID = sid }
		if to, ok, err := parseQueryInt(r, "timeout_ms"); err == nil && ok { timeoutMs = to }

		table := q.Get("table")
		if table == "" { table = cfg.PollTable }
		address, okAddr, err := parseQueryInt(r, "address")
		if err != nil { w.WriteHeader(http.StatusBadRequest); fmt.Fprintf(w, "{\"error\":\"invalid address\"}"); return }
		if !okAddr { address = cfg.PollAddress }
		quantity, okQty, err := parseQueryInt(r, "quantity")
		if err != nil { w.WriteHeader(http.StatusBadRequest); fmt.Fprintf(w, "{\"error\":\"invalid quantity\"}"); return }
		if !okQty { quantity = cfg.PollQuantity }

		// If request matches the cache, serve cached
		if protocol == cfg.ModbusProtocol && slaveID == cfg.SlaveID && strings.ToLower(table) == strings.ToLower(cfg.PollTable) && address == cfg.PollAddress && quantity == cfg.PollQuantity {
			if s := latest.Get(); s != nil {
				resp := *s
				resp.Source = "cache"
				writeJSON(w, resp)
				return
			}
		}

		// Live read with temporary client
		var (
			client modbus.Client
			closer interface{ Close() error }
			buildErr error
		)
		if protocol == "tcp" {
			var h *modbus.TCPClientHandler
			h, client, buildErr = buildTCPClient(host, port, slaveID, timeoutMs)
			closer = h
		} else {
			var h *modbus.RTUClientHandler
			h, client, buildErr = buildRTUClient(serialPort, cfg.SerialBaud, cfg.SerialDataBits, cfg.SerialStopBits, cfg.SerialParity, slaveID, timeoutMs)
			closer = h
		}
		if buildErr != nil {
			log.Printf("http: connect error: %v", buildErr)
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "{\"error\":\"connect failed\"}")
			return
		}
		defer closer.Close()

		s, err := readOnce(client, table, address, quantity)
		if err != nil {
			log.Printf("http: read error: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "{\"error\":\"read failed\"}")
			return
		}
		s.Protocol = protocol
		s.SlaveID = slaveID
		s.Source = "live"
		writeJSON(w, s)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startBackgroundCollector(ctx, cfg)

	http.HandleFunc("/modbus/read", modbusReadHandler(cfg))

	server := &http.Server{ Addr: fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort) }

	go func() {
		log.Printf("http: serving on %s:%d", cfg.HTTPHost, cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("shutdown: received signal, stopping...")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	cancel()
	log.Printf("stopped")
}
