package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/goburrow/modbus"
)

type DeviceInfo struct {
	Name         string `json:"name"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	Type         string `json:"type"`
}

type CommStatus struct {
	Configured       bool   `json:"configured"`
	OK               bool   `json:"ok"`
	LastError        string `json:"last_error,omitempty"`
	LastUpdateTs     string `json:"last_update_ts,omitempty"`
	ReconnectAttempts int   `json:"reconnect_attempts"`
	// TCP specifics
	Address          string `json:"address,omitempty"`
	UnitID           uint8  `json:"unit_id,omitempty"`
	// RTU specifics
	Port             string `json:"port,omitempty"`
	Baud             int    `json:"baud,omitempty"`
	Parity           string `json:"parity,omitempty"`
	StopBits         int    `json:"stop_bits,omitempty"`
	// MQTT specifics
	BrokerURL        string `json:"broker_url,omitempty"`
	ClientID         string `json:"client_id,omitempty"`
}

type StatusResponse struct {
	Device              DeviceInfo `json:"device"`
	AcquisitionRunning  bool       `json:"acquisition_running"`
	UpdateIntervalMs    int        `json:"update_interval_ms"`
	LastUpdateTs        string     `json:"last_update_ts,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	ModbusTCP           CommStatus `json:"modbus_tcp"`
	ModbusRTU           CommStatus `json:"modbus_rtu"`
	MQTT                CommStatus `json:"mqtt"`
}

type DriverState struct {
	cfg Config

	mu sync.RWMutex
	modbusTCP CommStatus
	modbusRTU CommStatus
	mqtt      CommStatus

	lastUpdate time.Time
	lastError  string
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg := LoadConfig()
	log.Printf("Starting DAQ-2000 driver HTTP=%s:%d interval=%dms", cfg.HTTPHost, cfg.HTTPPort, cfg.UpdateIntervalMs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	state := &DriverState{cfg: cfg}
	state.initStatuses()

	var wg sync.WaitGroup
	if cfg.ModbusTCPAddr != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runModbusTCPCollector(ctx, state)
		}()
	}
	if cfg.ModbusRTUPort != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runModbusRTUCollector(ctx, state)
		}()
	}
	if cfg.MQTTBrokerURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runMQTTClient(ctx, state)
		}()
	}

	server := &http.Server{Addr: cfg.HTTPHost + ":" + strconvI(cfg.HTTPPort)}
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		resp := state.statusResponse()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	go func() {
		log.Printf("HTTP server listening on %s:%d", cfg.HTTPHost, cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("Shutdown signal received, stopping...")
	cancel()
	shCtx, shCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shCancel()
	_ = server.Shutdown(shCtx)
	wg.Wait()
	log.Printf("Driver stopped")
}

func (s *DriverState) initStatuses() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modbusTCP = CommStatus{Configured: s.cfg.ModbusTCPAddr != "", Address: s.cfg.ModbusTCPAddr, UnitID: s.cfg.ModbusTCPUnitID}
	s.modbusRTU = CommStatus{Configured: s.cfg.ModbusRTUPort != "", Port: s.cfg.ModbusRTUPort, Baud: s.cfg.ModbusRTUBaud, Parity: s.cfg.ModbusRTUParity, StopBits: s.cfg.ModbusRTUStopBits, UnitID: s.cfg.ModbusRTUUnitID}
	s.mqtt = CommStatus{Configured: s.cfg.MQTTBrokerURL != "", BrokerURL: s.cfg.MQTTBrokerURL, ClientID: s.cfg.MQTTClientID}
}

func (s *DriverState) statusResponse() StatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lastUpdate := ""
	if !s.lastUpdate.IsZero() { lastUpdate = s.lastUpdate.Format(time.RFC3339) }
	return StatusResponse{
		Device: DeviceInfo{Name: "Industrial IoT Edge Data Acquisition Module", Model: "DAQ-2000", Manufacturer: "Siemens", Type: "Industrial IoT edge data acquisition module"},
		AcquisitionRunning: s.modbusTCP.OK || s.modbusRTU.OK,
		UpdateIntervalMs: s.cfg.UpdateIntervalMs,
		LastUpdateTs: lastUpdate,
		LastError: s.lastError,
		ModbusTCP: s.modbusTCP,
		ModbusRTU: s.modbusRTU,
		MQTT: s.mqtt,
	}
}

func (s *DriverState) setTCPStatus(ok bool, errStr string, update bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modbusTCP.OK = ok
	if errStr != "" { s.modbusTCP.LastError = errStr; s.lastError = "tcp: " + errStr }
	if update { s.modbusTCP.LastUpdateTs = time.Now().Format(time.RFC3339); s.lastUpdate = time.Now() }
}

func (s *DriverState) setRTUStatus(ok bool, errStr string, update bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modbusRTU.OK = ok
	if errStr != "" { s.modbusRTU.LastError = errStr; s.lastError = "rtu: " + errStr }
	if update { s.modbusRTU.LastUpdateTs = time.Now().Format(time.RFC3339); s.lastUpdate = time.Now() }
}

func (s *DriverState) incTCPReconnect() { s.mu.Lock(); s.modbusTCP.ReconnectAttempts++; s.mu.Unlock() }
func (s *DriverState) incRTUReconnect() { s.mu.Lock(); s.modbusRTU.ReconnectAttempts++; s.mu.Unlock() }
func (s *DriverState) setMQTTStatus(ok bool, errStr string) { s.mu.Lock(); s.mqtt.OK = ok; if errStr != "" { s.mqtt.LastError = errStr; s.lastError = "mqtt: " + errStr }; s.mqtt.LastUpdateTs = time.Now().Format(time.RFC3339); s.mu.Unlock() }
func (s *DriverState) incMQTTReconnect() { s.mu.Lock(); s.mqtt.ReconnectAttempts++; s.mu.Unlock() }

func runModbusTCPCollector(ctx context.Context, state *DriverState) {
	cfg := state.cfg
	backoff := time.Duration(cfg.RetryInitialBackoffMs) * time.Millisecond
	maxBackoff := time.Duration(cfg.RetryMaxBackoffMs) * time.Millisecond

	for ctx.Err() == nil {
		h := modbus.NewTCPClientHandler(cfg.ModbusTCPAddr)
		h.Timeout = time.Duration(cfg.RequestTimeoutMs) * time.Millisecond
		h.SlaveId = cfg.ModbusTCPUnitID
		log.Printf("[TCP] Connecting to %s (unit %d)", cfg.ModbusTCPAddr, cfg.ModbusTCPUnitID)
		if err := h.Connect(); err != nil {
			state.setTCPStatus(false, err.Error(), false)
			state.incTCPReconnect()
			log.Printf("[TCP] connect failed: %v, retry in %v", err, backoff)
			if !sleepCtx(ctx, backoff) { return }
			backoff = time.Duration(math.Min(float64(maxBackoff), float64(backoff)*cfg.RetryMultiplier))
			continue
		}
		state.setTCPStatus(true, "", false)
		state.incTCPReconnect()
		log.Printf("[TCP] Connected")
		backoff = time.Duration(cfg.RetryInitialBackoffMs) * time.Millisecond
		client := modbus.NewClient(h)

		// Optional writes on connect
		if cfg.WriteCoilEnable {
			val := uint16(0x0000)
			if cfg.WriteCoilValue { val = 0xFF00 }
			if _, err := client.WriteSingleCoil(cfg.WriteCoilAddr, val); err != nil {
				log.Printf("[TCP] write coil addr=%d val=%t error=%v", cfg.WriteCoilAddr, cfg.WriteCoilValue, err)
				state.setTCPStatus(true, "write coil: "+err.Error(), false)
			} else {
				log.Printf("[TCP] write coil addr=%d val=%t OK", cfg.WriteCoilAddr, cfg.WriteCoilValue)
			}
		}
		if cfg.WriteRegEnable {
			if _, err := client.WriteSingleRegister(cfg.WriteRegAddr, cfg.WriteRegValue); err != nil {
				log.Printf("[TCP] write reg addr=%d val=%d error=%v", cfg.WriteRegAddr, cfg.WriteRegValue, err)
				state.setTCPStatus(true, "write reg: "+err.Error(), false)
			} else {
				log.Printf("[TCP] write reg addr=%d val=%d OK", cfg.WriteRegAddr, cfg.WriteRegValue)
			}
		}

		ticker := time.NewTicker(time.Duration(cfg.UpdateIntervalMs) * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				_ = h.Close()
				return
			case <-ticker.C:
				if err := performReads(client, cfg, "TCP"); err != nil {
					state.setTCPStatus(false, err.Error(), false)
					_ = h.Close()
					log.Printf("[TCP] read error: %v, reconnecting...", err)
					break
				} else {
					state.setTCPStatus(true, "", true)
				}
			}
		}
	}
}

func runModbusRTUCollector(ctx context.Context, state *DriverState) {
	cfg := state.cfg
	backoff := time.Duration(cfg.RetryInitialBackoffMs) * time.Millisecond
	maxBackoff := time.Duration(cfg.RetryMaxBackoffMs) * time.Millisecond

	for ctx.Err() == nil {
		h := modbus.NewRTUClientHandler(cfg.ModbusRTUPort)
		h.BaudRate = cfg.ModbusRTUBaud
		h.DataBits = cfg.ModbusRTUDataBits
		h.Parity = cfg.ModbusRTUParity
		h.StopBits = cfg.ModbusRTUStopBits
		h.Timeout = time.Duration(cfg.RequestTimeoutMs) * time.Millisecond
		h.SlaveId = cfg.ModbusRTUUnitID
		log.Printf("[RTU] Connecting to %s (baud=%d parity=%s stop=%d unit=%d)", cfg.ModbusRTUPort, cfg.ModbusRTUBaud, cfg.ModbusRTUParity, cfg.ModbusRTUStopBits, cfg.ModbusRTUUnitID)
		if err := h.Connect(); err != nil {
			state.setRTUStatus(false, err.Error(), false)
			state.incRTUReconnect()
			log.Printf("[RTU] connect failed: %v, retry in %v", err, backoff)
			if !sleepCtx(ctx, backoff) { return }
			backoff = time.Duration(math.Min(float64(maxBackoff), float64(backoff)*cfg.RetryMultiplier))
			continue
		}
		state.setRTUStatus(true, "", false)
		state.incRTUReconnect()
		log.Printf("[RTU] Connected")
		backoff = time.Duration(cfg.RetryInitialBackoffMs) * time.Millisecond
		client := modbus.NewClient(h)

		// Optional writes on connect
		if cfg.WriteCoilEnable {
			val := uint16(0x0000)
			if cfg.WriteCoilValue { val = 0xFF00 }
			if _, err := client.WriteSingleCoil(cfg.WriteCoilAddr, val); err != nil {
				log.Printf("[RTU] write coil addr=%d val=%t error=%v", cfg.WriteCoilAddr, cfg.WriteCoilValue, err)
				state.setRTUStatus(true, "write coil: "+err.Error(), false)
			} else {
				log.Printf("[RTU] write coil addr=%d val=%t OK", cfg.WriteCoilAddr, cfg.WriteCoilValue)
			}
		}
		if cfg.WriteRegEnable {
			if _, err := client.WriteSingleRegister(cfg.WriteRegAddr, cfg.WriteRegValue); err != nil {
				log.Printf("[RTU] write reg addr=%d val=%d error=%v", cfg.WriteRegAddr, cfg.WriteRegValue, err)
				state.setRTUStatus(true, "write reg: "+err.Error(), false)
			} else {
				log.Printf("[RTU] write reg addr=%d val=%d OK", cfg.WriteRegAddr, cfg.WriteRegValue)
			}
		}

		ticker := time.NewTicker(time.Duration(cfg.UpdateIntervalMs) * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				_ = h.Close()
				return
			case <-ticker.C:
				if err := performReads(client, cfg, "RTU"); err != nil {
					state.setRTUStatus(false, err.Error(), false)
					_ = h.Close()
					log.Printf("[RTU] read error: %v, reconnecting...", err)
					break
				} else {
					state.setRTUStatus(true, "", true)
				}
			}
		}
	}
}

func performReads(client modbus.Client, cfg Config, tag string) error {
	// Read coils
	if cfg.ReadCoilsCount > 0 {
		b, err := client.ReadCoils(cfg.ReadCoilsAddr, cfg.ReadCoilsCount)
		if err != nil { return err }
		coils := parseBits(b, int(cfg.ReadCoilsCount))
		log.Printf("[%s] coils@%d x%d => %v", tag, cfg.ReadCoilsAddr, cfg.ReadCoilsCount, coils)
	}
	// Read discrete inputs
	if cfg.ReadDiscreteCount > 0 {
		b, err := client.ReadDiscreteInputs(cfg.ReadDiscreteAddr, cfg.ReadDiscreteCount)
		if err != nil { return err }
		di := parseBits(b, int(cfg.ReadDiscreteCount))
		log.Printf("[%s] discrete@%d x%d => %v", tag, cfg.ReadDiscreteAddr, cfg.ReadDiscreteCount, di)
	}
	// Read input registers
	if cfg.ReadInputRegCount > 0 {
		b, err := client.ReadInputRegisters(cfg.ReadInputRegAddr, cfg.ReadInputRegCount)
		if err != nil { return err }
		irs := bytesToUint16s(b)
		log.Printf("[%s] inputReg@%d x%d => %v", tag, cfg.ReadInputRegAddr, cfg.ReadInputRegCount, irs)
	}
	// Read holding registers
	if cfg.ReadHoldingRegCount > 0 {
		b, err := client.ReadHoldingRegisters(cfg.ReadHoldingRegAddr, cfg.ReadHoldingRegCount)
		if err != nil { return err }
		hrs := bytesToUint16s(b)
		log.Printf("[%s] holdingReg@%d x%d => %v", tag, cfg.ReadHoldingRegAddr, cfg.ReadHoldingRegCount, hrs)
	}
	return nil
}

func parseBits(b []byte, count int) []bool {
	res := make([]bool, count)
	for i := 0; i < count; i++ {
		byteIndex := i / 8
		bitIndex := uint(i % 8)
		if byteIndex < len(b) {
			res[i] = ((b[byteIndex] >> bitIndex) & 0x01) == 0x01
		}
	}
	return res
}

func bytesToUint16s(b []byte) []uint16 {
	res := make([]uint16, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		res[i/2] = binary.BigEndian.Uint16(b[i : i+2])
	}
	return res
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func strconvI(i int) string { return fmtInt(i) }

func fmtInt(i int) string { return strconvFormatInt(int64(i)) }

func strconvFormatInt(i int64) string { return (func() string { return fmtS(i) })() }

// Minimal inline formatting to avoid importing strconv and fmt redundantly
func fmtS(i int64) string {
	// simple base 10 int to string
	if i == 0 { return "0" }
	neg := i < 0
	if neg { i = -i }
	buf := make([]byte, 0, 20)
	for i > 0 {
		buf = append(buf, byte('0'+(i%10)))
		i /= 10
	}
	// reverse
	for l, r := 0, len(buf)-1; l < r; l, r = l+1, r-1 {
		buf[l], buf[r] = buf[r], buf[l]
	}
	if neg { return "-" + string(buf) }
	return string(buf)
}

func runMQTTClient(ctx context.Context, state *DriverState) {
	cfg := state.cfg
	opts := mqtt.NewClientOptions().AddBroker(cfg.MQTTBrokerURL).SetClientID(cfg.MQTTClientID).SetAutoReconnect(true)
	if cfg.MQTTUsername != "" { opts.SetUsername(cfg.MQTTUsername) }
	if cfg.MQTTPassword != "" { opts.SetPassword(cfg.MQTTPassword) }
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("[MQTT] connection lost: %v", err)
		state.setMQTTStatus(false, err.Error())
		state.incMQTTReconnect()
	})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		log.Printf("[MQTT] connected to %s", cfg.MQTTBrokerURL)
		state.setMQTTStatus(true, "")
		if cfg.MQTTTopic != "" {
			if token := c.Subscribe(cfg.MQTTTopic, 0, func(_ mqtt.Client, msg mqtt.Message) {
				log.Printf("[MQTT] msg topic=%s len=%d", msg.Topic(), len(msg.Payload()))
				state.setMQTTStatus(true, "")
			}); token.Wait() && token.Error() != nil {
				log.Printf("[MQTT] subscribe error: %v", token.Error())
				state.setMQTTStatus(true, token.Error().Error())
			}
		}
	})
	client := mqtt.NewClient(opts)
	for ctx.Err() == nil {
		log.Printf("[MQTT] connecting...")
		token := client.Connect()
		if token.Wait() && token.Error() != nil {
			log.Printf("[MQTT] connect error: %v", token.Error())
			state.setMQTTStatus(false, token.Error().Error())
			state.incMQTTReconnect()
			if !sleepCtx(ctx, time.Duration(cfg.RetryInitialBackoffMs)*time.Millisecond) { return }
			continue
		}
		// Wait for context cancel
		<-ctx.Done()
		break
	}
	if client.IsConnected() { client.Disconnect(250) }
}
