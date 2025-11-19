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
	StopBits     int
	Parity       string // N, E, O

	SlaveID      byte

	RegTemp      uint16
	RegHum       uint16
	RegCO2       uint16

	ScaleTemp    float64
	ScaleHum     float64
	ScaleCO2     float64

	TempSigned   bool
	HumSigned    bool
	CO2Signed    bool

	PollInterval time.Duration
	ReadTimeout  time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

func getenv(key string, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func mustGetenv(key string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("missing required environment variable: %s", key)
	}
	return v, nil
}

func parseIntEnv(key string, def int) int {
	s := getenv(key, "")
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func parseUint16Env(key string) (uint16, error) {
	s := getenv(key, "")
	if s == "" {
		return 0, fmt.Errorf("missing required environment variable: %s", key)
	}
	u, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid uint16 for %s: %v", key, err)
	}
	return uint16(u), nil
}

func parseFloatEnv(key string, def float64) float64 {
	s := getenv(key, "")
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

func parseBoolEnv(key string, def bool) bool {
	s := getenv(key, "")
	if s == "" {
		return def
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

func parseDurationMsEnv(key string, def int) time.Duration {
	s := getenv(key, "")
	if s == "" {
		return time.Duration(def) * time.Millisecond
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return time.Duration(def) * time.Millisecond
	}
	return time.Duration(v) * time.Millisecond
}

func LoadConfig() (*Config, error) {
	cfg := &Config{}

	cfg.HTTPHost = getenv("HTTP_HOST", "0.0.0.0")
	cfg.HTTPPort = parseIntEnv("HTTP_PORT", 8080)

	sp, err := mustGetenv("SERIAL_PORT")
	if err != nil {
		return nil, err
	}
	cfg.SerialPort = sp
	cfg.BaudRate = parseIntEnv("BAUD_RATE", 9600)
	cfg.DataBits = parseIntEnv("DATA_BITS", 8)
	cfg.StopBits = parseIntEnv("STOP_BITS", 1)
	cfg.Parity = strings.ToUpper(getenv("PARITY", "N"))
	if cfg.Parity != "N" && cfg.Parity != "E" && cfg.Parity != "O" {
		return nil, fmt.Errorf("invalid PARITY: %s (must be N, E, or O)", cfg.Parity)
	}

	sidInt := parseIntEnv("MODBUS_SLAVE_ID", 1)
	if sidInt < 0 || sidInt > 255 {
		return nil, fmt.Errorf("MODBUS_SLAVE_ID must be 0-255")
	}
	cfg.SlaveID = byte(sidInt)

	regTemp, err := parseUint16Env("REG_TEMP")
	if err != nil {
		return nil, err
	}
	cfg.RegTemp = regTemp
	regHum, err := parseUint16Env("REG_HUM")
	if err != nil {
		return nil, err
	}
	cfg.RegHum = regHum
	regCO2, err := parseUint16Env("REG_CO2")
	if err != nil {
		return nil, err
	}
	cfg.RegCO2 = regCO2

	cfg.ScaleTemp = parseFloatEnv("SCALE_TEMP", 1.0)
	cfg.ScaleHum = parseFloatEnv("SCALE_HUM", 1.0)
	cfg.ScaleCO2 = parseFloatEnv("SCALE_CO2", 1.0)

	cfg.TempSigned = parseBoolEnv("TEMP_SIGNED", true)
	cfg.HumSigned = parseBoolEnv("HUM_SIGNED", true)
	cfg.CO2Signed = parseBoolEnv("CO2_SIGNED", false)

	cfg.PollInterval = parseDurationMsEnv("POLL_INTERVAL_MS", 1000)
	cfg.ReadTimeout = parseDurationMsEnv("READ_TIMEOUT_MS", 500)
	cfg.BackoffBase = parseDurationMsEnv("BACKOFF_BASE_MS", 500)
	cfg.BackoffMax = parseDurationMsEnv("BACKOFF_MAX_MS", 15000)

	return cfg, nil
}
