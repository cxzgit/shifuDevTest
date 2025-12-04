package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all environment-driven configuration.
// All fields must be provided via environment variables.
// No hard-coded defaults are used.
type Config struct {
	HTTPHost        string
	HTTPPort        int

	SerialPort      string
	BaudRate        int
	DataBits        int
	Parity          string
	StopBits        int
	SlaveID         int

	RegTempAddr     uint16
	RegHumAddr      uint16
	RegSamplingAddr uint16

	ScaleTemp       float64
	ScaleHum        float64

	PollInterval    time.Duration
	ReadTimeout     time.Duration

	RetryInitial    time.Duration
	RetryMax        time.Duration
	RetryMultiplier float64
}

func mustGetEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("missing required env var: %s", key)
	}
	return v, nil
}

func parseIntEnv(key string) (int, error) {
	v, err := mustGetEnv(key)
	if err != nil {
		return 0, err
	}
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("invalid int for %s: %v", key, err)
	}
	return i, nil
}

func parseUint16Env(key string) (uint16, error) {
	v, err := mustGetEnv(key)
	if err != nil {
		return 0, err
	}
	ui64, err := strconv.ParseUint(strings.TrimSpace(v), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid uint16 for %s: %v", key, err)
	}
	return uint16(ui64), nil
}

func parseFloatEnv(key string) (float64, error) {
	v, err := mustGetEnv(key)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float for %s: %v", key, err)
	}
	return f, nil
}

func parseDurationMsEnv(key string) (time.Duration, error) {
	ms, err := parseIntEnv(key)
	if err != nil {
		return 0, err
	}
	if ms < 0 {
		return 0, fmt.Errorf("%s must be >= 0", key)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{}

	var err error

	if cfg.HTTPHost, err = mustGetEnv("HTTP_HOST"); err != nil {
		return nil, err
	}
	if cfg.HTTPPort, err = parseIntEnv("HTTP_PORT"); err != nil {
		return nil, err
	}

	if cfg.SerialPort, err = mustGetEnv("SERIAL_PORT"); err != nil {
		return nil, err
	}
	if cfg.BaudRate, err = parseIntEnv("BAUD_RATE"); err != nil {
		return nil, err
	}
	if cfg.DataBits, err = parseIntEnv("DATA_BITS"); err != nil {
		return nil, err
	}
	if cfg.Parity, err = mustGetEnv("PARITY"); err != nil {
		return nil, err
	}
	cfg.Parity = strings.ToUpper(strings.TrimSpace(cfg.Parity))
	if cfg.Parity != "N" && cfg.Parity != "E" && cfg.Parity != "O" {
		return nil, fmt.Errorf("PARITY must be one of N,E,O")
	}
	if cfg.StopBits, err = parseIntEnv("STOP_BITS"); err != nil {
		return nil, err
	}
	if cfg.StopBits != 1 && cfg.StopBits != 2 {
		return nil, fmt.Errorf("STOP_BITS must be 1 or 2")
	}
	if cfg.SlaveID, err = parseIntEnv("SLAVE_ID"); err != nil {
		return nil, err
	}
	if cfg.SlaveID < 0 || cfg.SlaveID > 247 {
		return nil, fmt.Errorf("SLAVE_ID must be in 0..247")
	}

	if cfg.RegTempAddr, err = parseUint16Env("REG_TEMP_ADDR"); err != nil {
		return nil, err
	}
	if cfg.RegHumAddr, err = parseUint16Env("REG_HUM_ADDR"); err != nil {
		return nil, err
	}
	if cfg.RegSamplingAddr, err = parseUint16Env("REG_SAMPLING_ADDR"); err != nil {
		return nil, err
	}

	if cfg.ScaleTemp, err = parseFloatEnv("SCALE_TEMP"); err != nil {
		return nil, err
	}
	if cfg.ScaleHum, err = parseFloatEnv("SCALE_HUM"); err != nil {
		return nil, err
	}

	if cfg.PollInterval, err = parseDurationMsEnv("POLL_INTERVAL_MS"); err != nil {
		return nil, err
	}
	if cfg.ReadTimeout, err = parseDurationMsEnv("READ_TIMEOUT_MS"); err != nil {
		return nil, err
	}

	if cfg.RetryInitial, err = parseDurationMsEnv("RETRY_INITIAL_MS"); err != nil {
		return nil, err
	}
	if cfg.RetryMax, err = parseDurationMsEnv("RETRY_MAX_MS"); err != nil {
		return nil, err
	}
	if cfg.RetryMultiplier, err = parseFloatEnv("RETRY_MULTIPLIER"); err != nil {
		return nil, err
	}
	if cfg.RetryMultiplier < 1.0 {
		return nil, errors.New("RETRY_MULTIPLIER must be >= 1.0")
	}

	return cfg, nil
}
