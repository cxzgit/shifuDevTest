package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPHost string
	HTTPPort string

	// Modbus TCP
	ModbusTCPAddr   string // host:port
	ModbusUnitID    byte
	RequestTimeout  time.Duration
	AcqInterval     time.Duration
	BackoffInitial  time.Duration
	BackoffMax      time.Duration
	BufferSize      int
	OnlineTTLSec    int

	// Publish interval via Modbus (optional)
	ModbusPubIntervalReg   *uint16
	ModbusPubIntervalScale int

	// MQTT (optional, for /config/publish)
	MQTTBroker      string // e.g., tcp://localhost:1883
	MQTTClientID    string
	MQTTUsername    string
	MQTTPassword    string
	MQTTCommandTopic string // e.g., device/cmd/config
}

func LoadConfig() (*Config, error) {
	cfg := &Config{}
	cfg.HTTPHost = getEnv("HTTP_HOST", "0.0.0.0")
	cfg.HTTPPort = getEnv("HTTP_PORT", "8080")

	cfg.ModbusTCPAddr = os.Getenv("MODBUS_TCP_ADDR")
	if cfg.ModbusTCPAddr == "" {
		return nil, errors.New("MODBUS_TCP_ADDR is required")
	}
	unit := getEnv("MODBUS_UNIT_ID", "1")
	uid64, err := strconv.ParseUint(unit, 10, 8)
	if err != nil {
		return nil, fmt.Errorf("invalid MODBUS_UNIT_ID: %w", err)
	}
	cfg.ModbusUnitID = byte(uid64)

	rt := getEnv("REQUEST_TIMEOUT_MS", "1000")
	rtMs, err := strconv.Atoi(rt)
	if err != nil || rtMs <= 0 {
		return nil, fmt.Errorf("invalid REQUEST_TIMEOUT_MS: %v", rt)
	}
	cfg.RequestTimeout = time.Duration(rtMs) * time.Millisecond

	acq := getEnv("ACQ_INTERVAL_MS", "200")
	acqMs, err := strconv.Atoi(acq)
	if err != nil || acqMs <= 0 {
		return nil, fmt.Errorf("invalid ACQ_INTERVAL_MS: %v", acq)
	}
	cfg.AcqInterval = time.Duration(acqMs) * time.Millisecond

	bi := getEnv("BACKOFF_INITIAL_MS", "500")
	biMs, err := strconv.Atoi(bi)
	if err != nil || biMs <= 0 {
		return nil, fmt.Errorf("invalid BACKOFF_INITIAL_MS: %v", bi)
	}
	cfg.BackoffInitial = time.Duration(biMs) * time.Millisecond

	bm := getEnv("BACKOFF_MAX_MS", "10000")
	bmMs, err := strconv.Atoi(bm)
	if err != nil || bmMs <= 0 {
		return nil, fmt.Errorf("invalid BACKOFF_MAX_MS: %v", bm)
	}
	cfg.BackoffMax = time.Duration(bmMs) * time.Millisecond

	bs := getEnv("BUFFER_SIZE", "256")
	bsN, err := strconv.Atoi(bs)
	if err != nil || bsN <= 0 {
		return nil, fmt.Errorf("invalid BUFFER_SIZE: %v", bs)
	}
	cfg.BufferSize = bsN

	ot := getEnv("ONLINE_TTL_SEC", "5")
	otN, err := strconv.Atoi(ot)
	if err != nil || otN <= 0 {
		return nil, fmt.Errorf("invalid ONLINE_TTL_SEC: %v", ot)
	}
	cfg.OnlineTTLSec = otN

	// Optional: Modbus register for publish interval
	if regStr := os.Getenv("MODBUS_PUB_INTERVAL_REG"); regStr != "" {
		regVal, err := strconv.ParseUint(regStr, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid MODBUS_PUB_INTERVAL_REG: %v", regStr)
		}
		reg := uint16(regVal)
		cfg.ModbusPubIntervalReg = &reg
	}
	scaleStr := getEnv("MODBUS_PUB_INTERVAL_SCALE", "1")
	scale, err := strconv.Atoi(scaleStr)
	if err != nil || scale <= 0 {
		return nil, fmt.Errorf("invalid MODBUS_PUB_INTERVAL_SCALE: %v", scaleStr)
	}
	cfg.ModbusPubIntervalScale = scale

	// MQTT optional
	cfg.MQTTBroker = os.Getenv("MQTT_BROKER")
	cfg.MQTTClientID = getEnv("MQTT_CLIENT_ID", "daq-driver")
	cfg.MQTTUsername = os.Getenv("MQTT_USERNAME")
	cfg.MQTTPassword = os.Getenv("MQTT_PASSWORD")
	cfg.MQTTCommandTopic = os.Getenv("MQTT_COMMAND_TOPIC")

	return cfg, nil
}

func getEnv(key, def string) string { v := os.Getenv(key); if v == "" { return def }; return v }
