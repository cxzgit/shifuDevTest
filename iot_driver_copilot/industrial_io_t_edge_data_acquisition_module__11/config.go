package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPHost           string
	HTTPPort           int

	ModbusTCPAddr      string
	ModbusUnitID       byte
	ModbusTimeout      time.Duration

	PollInterval       time.Duration
	BackoffInitial     time.Duration
	BackoffMax         time.Duration

	AnalogBaseAddr     int
	AnalogCount        int
	AnalogRegWidthBits int // 16 or 32
	AnalogEndian       string // big or little (only for 32)
	AnalogSigned       bool   // only for 16-bit reads
	AnalogScale        float64
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func atoiEnv(key string, def int) (int, error) {
	v := getenv(key, "")
	if v == "" {
		return def, nil
	}
	iv, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("invalid int for %s: %v", key, err)
	}
	return iv, nil
}

func atobEnv(key string, def bool) (bool, error) {
	v := getenv(key, "")
	if v == "" {
		return def, nil
	}
	bv, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("invalid bool for %s: %v", key, err)
	}
	return bv, nil
}

func atofEnv(key string, def float64) (float64, error) {
	v := getenv(key, "")
	if v == "" {
		return def, nil
	}
	fv, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float for %s: %v", key, err)
	}
	return fv, nil
}

func durEnvMS(key string, defMS int) (time.Duration, error) {
	iv, err := atoiEnv(key, defMS)
	if err != nil {
		return 0, err
	}
	if iv < 0 {
		iv = defMS
	}
	return time.Duration(iv) * time.Millisecond, nil
}

func LoadConfig() (Config, error) {
	var cfg Config
	var err error

	cfg.HTTPHost = getenv("HTTP_HOST", "0.0.0.0")
	if cfg.HTTPPort, err = atoiEnv("HTTP_PORT", 8080); err != nil {
		return cfg, err
	}

	cfg.ModbusTCPAddr = getenv("MODBUS_TCP_ADDR", "127.0.0.1:502")
	unit, err := atoiEnv("MODBUS_UNIT_ID", 1)
	if err != nil {
		return cfg, err
	}
	if unit < 0 || unit > 247 {
		return cfg, fmt.Errorf("MODBUS_UNIT_ID out of range 0..247")
	}
	cfg.ModbusUnitID = byte(unit)
	if cfg.ModbusTimeout, err = durEnvMS("MODBUS_TIMEOUT_MS", 2000); err != nil {
		return cfg, err
	}

	if cfg.PollInterval, err = durEnvMS("POLL_INTERVAL_MS", 1000); err != nil {
		return cfg, err
	}
	if cfg.BackoffInitial, err = durEnvMS("BACKOFF_INITIAL_MS", 1000); err != nil {
		return cfg, err
	}
	if cfg.BackoffMax, err = durEnvMS("BACKOFF_MAX_MS", 15000); err != nil {
		return cfg, err
	}

	if cfg.AnalogBaseAddr, err = atoiEnv("ANALOG_BASE_ADDR", 0); err != nil {
		return cfg, err
	}
	if cfg.AnalogCount, err = atoiEnv("ANALOG_COUNT", 8); err != nil {
		return cfg, err
	}
	if cfg.AnalogCount <= 0 {
		return cfg, fmt.Errorf("ANALOG_COUNT must be > 0")
	}
	if cfg.AnalogRegWidthBits, err = atoiEnv("ANALOG_REG_WIDTH", 16); err != nil {
		return cfg, err
	}
	if cfg.AnalogRegWidthBits != 16 && cfg.AnalogRegWidthBits != 32 {
		return cfg, fmt.Errorf("ANALOG_REG_WIDTH must be 16 or 32")
	}
	cfg.AnalogEndian = strings.ToLower(getenv("ANALOG_ENDIAN", "big"))
	if cfg.AnalogRegWidthBits == 32 && cfg.AnalogEndian != "big" && cfg.AnalogEndian != "little" {
		return cfg, fmt.Errorf("ANALOG_ENDIAN must be 'big' or 'little'")
	}
	if cfg.AnalogSigned, err = atobEnv("ANALOG_SIGNED", false); err != nil {
		return cfg, err
	}
	if cfg.AnalogScale, err = atofEnv("ANALOG_SCALE", 1000.0); err != nil {
		return cfg, err
	}
	if cfg.AnalogScale == 0 {
		cfg.AnalogScale = 1.0
	}
	return cfg, nil
}
