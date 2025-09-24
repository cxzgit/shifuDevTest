package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPHost          string
	HTTPPort          int

	ModbusProtocol    string // tcp | rtu

	// TCP
	DeviceHost        string
	DevicePort        int

	// RTU
	SerialPort        string
	SerialBaud        int
	SerialParity      string // N|E|O
	SerialStopBits    int
	SerialDataBits    int

	SlaveID           int
	TimeoutMs         int

	// Polling
	PollTable         string // coil|discrete|input|holding
	PollAddress       int
	PollQuantity      int
	PollIntervalMs    int

	// Backoff
	RetryInitialMs    int
	RetryMaxMs        int
	RetryMaxAttempts  int
}

func getenv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("missing env %s", key)
	}
	return v, nil
}

func getenvInt(key string) (int, error) {
	v, err := getenv(key)
	if err != nil {
		return 0, err
	}
	iv, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid int for %s: %v", key, err)
	}
	return iv, nil
}

func loadConfig() (Config, error) {
	var c Config
	var err error

	c.HTTPHost, err = getenv("HTTP_HOST")
	if err != nil { return c, err }
	c.HTTPPort, err = getenvInt("HTTP_PORT")
	if err != nil { return c, err }

	c.ModbusProtocol, err = getenv("MODBUS_PROTOCOL")
	if err != nil { return c, err }
	c.ModbusProtocol = strings.ToLower(c.ModbusProtocol)
	if c.ModbusProtocol != "tcp" && c.ModbusProtocol != "rtu" {
		return c, fmt.Errorf("MODBUS_PROTOCOL must be 'tcp' or 'rtu'")
	}

	if c.ModbusProtocol == "tcp" {
		c.DeviceHost, err = getenv("DEVICE_HOST")
		if err != nil { return c, err }
		c.DevicePort, err = getenvInt("DEVICE_PORT")
		if err != nil { return c, err }
	} else {
		c.SerialPort, err = getenv("SERIAL_PORT")
		if err != nil { return c, err }
		c.SerialBaud, err = getenvInt("SERIAL_BAUD")
		if err != nil { return c, err }
		c.SerialParity, err = getenv("SERIAL_PARITY")
		if err != nil { return c, err }
		c.SerialParity = strings.ToUpper(c.SerialParity)
		if c.SerialParity != "N" && c.SerialParity != "E" && c.SerialParity != "O" && c.SerialParity != "NONE" && c.SerialParity != "EVEN" && c.SerialParity != "ODD" {
			return c, fmt.Errorf("SERIAL_PARITY must be N/E/O or NONE/EVEN/ODD")
		}
		// Normalize
		if c.SerialParity == "NONE" { c.SerialParity = "N" }
		if c.SerialParity == "EVEN" { c.SerialParity = "E" }
		if c.SerialParity == "ODD" { c.SerialParity = "O" }
		c.SerialStopBits, err = getenvInt("SERIAL_STOPBITS")
		if err != nil { return c, err }
		c.SerialDataBits, err = getenvInt("SERIAL_DATABITS")
		if err != nil { return c, err }
	}

	c.SlaveID, err = getenvInt("SLAVE_ID")
	if err != nil { return c, err }
	c.TimeoutMs, err = getenvInt("TIMEOUT_MS")
	if err != nil { return c, err }

	c.PollTable, err = getenv("POLL_TABLE")
	if err != nil { return c, err }
	c.PollTable = strings.ToLower(c.PollTable)
	if c.PollTable != "coil" && c.PollTable != "discrete" && c.PollTable != "input" && c.PollTable != "holding" {
		return c, fmt.Errorf("POLL_TABLE must be coil|discrete|input|holding")
	}
	c.PollAddress, err = getenvInt("POLL_ADDRESS")
	if err != nil { return c, err }
	c.PollQuantity, err = getenvInt("POLL_QUANTITY")
	if err != nil { return c, err }
	c.PollIntervalMs, err = getenvInt("POLL_INTERVAL_MS")
	if err != nil { return c, err }

	c.RetryInitialMs, err = getenvInt("RETRY_INITIAL_MS")
	if err != nil { return c, err }
	c.RetryMaxMs, err = getenvInt("RETRY_MAX_MS")
	if err != nil { return c, err }
	c.RetryMaxAttempts, err = getenvInt("RETRY_MAX_ATTEMPTS")
	if err != nil { return c, err }

	return c, nil
}
