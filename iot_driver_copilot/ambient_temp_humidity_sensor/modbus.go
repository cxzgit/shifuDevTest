package ambient_temp_humidity_sensor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	serial "github.com/tarm/serial"
)

const (
	fcReadHoldingRegisters byte = 0x03
)

// ModbusErrorType categorizes errors for counters and status.
type ModbusErrorType string

const (
	ErrTimeout   ModbusErrorType = "timeout"
	ErrCRC       ModbusErrorType = "crc"
	ErrException ModbusErrorType = "exception"
	ErrIO        ModbusErrorType = "io"
)

type ModbusError struct {
	Type    ModbusErrorType
	Message string
}

func (e ModbusError) Error() string { return fmt.Sprintf("%s: %s", e.Type, e.Message) }

// CRC16 Modbus calculation (poly 0xA001, init 0xFFFF)
func crc16Modbus(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if (crc & 0x0001) != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func buildReadRequest(slave byte, start uint16, qty uint16) []byte {
	buf := []byte{slave, fcReadHoldingRegisters, byte(start >> 8), byte(start & 0xFF), byte(qty >> 8), byte(qty & 0xFF)}
	crc := crc16Modbus(buf)
	buf = append(buf, byte(crc&0xFF), byte(crc>>8)) // CRC low, CRC high
	return buf
}

// readExactWithTimeout reads exactly n bytes, looping until n bytes are read or overallTimeout elapses.
func readExactWithTimeout(port *serial.Port, n int, overallTimeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(overallTimeout)
	buf := make([]byte, 0, n)
	tmp := make([]byte, n)
	for len(buf) < n {
		if time.Now().After(deadline) {
			return buf, ModbusError{Type: ErrTimeout, Message: "read timeout"}
		}
		rem := n - len(buf)
		// Attempt to read remaining bytes
		m, err := port.Read(tmp[:rem])
		if err != nil {
			if errors.Is(err, io.EOF) {
				// short wait to avoid busy loop
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return buf, ModbusError{Type: ErrIO, Message: err.Error()}
		}
		if m > 0 {
			buf = append(buf, tmp[:m]...)
		} else {
			// no bytes; small sleep before retry
			time.Sleep(5 * time.Millisecond)
		}
	}
	return buf, nil
}

// ReadHoldingRegisters sends a Modbus RTU request to read qty registers starting at start.
// It validates the response and returns the decoded register values.
func ReadHoldingRegisters(port *serial.Port, slave byte, start uint16, qty uint16, overallTimeout time.Duration) ([]uint16, error) {
	req := buildReadRequest(slave, start, qty)
	_, err := port.Write(req)
	if err != nil {
		return nil, ModbusError{Type: ErrIO, Message: fmt.Sprintf("write failed: %v", err)}
	}

	// Read header: addr, func, byteCount or exceptionCode
	hdr, err := readExactWithTimeout(port, 3, overallTimeout)
	if err != nil {
		return nil, err
	}
	addr := hdr[0]
	fc := hdr[1]
	bc := hdr[2]
	if addr != slave {
		return nil, ModbusError{Type: ErrIO, Message: fmt.Sprintf("unexpected slave addr 0x%02X", addr)}
	}
	// Exception response
	if fc&0x80 != 0 {
		// Read CRC
		crcBytes, err2 := readExactWithTimeout(port, 2, overallTimeout)
		if err2 != nil {
			return nil, err2
		}
		msg := []byte{hdr[0], hdr[1], hdr[2]}
		crcCalc := crc16Modbus(msg)
		crcRecv := uint16(crcBytes[0]) | (uint16(crcBytes[1]) << 8)
		if crcCalc != crcRecv {
			return nil, ModbusError{Type: ErrCRC, Message: "CRC mismatch on exception"}
		}
		return nil, ModbusError{Type: ErrException, Message: fmt.Sprintf("modbus exception code 0x%02X", bc)}
	}
	if fc != fcReadHoldingRegisters {
		return nil, ModbusError{Type: ErrIO, Message: fmt.Sprintf("unexpected function code 0x%02X", fc)}
	}
	expected := int(qty) * 2
	if int(bc) != expected {
		// Still read remaining bytes and CRC to drain
		rem, err2 := readExactWithTimeout(port, int(bc)+2, overallTimeout)
		if err2 != nil {
			return nil, err2
		}
		msg := append(hdr, rem...)
		crcCalc := crc16Modbus(msg[:len(msg)-2])
		crcRecv := uint16(msg[len(msg)-2]) | (uint16(msg[len(msg)-1]) << 8)
		if crcCalc != crcRecv {
			return nil, ModbusError{Type: ErrCRC, Message: "CRC mismatch"}
		}
		return nil, ModbusError{Type: ErrIO, Message: fmt.Sprintf("byte count %d did not match expected %d", bc, expected)}
	}
	payload, err := readExactWithTimeout(port, expected+2, overallTimeout) // data + CRC
	if err != nil {
		return nil, err
	}
	msg := append(hdr, payload...)
	crcCalc := crc16Modbus(msg[:len(msg)-2])
	crcRecv := uint16(msg[len(msg)-2]) | (uint16(msg[len(msg)-1]) << 8)
	if crcCalc != crcRecv {
		return nil, ModbusError{Type: ErrCRC, Message: "CRC mismatch"}
	}
	vals := make([]uint16, qty)
	for i := 0; i < int(qty); i++ {
		off := i * 2
		vals[i] = binary.BigEndian.Uint16(payload[off : off+2])
	}
	return vals, nil
}
