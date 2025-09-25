package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// HTTP
	HTTPHost string
	HTTPPort int

	// Protocol selection: "tcp" or "rtu"
	Protocol string

	// Modbus TCP
	ModbusTCPAddr string // host:port
	TCPTimeout    time.Duration

	// Modbus RTU
	SerialPort string
	BaudRate   int
	DataBits   int
	Parity     string // N/E/O
	StopBits   int    // 1/2

	// Common Modbus
	SlaveID uint8

	// Acquisition
	PollInterval time.Duration

	// Read ranges
	ReadHoldingStart   int
	ReadHoldingCount   int
	ReadInputStart     int
	ReadInputCount     int
	ReadCoilStart      int
	ReadCoilCount      int
	ReadDiscreteStart  int
	ReadDiscreteCount  int

	// Retry/backoff
	RetryInitial time.Duration
	RetryMax     time.Duration

	// MQTT (optional)
	MQTTBroker    string
	MQTTClientID  string
	MQTTUsername  string
	MQTTPassword  string
	MQTTTopic     string
	MQTTKeepAlive time.Duration
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func atoiEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return i, nil
}

func durMsEnv(key string, defMs int) (time.Duration, error) {
	i, err := atoiEnv(key, defMs)
	if err != nil {
		return 0, err
	}
	return time.Duration(i) * time.Millisecond, nil
}

func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{}

	cfg.HTTPHost = getenv("HTTP_HOST", "0.0.0.0")
	port, err := atoiEnv("HTTP_PORT", 8080)
	if err != nil {
		return nil, err
	}
	cfg.HTTPPort = port

	cfg.Protocol = strings.ToLower(getenv("PROTOCOL", ""))
	cfg.ModbusTCPAddr = getenv("MODBUS_TCP_ADDR", "")

	tcpTimeout, err := durMsEnv("TCP_TIMEOUT_MS", 1000)
	if err != nil {
		return nil, err
	}
	cfg.TCPTimeout = tcpTimeout

	cfg.SerialPort = getenv("SERIAL_PORT", "")
	baud, err := atoiEnv("BAUD_RATE", 9600)
	if err != nil {
		return nil, err
	}
	cfg.BaudRate = baud
	dataBits, err := atoiEnv("DATA_BITS", 8)
	if err != nil {
		return nil, err
	}
	cfg.DataBits = dataBits
	cfg.Parity = strings.ToUpper(getenv("PARITY", "N"))
	stopBits, err := atoiEnv("STOP_BITS", 1)
	if err != nil {
		return nil, err
	}
	cfg.StopBits = stopBits

	sid, err := atoiEnv("SLAVE_ID", 1)
	if err != nil {
		return nil, err
	}
	if sid < 0 || sid > 247 {
		return nil, errors.New("SLAVE_ID must be in 0..247")
	}
	cfg.SlaveID = uint8(sid)

	poll, err := durMsEnv("POLL_INTERVAL_MS", 1000)
	if err != nil {
		return nil, err
	}
	// enforce update interval <= 1s
	if poll > 1000*time.Millisecond {
		poll = 1000 * time.Millisecond
	}
	if poll < 50*time.Millisecond {
		poll = 50 * time.Millisecond
	}
	cfg.PollInterval = poll

	cfg.ReadHoldingStart, err = atoiEnv("READ_HOLDING_START", 0)
	if err != nil {
		return nil, err
	}
	cfg.ReadHoldingCount, err = atoiEnv("READ_HOLDING_COUNT", 10)
	if err != nil {
		return nil, err
	}
	cfg.ReadInputStart, err = atoiEnv("READ_INPUT_START", 0)
	if err != nil {
		return nil, err
	}
	cfg.ReadInputCount, err = atoiEnv("READ_INPUT_COUNT", 0)
	if err != nil {
		return nil, err
	}
	cfg.ReadCoilStart, err = atoiEnv("READ_COIL_START", 0)
	if err != nil {
		return nil, err
	}
	cfg.ReadCoilCount, err = atoiEnv("READ_COIL_COUNT", 0)
	if err != nil {
		return nil, err
	}
	cfg.ReadDiscreteStart, err = atoiEnv("READ_DISCRETE_START", 0)
	if err != nil {
		return nil, err
	}
	cfg.ReadDiscreteCount, err = atoiEnv("READ_DISCRETE_COUNT", 0)
	if err != nil {
		return nil, err
	}

	retryInit, err := durMsEnv("RETRY_INITIAL_MS", 500)
	if err != nil {
		return nil, err
	}
	retryMax, err := durMsEnv("RETRY_MAX_MS", 10000)
	if err != nil {
		return nil, err
	}
	if retryMax < retryInit {
		retryMax = retryInit
	}
	cfg.RetryInitial = retryInit
	cfg.RetryMax = retryMax

	cfg.MQTTBroker = getenv("MQTT_BROKER", "")
	cfg.MQTTClientID = getenv("MQTT_CLIENT_ID", "")
	cfg.MQTTUsername = getenv("MQTT_USERNAME", "")
	cfg.MQTTPassword = getenv("MQTT_PASSWORD", "")
	cfg.MQTTTopic = getenv("MQTT_TOPIC", "")
	kaSec, err := atoiEnv("MQTT_KEEPALIVE_S", 30)
	if err != nil {
		return nil, err
	}
	cfg.MQTTKeepAlive = time.Duration(kaSec) * time.Second

	// Auto-detect protocol if not specified
	if cfg.Protocol == "" {
		if cfg.ModbusTCPAddr != "" {
			cfg.Protocol = "tcp"
		} else if cfg.SerialPort != "" {
			cfg.Protocol = "rtu"
		} else {
			return nil, errors.New("must set PROTOCOL or provide MODBUS_TCP_ADDR or SERIAL_PORT")
		}
	}
	if cfg.Protocol != "tcp" && cfg.Protocol != "rtu" {
		return nil, errors.New("PROTOCOL must be 'tcp' or 'rtu'")
	}

	if cfg.Protocol == "tcp" && cfg.ModbusTCPAddr == "" {
		return nil, errors.New("MODBUS_TCP_ADDR required for tcp protocol")
	}
	if cfg.Protocol == "rtu" && cfg.SerialPort == "" {
		return nil, errors.New("SERIAL_PORT required for rtu protocol")
	}

	if strings.IndexByte("NEO", cfg.Parity[0]) == -1 {
		return nil, errors.New("PARITY must be N/E/O")
	}
	if cfg.StopBits != 1 && cfg.StopBits != 2 {
		return nil, errors.New("STOP_BITS must be 1 or 2")
	}

	return cfg, nil
}
