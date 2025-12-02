package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPHost     string
	HTTPPort     int
	SerialPort   string
	BaudRate     int
	DataBits     int
	Parity       string
	StopBits     int
	SlaveID      int
	Timeout      time.Duration
	PollInterval time.Duration

	TempRegister uint16
	TempFunc     string // "input" or "holding"
	TempScale    float64

	HumRegister uint16
	HumFunc     string // "input" or "holding"
	HumScale    float64

	MaxStaleness time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
	LogLevel     string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseIntEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return i
}

func parseUint16Env(key string, def uint16) uint16 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	iv, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || iv < 0 || iv > 0xFFFF {
		return def
	}
	return uint16(iv)
}

func parseFloatEnv(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

func parseDurationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	// Accept plain milliseconds as integer, or Go duration string (e.g., "1500ms")
	if ms, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func LoadConfig() (Config, error) {
	cfg := Config{}
	cfg.HTTPHost = getenv("HTTP_HOST", "0.0.0.0")
	cfg.HTTPPort = parseIntEnv("HTTP_PORT", 8080)
	cfg.SerialPort = getenv("SERIAL_PORT", "/dev/ttyUSB0")
	cfg.BaudRate = parseIntEnv("BAUD_RATE", 9600)
	cfg.DataBits = parseIntEnv("DATA_BITS", 8)
	cfg.Parity = strings.ToUpper(getenv("PARITY", "N"))
	if cfg.Parity != "N" && cfg.Parity != "E" && cfg.Parity != "O" {
		return cfg, fmt.Errorf("invalid PARITY: %s", cfg.Parity)
	}
	cfg.StopBits = parseIntEnv("STOP_BITS", 1)

	// Slave ID can be provided via SLAVE_ID or DEVICE_ADDRESS
	if v := os.Getenv("SLAVE_ID"); v != "" {
		cfg.SlaveID = parseIntEnv("SLAVE_ID", 1)
	} else {
		cfg.SlaveID = parseIntEnv("DEVICE_ADDRESS", 1)
	}
	if cfg.SlaveID < 1 || cfg.SlaveID > 247 {
		return cfg, fmt.Errorf("invalid slave id: %d", cfg.SlaveID)
	}

	cfg.Timeout = parseDurationEnv("TIMEOUT_MS", 5000*time.Millisecond)
	cfg.PollInterval = parseDurationEnv("POLL_INTERVAL_MS", 1000*time.Millisecond)

	cfg.TempRegister = parseUint16Env("TEMP_REGISTER", 0)
	cfg.TempFunc = strings.ToLower(getenv("TEMP_FUNC", "input"))
	if cfg.TempFunc != "input" && cfg.TempFunc != "holding" {
		return cfg, fmt.Errorf("invalid TEMP_FUNC: %s", cfg.TempFunc)
	}
	cfg.TempScale = parseFloatEnv("TEMP_SCALE", 1.0)

	cfg.HumRegister = parseUint16Env("HUM_REGISTER", 1)
	cfg.HumFunc = strings.ToLower(getenv("HUM_FUNC", "input"))
	if cfg.HumFunc != "input" && cfg.HumFunc != "holding" {
		return cfg, fmt.Errorf("invalid HUM_FUNC: %s", cfg.HumFunc)
	}
	cfg.HumScale = parseFloatEnv("HUM_SCALE", 1.0)

	cfg.MaxStaleness = parseDurationEnv("MAX_STALENESS_MS", 15000*time.Millisecond)
	cfg.BackoffBase = parseDurationEnv("BACKOFF_BASE_MS", 500*time.Millisecond)
	cfg.BackoffMax = parseDurationEnv("BACKOFF_MAX_MS", 10000*time.Millisecond)
	cfg.LogLevel = strings.ToLower(getenv("LOG_LEVEL", "info"))

	return cfg, nil
}
