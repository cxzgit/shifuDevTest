package main

import (
	contextpkg "context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/goburrow/modbus"
)

type ErrorEntry struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

type PollingRanges struct {
	Holding   Range `json:"holding"`
	Input     Range `json:"input"`
	Coil      Range `json:"coil"`
	Discrete  Range `json:"discrete"`
}

type Range struct {
	Start int `json:"start"`
	Count int `json:"count"`
}

type Status struct {
	DeviceName     string `json:"device_name"`
	DeviceModel    string `json:"device_model"`
	Manufacturer   string `json:"manufacturer"`

	ProtocolInUse      string `json:"protocol_in_use"`
	NetworkLinkState   string `json:"network_link_state"`
	ModbusTCPReady     bool   `json:"modbus_tcp_ready"`
	ModbusRTUReady     bool   `json:"modbus_rtu_ready"`
	MQTTConnected      bool   `json:"mqtt_connected"`
	AcquisitionStatus  string `json:"acquisition_status"`
	UpdateIntervalMS   int    `json:"update_interval_ms"`
	LastSampleTS       string `json:"last_sample_timestamp"`
	SlaveID            uint8  `json:"slave_id"`
	Polling            PollingRanges `json:"polling_targets"`
	ReconnectAttempts1m int    `json:"reconnect_attempts_1m"`
	LastReconnectTS    string `json:"last_reconnect_timestamp"`
	RecentErrors       []ErrorEntry `json:"recent_errors"`
}

type lastSample struct {
	Time    time.Time
	Holding []byte
	Input   []byte
	Coil    []byte
	Discrete []byte
}

type DeviceManager struct {
	cfg *Config

	mu           sync.RWMutex
	modbusClient modbus.Client
	tcpHandler   *modbus.TCPClientHandler
	rtuHandler   *modbus.RTUClientHandler

	connectedTCP bool
	connectedRTU bool
	acquiring    bool
	lastSample   lastSample

	recentErrors []ErrorEntry
	maxErrors    int

	reconnectTimes []time.Time
	lastReconnect  time.Time

	mqttClient   mqtt.Client
	mqttConnected bool

	ctx    contextpkg.Context
	cancel contextpkg.CancelFunc

	wg sync.WaitGroup
}

func NewDeviceManager(cfg *Config) *DeviceManager {
	return &DeviceManager{
		cfg:         cfg,
		maxErrors:   20,
		reconnectTimes: make([]time.Time, 0, 64),
	}
}

func (d *DeviceManager) Start(ctx contextpkg.Context) error {
	ctx, cancel := contextpkg.WithCancel(ctx)
	d.ctx = ctx
	d.cancel = cancel

	// Start Modbus acquisition loop
	d.wg.Add(1)
	go d.acquisitionLoop()

	// Start MQTT connection maintenance if configured
	if d.cfg.MQTTBroker != "" {
		d.wg.Add(1)
		go d.mqttLoop()
	}
	return nil
}

func (d *DeviceManager) Stop(ctx contextpkg.Context) error {
	if d.cancel != nil {
		d.cancel()
	}
	c := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(c)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c:
	}
	// Close handlers
	if d.tcpHandler != nil {
		_ = d.tcpHandler.Close()
	}
	if d.rtuHandler != nil {
		_ = d.rtuHandler.Close()
	}
	if d.mqttClient != nil && d.mqttClient.IsConnectionOpen() {
		d.mqttClient.Disconnect(250)
	}
	return nil
}

func (d *DeviceManager) SnapshotStatus() Status {
	d.mu.RLock()
	defer d.mu.RUnlock()

	st := Status{
		DeviceName:     "Industrial IoT edge data acquisition module",
		DeviceModel:    "DA-01",
		Manufacturer:   "Honeywell",
		ProtocolInUse:  d.cfg.Protocol,
		NetworkLinkState: d.networkStateLocked(),
		ModbusTCPReady: d.connectedTCP,
		ModbusRTUReady: d.connectedRTU,
		MQTTConnected:  d.mqttConnected,
		AcquisitionStatus: func() string { if d.acquiring { return "running" } else { return "stopped" } }(),
		UpdateIntervalMS: int(d.cfg.PollInterval / time.Millisecond),
		LastSampleTS:     d.lastSample.Time.Format(time.RFC3339Nano),
		SlaveID:          d.cfg.SlaveID,
		Polling: PollingRanges{
			Holding:  Range{Start: d.cfg.ReadHoldingStart, Count: d.cfg.ReadHoldingCount},
			Input:    Range{Start: d.cfg.ReadInputStart, Count: d.cfg.ReadInputCount},
			Coil:     Range{Start: d.cfg.ReadCoilStart, Count: d.cfg.ReadCoilCount},
			Discrete: Range{Start: d.cfg.ReadDiscreteStart, Count: d.cfg.ReadDiscreteCount},
		},
		ReconnectAttempts1m: d.reconnectCountLocked(1 * time.Minute),
		LastReconnectTS:     func() string { if d.lastReconnect.IsZero() { return "" } else { return d.lastReconnect.Format(time.RFC3339Nano) } }(),
		RecentErrors:        append([]ErrorEntry(nil), d.recentErrors...),
	}
	return st
}

func (d *DeviceManager) networkStateLocked() string {
	if d.cfg.Protocol == "tcp" {
		if d.connectedTCP { return "up" }
		return "down"
	}
	if d.cfg.Protocol == "rtu" {
		if d.connectedRTU { return "up" }
		return "down"
	}
	return "unknown"
}

func (d *DeviceManager) acquisitionLoop() {
	defer d.wg.Done()
	ctx := d.ctx
	cfg := d.cfg

	backoff := cfg.RetryInitial
	if backoff <= 0 { backoff = 500 * time.Millisecond }
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Ensure connection
		if err := d.ensureConnected(); err != nil {
			d.recordError(fmt.Errorf("connect failed: %w", err))
			d.recordReconnect()
			logJSON("warn", "modbus_connect_retry", map[string]any{"error": err.Error(), "backoff_ms": backoff.Milliseconds()})
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < cfg.RetryMax { backoff *= 2; if backoff > cfg.RetryMax { backoff = cfg.RetryMax } }
			continue
		}
		backoff = cfg.RetryInitial

		// Poll loop
		d.setAcquiring(true)
		if err := d.pollOnce(); err != nil {
			d.recordError(fmt.Errorf("poll failed: %w", err))
			// Force reconnect next iteration
			d.disconnect()
			continue
		}
		select {
		case <-time.After(cfg.PollInterval):
		case <-ctx.Done():
			return
		}
	}
}

func (d *DeviceManager) ensureConnected() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	switch d.cfg.Protocol {
	case "tcp":
		if d.connectedTCP && d.tcpHandler != nil && d.modbusClient != nil {
			return nil
		}
		if d.tcpHandler != nil {
			_ = d.tcpHandler.Close()
		}
		h := modbus.NewTCPClientHandler(d.cfg.ModbusTCPAddr)
		h.Timeout = d.cfg.TCPTimeout
		h.SlaveId = d.cfg.SlaveID
		if err := h.Connect(); err != nil {
			return err
		}
		d.tcpHandler = h
		d.modbusClient = modbus.NewClient(h)
		d.connectedTCP = true
		d.connectedRTU = false
		logJSON("info", "modbus_tcp_connected", map[string]any{"addr": d.cfg.ModbusTCPAddr, "slave_id": d.cfg.SlaveID})
		return nil
	case "rtu":
		if d.connectedRTU && d.rtuHandler != nil && d.modbusClient != nil {
			return nil
		}
		if d.rtuHandler != nil {
			_ = d.rtuHandler.Close()
		}
		h := modbus.NewRTUClientHandler(d.cfg.SerialPort)
		h.BaudRate = d.cfg.BaudRate
		h.DataBits = d.cfg.DataBits
		h.Parity = d.cfg.Parity
		h.StopBits = d.cfg.StopBits
		h.Timeout = d.cfg.TCPTimeout
		h.SlaveId = d.cfg.SlaveID
		if err := h.Connect(); err != nil {
			return err
		}
		d.rtuHandler = h
		d.modbusClient = modbus.NewClient(h)
		d.connectedRTU = true
		d.connectedTCP = false
		logJSON("info", "modbus_rtu_connected", map[string]any{"port": d.cfg.SerialPort, "baud": d.cfg.BaudRate, "parity": d.cfg.Parity, "stop_bits": d.cfg.StopBits, "slave_id": d.cfg.SlaveID})
		return nil
	default:
		return errors.New("unsupported protocol")
	}
}

func (d *DeviceManager) disconnect() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tcpHandler != nil {
		_ = d.tcpHandler.Close()
	}
	if d.rtuHandler != nil {
		_ = d.rtuHandler.Close()
	}
	if d.connectedTCP {
		logJSON("warn", "modbus_tcp_disconnected", nil)
	}
	if d.connectedRTU {
		logJSON("warn", "modbus_rtu_disconnected", nil)
	}
	d.connectedTCP = false
	d.connectedRTU = false
	d.modbusClient = nil
	d.acquiring = false
}

func (d *DeviceManager) pollOnce() error {
	d.mu.RLock()
	client := d.modbusClient
	cfg := d.cfg
	d.mu.RUnlock()

	if client == nil {
		return errors.New("no modbus client")
	}

	var (
		err error
		holding, input, coil, discrete []byte
	)

	// Read ranges conditionally
	if cfg.ReadHoldingCount > 0 {
		holding, err = client.ReadHoldingRegisters(uint16(cfg.ReadHoldingStart), uint16(cfg.ReadHoldingCount))
		if err != nil { return fmt.Errorf("read holding: %w", err) }
	}
	if cfg.ReadInputCount > 0 {
		input, err = client.ReadInputRegisters(uint16(cfg.ReadInputStart), uint16(cfg.ReadInputCount))
		if err != nil { return fmt.Errorf("read input: %w", err) }
	}
	if cfg.ReadCoilCount > 0 {
		coil, err = client.ReadCoils(uint16(cfg.ReadCoilStart), uint16(cfg.ReadCoilCount))
		if err != nil { return fmt.Errorf("read coil: %w", err) }
	}
	if cfg.ReadDiscreteCount > 0 {
		discrete, err = client.ReadDiscreteInputs(uint16(cfg.ReadDiscreteStart), uint16(cfg.ReadDiscreteCount))
		if err != nil { return fmt.Errorf("read discrete: %w", err) }
	}

	d.mu.Lock()
	d.lastSample = lastSample{
		Time:     time.Now(),
		Holding:  append([]byte(nil), holding...),
		Input:    append([]byte(nil), input...),
		Coil:     append([]byte(nil), coil...),
		Discrete: append([]byte(nil), discrete...),
	}
	d.mu.Unlock()

	logJSON("info", "poll_success", map[string]any{
		"ts": time.Now().Format(time.RFC3339Nano),
		"holding_len": len(holding),
		"input_len": len(input),
		"coil_len": len(coil),
		"discrete_len": len(discrete),
		"holding_hex": shortHex(holding),
	})
	return nil
}

func shortHex(b []byte) string {
	if len(b) == 0 { return "" }
	max := 16
	if len(b) < max { max = len(b) }
	return hex.EncodeToString(b[:max])
}

func (d *DeviceManager) setAcquiring(v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.acquiring = v
}

func (d *DeviceManager) recordError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := ErrorEntry{Time: time.Now(), Message: err.Error()}
	d.recentErrors = append(d.recentErrors, entry)
	if len(d.recentErrors) > d.maxErrors {
		// drop oldest
		copy(d.recentErrors[0:], d.recentErrors[1:])
		d.recentErrors = d.recentErrors[:d.maxErrors]
	}
	logJSON("error", "device_error", map[string]any{"error": err.Error()})
}

func (d *DeviceManager) recordReconnect() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReconnect = time.Now()
	d.reconnectTimes = append(d.reconnectTimes, d.lastReconnect)
	// Trim history older than 10 minutes to bound memory
	cut := time.Now().Add(-10 * time.Minute)
	i := 0
	for _, t := range d.reconnectTimes {
		if t.After(cut) {
			d.reconnectTimes[i] = t
			i++
		}
	}
	d.reconnectTimes = d.reconnectTimes[:i]
}

func (d *DeviceManager) reconnectCountLocked(win time.Duration) int {
	cut := time.Now().Add(-win)
	c := 0
	for _, t := range d.reconnectTimes {
		if t.After(cut) { c++ }
	}
	return c
}

// Optional: write operations supported internally
func (d *DeviceManager) WriteSingleRegister(addr uint16, value uint16) error {
	d.mu.RLock()
	client := d.modbusClient
	d.mu.RUnlock()
	if client == nil { return errors.New("no modbus client") }
	_, err := client.WriteSingleRegister(addr, value)
	return err
}

func (d *DeviceManager) WriteSingleCoil(addr uint16, on bool) error {
	d.mu.RLock()
	client := d.modbusClient
	d.mu.RUnlock()
	if client == nil { return errors.New("no modbus client") }
	var v uint16 = 0x0000
	if on { v = 0xFF00 }
	_, err := client.WriteSingleCoil(addr, v)
	return err
}

func (d *DeviceManager) mqttLoop() {
	defer d.wg.Done()
	cfg := d.cfg
	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.MQTTBroker)
	if cfg.MQTTClientID != "" {
		opts.SetClientID(cfg.MQTTClientID)
	}
	if cfg.MQTTUsername != "" || cfg.MQTTPassword != "" {
		opts.SetUsername(cfg.MQTTUsername)
		opts.SetPassword(cfg.MQTTPassword)
	}
	opts.SetCleanSession(true)
	opts.SetKeepAlive(cfg.MQTTKeepAlive)
	opts.SetAutoReconnect(true)
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		d.mu.Lock()
		d.mqttConnected = true
		d.mu.Unlock()
		logJSON("info", "mqtt_connected", map[string]any{"broker": cfg.MQTTBroker})
		if cfg.MQTTTopic != "" {
			if token := c.Subscribe(cfg.MQTTTopic, 0, func(_ mqtt.Client, msg mqtt.Message) {
				// We only track connectivity; optionally record last-message timestamp as a diagnostic
				logJSON("info", "mqtt_msg", map[string]any{"topic": msg.Topic(), "len": len(msg.Payload())})
			}); token.Wait() && token.Error() != nil {
				logJSON("error", "mqtt_subscribe_error", map[string]any{"error": token.Error().Error()})
			}
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		d.mu.Lock(); d.mqttConnected = false; d.mu.Unlock()
		logJSON("warn", "mqtt_disconnected", map[string]any{"error": err.Error()})
	})

	client := mqtt.NewClient(opts)
	d.mqttClient = client

	for {
		if token := client.Connect(); token.Wait() && token.Error() != nil {
			logJSON("error", "mqtt_connect_error", map[string]any{"error": token.Error().Error()})
			select {
			case <-time.After(2 * time.Second):
			case <-d.ctx.Done():
				return
			}
		} else {
			break
		}
	}

	// Wait until context done
	<-d.ctx.Done()
}

// Marshal status as JSON for logs when needed
func (s Status) String() string {
	b, _ := json.Marshal(s)
	return string(b)
}
