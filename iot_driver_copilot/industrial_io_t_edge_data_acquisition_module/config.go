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
	HTTPHost             string
	HTTPPort             int

	Protocol             string // tcp|rtu
	TCPHost              string
	TCPPort              int

	SerialPort           string
	BaudRate             int
	Parity               string // N|E|O
	StopBits             int    // 1|2
	DataBits             int

	SlaveID              uint8
	Table                string // coil|discrete|input|holding
	Address              uint16
	Quantity             uint16

	Timeout              time.Duration
	PollInterval         time.Duration
	RetryMax             int
	RetryBaseBackoff     time.Duration
	RetryMaxBackoff      time.Duration
}

func LoadConfigFromEnv() (Config, error) {
	var cfg Config
	var err error

	cfg.HTTPHost, err = mustGetString("HTTP_HOST")
	if err != nil {
		return cfg, err
	}
	cfg.HTTPPort, err = mustGetInt("HTTP_PORT")
	if err != nil {
		return cfg, err
	}

	cfg.Protocol, err = mustGetString("MODBUS_PROTOCOL")
	if err != nil {
		return cfg, err
	}
	cfg.Protocol = strings.ToLower(cfg.Protocol)
	if cfg.Protocol != "tcp" && cfg.Protocol != "rtu" {
		return cfg, fmt.Errorf("MODBUS_PROTOCOL must be 'tcp' or 'rtu', got %s", cfg.Protocol)
	}

	if cfg.Protocol == "tcp" {
		cfg.TCPHost, err = mustGetString("MODBUS_TCP_HOST")
		if err != nil { return cfg, err }
		cfg.TCPPort, err = mustGetInt("MODBUS_TCP_PORT")
		if err != nil { return cfg, err }
	} else {
		cfg.SerialPort, err = mustGetString("MODBUS_RTU_SERIAL_PORT")
		if err != nil { return cfg, err }
		cfg.BaudRate, err = mustGetInt("MODBUS_RTU_BAUD")
		if err != nil { return cfg, err }
		cfg.Parity, err = mustGetString("MODBUS_RTU_PARITY")
		if err != nil { return cfg, err }
		cfg.Parity = strings.ToUpper(cfg.Parity)
		if cfg.Parity != "N" && cfg.Parity != "E" && cfg.Parity != "O" {
			return cfg, fmt.Errorf("MODBUS_RTU_PARITY must be N, E, or O, got %s", cfg.Parity)
		}
		cfg.StopBits, err = mustGetInt("MODBUS_RTU_STOP_BITS")
		if err != nil { return cfg, err }
		if cfg.StopBits != 1 && cfg.StopBits != 2 {
			return cfg, fmt.Errorf("MODBUS_RTU_STOP_BITS must be 1 or 2, got %d", cfg.StopBits)
		}
		cfg.DataBits, err = mustGetInt("MODBUS_RTU_DATA_BITS")
		if err != nil { return cfg, err }
		if cfg.DataBits < 7 || cfg.DataBits > 8 {
			return cfg, fmt.Errorf("MODBUS_RTU_DATA_BITS must be 7 or 8, got %d", cfg.DataBits)
		}
	}

	slaveIDInt, err := mustGetInt("MODBUS_SLAVE_ID")
	if err != nil { return cfg, err }
	if slaveIDInt < 0 || slaveIDInt > 255 {
		return cfg, fmt.Errorf("MODBUS_SLAVE_ID must be 0-255, got %d", slaveIDInt)
	}
	cfg.SlaveID = uint8(slaveIDInt)

	cfg.Table, err = mustGetString("MODBUS_TABLE")
	if err != nil { return cfg, err }
	cfg.Table = strings.ToLower(cfg.Table)
	switch cfg.Table {
	case "coil", "discrete", "input", "holding":
	default:
		return cfg, fmt.Errorf("MODBUS_TABLE must be one of coil|discrete|input|holding, got %s", cfg.Table)
	}

	addrInt, err := mustGetInt("MODBUS_ADDRESS")
	if err != nil { return cfg, err }
	if addrInt < 0 || addrInt > 65535 { return cfg, fmt.Errorf("MODBUS_ADDRESS must be 0-65535, got %d", addrInt) }
	cfg.Address = uint16(addrInt)

	qtyInt, err := mustGetInt("MODBUS_QUANTITY")
	if err != nil { return cfg, err }
	if qtyInt <= 0 || qtyInt > 125 { // typical max for registers
		return cfg, fmt.Errorf("MODBUS_QUANTITY must be 1-125, got %d", qtyInt)
	}
	cfg.Quantity = uint16(qtyInt)

	tmoMs, err := mustGetInt("MODBUS_TIMEOUT_MS")
	if err != nil { return cfg, err }
	cfg.Timeout = time.Duration(tmoMs) * time.Millisecond

	pollMs, err := mustGetInt("POLL_INTERVAL_MS")
	if err != nil { return cfg, err }
	cfg.PollInterval = time.Duration(pollMs) * time.Millisecond

	retryMax, err := mustGetInt("RETRY_MAX")
	if err != nil { return cfg, err }
	cfg.RetryMax = retryMax

	baseMs, err := mustGetInt("RETRY_BASE_BACKOFF_MS")
	if err != nil { return cfg, err }
	cfg.RetryBaseBackoff = time.Duration(baseMs) * time.Millisecond

	maxMs, err := mustGetInt("RETRY_MAX_BACKOFF_MS")
	if err != nil { return cfg, err }
	cfg.RetryMaxBackoff = time.Duration(maxMs) * time.Millisecond

	return cfg, nil
}

func mustGetString(k string) (string, error) {
	v, ok := os.LookupEnv(k)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("missing required env %s", k)
	}
	return v, nil
}

func mustGetInt(k string) (int, error) {
	v, ok := os.LookupEnv(k)
	if !ok || strings.TrimSpace(v) == "" {
		return 0, fmt.Errorf("missing required env %s", k)
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid int for %s: %v", k, err)
	}
	return i, nil
}

func ValidateQueryProtocol(s string) error {
	s = strings.ToLower(s)
	if s != "tcp" && s != "rtu" {
		return errors.New("protocol must be tcp or rtu")
	}
	return nil
}
