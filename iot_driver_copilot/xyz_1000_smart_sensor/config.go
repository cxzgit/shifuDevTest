package main

import (
	"log"
	"os"
	"strconv"
)

type Config struct {
	HTTPHost       string
	HTTPPort       int
	SerialPort     string
	BaudRate       int
	ModbusAddress  int
	PollIntervalMs int
	ReadTimeoutMs  int
	RetryInitialMs int
	RetryMaxMs     int
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("warn: invalid int for %s: %v", key, err)
		return def
	}
	return i
}

func LoadConfig() *Config {
	cfg := &Config{
		HTTPHost:       getenv("IOT_HTTP_HOST", "0.0.0.0"),
		HTTPPort:       getenvInt("IOT_HTTP_PORT", 8080),
		SerialPort:     getenv("IOT_SERIAL_PORT", ""),
		BaudRate:       getenvInt("IOT_BAUD_RATE", 9600),
		ModbusAddress:  getenvInt("IOT_MODBUS_ADDRESS", 1),
		PollIntervalMs: getenvInt("IOT_POLL_INTERVAL_MS", 1000),
		ReadTimeoutMs:  getenvInt("IOT_READ_TIMEOUT_MS", 500),
		RetryInitialMs: getenvInt("IOT_RETRY_INITIAL_MS", 500),
		RetryMaxMs:     getenvInt("IOT_RETRY_MAX_MS", 10000),
	}
	return cfg
}
