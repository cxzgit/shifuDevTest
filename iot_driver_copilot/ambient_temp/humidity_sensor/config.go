package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all environment-driven configuration for the driver.
type Config struct {
	// HTTP server
	HTTPHost string
	HTTPPort int

	// Serial / Modbus RTU
	SerialPort       string
	SerialBaud       int
	SerialDataBits   int
	SerialParity     string // N/E/O
	SerialStopBits   int    // 1/2
	SerialReadChunkMs int   // internal chunk timeout for serial reads

	ModbusSlaveID    int

	// Registers
	RegTemperature      int
	RegHumidity         int
	RegSampleInterval   int

	// Scaling factors
	ScaleTemperature float64
	ScaleHumidity    float64

	// Polling
	PollUseSampleInterval bool
	PollIntervalSec       int

	// Retry/backoff
	RetryMax       int
	BackoffBaseMs  int
	BackoffMaxMs   int
	ReadTimeoutMs  int // overall transaction timeout per Modbus request

	// Logging
	LogLevel string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("WARN: invalid int for %s: %q, using default %d", key, v, def)
		return def
	}
	return i
}

func getenvFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("WARN: invalid float for %s: %q, using default %.6f", key, v, def)
		return def
	}
	return f
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "1" || s == "true" || s == "yes" || s == "y" || s == "on" {
		return true
	}
	if s == "0" || s == "false" || s == "no" || s == "n" || s == "off" {
		return false
	}
	log.Printf("WARN: invalid bool for %s: %q, using default %v", key, v, def)
	return def
}

func LoadConfig() Config {
	cfg := Config{
		HTTPHost: getenv("HTTP_HOST", "0.0.0.0"),
		HTTPPort: getenvInt("HTTP_PORT", 8080),

		SerialPort:       getenv("SERIAL_PORT", "/dev/ttyUSB0"),
		SerialBaud:       getenvInt("SERIAL_BAUD", 9600),
		SerialDataBits:   getenvInt("SERIAL_DATA_BITS", 8),
		SerialParity:     strings.ToUpper(getenv("SERIAL_PARITY", "N")),
		SerialStopBits:   getenvInt("SERIAL_STOP_BITS", 1),
		SerialReadChunkMs: getenvInt("SERIAL_READ_CHUNK_MS", 50),

		ModbusSlaveID: getenvInt("MODBUS_SLAVE_ID", 1),

		RegTemperature:    getenvInt("REG_TEMPERATURE", 0),
		RegHumidity:       getenvInt("REG_HUMIDITY", 1),
		RegSampleInterval: getenvInt("REG_SAMPLE_INTERVAL", 2),

		ScaleTemperature: getenvFloat("SCALE_TEMPERATURE", 0.01),
		ScaleHumidity:    getenvFloat("SCALE_HUMIDITY", 0.01),

		PollUseSampleInterval: getenvBool("POLL_USE_SAMPLE_INTERVAL", true),
		PollIntervalSec:       getenvInt("POLL_INTERVAL_SEC", 2),

		RetryMax:      getenvInt("RETRY_MAX", 3),
		BackoffBaseMs: getenvInt("BACKOFF_BASE_MS", 200),
		BackoffMaxMs:  getenvInt("BACKOFF_MAX_MS", 5000),
		ReadTimeoutMs: getenvInt("READ_TIMEOUT_MS", 1000),

		LogLevel: strings.ToLower(getenv("LOG_LEVEL", "info")),
	}

	if cfg.SerialDataBits < 5 || cfg.SerialDataBits > 8 {
		log.Printf("WARN: SERIAL_DATA_BITS=%d out of range, forcing 8", cfg.SerialDataBits)
		cfg.SerialDataBits = 8
	}
	if cfg.SerialStopBits != 1 && cfg.SerialStopBits != 2 {
		log.Printf("WARN: SERIAL_STOP_BITS=%d invalid, forcing 1", cfg.SerialStopBits)
		cfg.SerialStopBits = 1
	}
	if cfg.SerialParity != "N" && cfg.SerialParity != "E" && cfg.SerialParity != "O" {
		log.Printf("WARN: SERIAL_PARITY=%s invalid, forcing N", cfg.SerialParity)
		cfg.SerialParity = "N"
	}
	if cfg.SerialBaud <= 0 {
		cfg.SerialBaud = 9600
	}
	if cfg.PollIntervalSec < 1 {
		cfg.PollIntervalSec = 1
	}
	if cfg.ReadTimeoutMs < 100 {
		cfg.ReadTimeoutMs = 100
	}
	if cfg.SerialReadChunkMs < 1 {
		cfg.SerialReadChunkMs = 10
	}
	if cfg.RetryMax < 1 {
		cfg.RetryMax = 1
	}
	if cfg.BackoffBaseMs < 10 {
		cfg.BackoffBaseMs = 10
	}
	if cfg.BackoffMaxMs < cfg.BackoffBaseMs {
		cfg.BackoffMaxMs = cfg.BackoffBaseMs * 10
	}
	return cfg
}

func (c Config) ReadTimeout() time.Duration {
	return time.Duration(c.ReadTimeoutMs) * time.Millisecond
}

func (c Config) SerialChunkTimeout() time.Duration {
	return time.Duration(c.SerialReadChunkMs) * time.Millisecond
}
