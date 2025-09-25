package main

import (
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPHost               string
	HTTPPort               int

	UpdateIntervalMs       int
	RequestTimeoutMs       int

	RetryInitialBackoffMs  int
	RetryMaxBackoffMs      int
	RetryMultiplier        float64

	// Modbus TCP
	ModbusTCPAddr          string
	ModbusTCPUnitID        uint8

	// Modbus RTU
	ModbusRTUPort          string
	ModbusRTUBaud          int
	ModbusRTUDataBits      int
	ModbusRTUParity        string
	ModbusRTUStopBits      int
	ModbusRTUUnitID        uint8

	// Read configuration (addresses/counts)
	ReadCoilsAddr          uint16
	ReadCoilsCount         uint16
	ReadDiscreteAddr       uint16
	ReadDiscreteCount      uint16
	ReadInputRegAddr       uint16
	ReadInputRegCount      uint16
	ReadHoldingRegAddr     uint16
	ReadHoldingRegCount    uint16

	// Optional write ops on connect
	WriteCoilEnable        bool
	WriteCoilAddr          uint16
	WriteCoilValue         bool

	WriteRegEnable         bool
	WriteRegAddr           uint16
	WriteRegValue          uint16

	// MQTT (optional)
	MQTTBrokerURL          string
	MQTTClientID           string
	MQTTUsername           string
	MQTTPassword           string
	MQTTTopic              string
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if iv, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return iv
		}
		log.Printf("WARN: invalid int for %s: %s, using default %d", key, v, def)
	}
	return def
}

func getenvUint16(key string, def uint16) uint16 {
	if v, ok := os.LookupEnv(key); ok {
		if iv, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			if iv < 0 {
				log.Printf("WARN: negative for %s: %s, using default %d", key, v, def)
				return def
			}
			return uint16(iv)
		}
		log.Printf("WARN: invalid uint16 for %s: %s, using default %d", key, v, def)
	}
	return def
}

func getenvUint8(key string, def uint8) uint8 {
	if v, ok := os.LookupEnv(key); ok {
		if iv, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			if iv < 0 {
				log.Printf("WARN: negative for %s: %s, using default %d", key, v, def)
				return def
			}
			return uint8(iv)
		}
		log.Printf("WARN: invalid uint8 for %s: %s, using default %d", key, v, def)
	}
	return def
}

func getenvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		lv := strings.ToLower(strings.TrimSpace(v))
		return lv == "1" || lv == "true" || lv == "yes" || lv == "y"
	}
	return def
}

func getenvFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if fv, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return fv
		}
		log.Printf("WARN: invalid float for %s: %s, using default %f", key, v, def)
	}
	return def
}

func LoadConfig() Config {
	cfg := Config{
		HTTPHost:              getenv("HTTP_HOST", "0.0.0.0"),
		HTTPPort:              getenvInt("HTTP_PORT", 8080),
		UpdateIntervalMs:      getenvInt("ACQ_INTERVAL_MS", 500),
		RequestTimeoutMs:      getenvInt("REQUEST_TIMEOUT_MS", 500),
		RetryInitialBackoffMs: getenvInt("RETRY_INITIAL_BACKOFF_MS", 500),
		RetryMaxBackoffMs:     getenvInt("RETRY_MAX_BACKOFF_MS", 10000),
		RetryMultiplier:       getenvFloat("RETRY_MULTIPLIER", 2.0),

		ModbusTCPAddr:         getenv("MODBUS_TCP_ADDR", ""),
		ModbusTCPUnitID:       getenvUint8("MODBUS_TCP_UNIT_ID", 1),

		ModbusRTUPort:         getenv("MODBUS_RTU_PORT", ""),
		ModbusRTUBaud:         getenvInt("MODBUS_RTU_BAUD", 9600),
		ModbusRTUDataBits:     getenvInt("MODBUS_RTU_DATA_BITS", 8),
		ModbusRTUParity:       strings.ToUpper(getenv("MODBUS_RTU_PARITY", "N")),
		ModbusRTUStopBits:     getenvInt("MODBUS_RTU_STOP_BITS", 1),
		ModbusRTUUnitID:       getenvUint8("MODBUS_RTU_UNIT_ID", 1),

		ReadCoilsAddr:         getenvUint16("READ_COILS_ADDR", 0),
		ReadCoilsCount:        getenvUint16("READ_COILS_COUNT", 0),
		ReadDiscreteAddr:      getenvUint16("READ_DISCRETE_ADDR", 0),
		ReadDiscreteCount:     getenvUint16("READ_DISCRETE_COUNT", 0),
		ReadInputRegAddr:      getenvUint16("READ_INPUT_REG_ADDR", 0),
		ReadInputRegCount:     getenvUint16("READ_INPUT_REG_COUNT", 0),
		ReadHoldingRegAddr:    getenvUint16("READ_HOLDING_REG_ADDR", 0),
		ReadHoldingRegCount:   getenvUint16("READ_HOLDING_REG_COUNT", 0),

		WriteCoilEnable:       getenvBool("WRITE_COIL_ENABLE", false),
		WriteCoilAddr:         getenvUint16("WRITE_COIL_ADDR", 0),
		WriteCoilValue:        getenvBool("WRITE_COIL_VALUE", false),

		WriteRegEnable:        getenvBool("WRITE_REG_ENABLE", false),
		WriteRegAddr:          getenvUint16("WRITE_REG_ADDR", 0),
		WriteRegValue:         getenvUint16("WRITE_REG_VALUE", 0),

		MQTTBrokerURL:         getenv("MQTT_BROKER_URL", ""),
		MQTTClientID:          getenv("MQTT_CLIENT_ID", "daq2000-driver"),
		MQTTUsername:          getenv("MQTT_USERNAME", ""),
		MQTTPassword:          getenv("MQTT_PASSWORD", ""),
		MQTTTopic:             getenv("MQTT_TOPIC", ""),
	}

	if cfg.UpdateIntervalMs <= 0 {
		cfg.UpdateIntervalMs = 500
	}
	if cfg.UpdateIntervalMs > 1000 {
		log.Printf("INFO: ACQ_INTERVAL_MS capped at 1000ms (was %d)", cfg.UpdateIntervalMs)
		cfg.UpdateIntervalMs = 1000
	}
	if cfg.ModbusRTUParity != "N" && cfg.ModbusRTUParity != "E" && cfg.ModbusRTUParity != "O" {
		log.Printf("WARN: invalid MODBUS_RTU_PARITY '%s', using 'N'", cfg.ModbusRTUParity)
		cfg.ModbusRTUParity = "N"
	}
	if cfg.ModbusRTUStopBits != 1 && cfg.ModbusRTUStopBits != 2 {
		log.Printf("WARN: invalid MODBUS_RTU_STOP_BITS '%d', using 1", cfg.ModbusRTUStopBits)
		cfg.ModbusRTUStopBits = 1
	}
	return cfg
}
