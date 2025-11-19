package main

import (
	"errors"
	"fmt"
	"time"

	serial "go.bug.st/serial"
)

func modbusCRC16(data []byte) uint16 {
	var crc uint16 = 0xFFFF
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

func buildReadHoldingRegistersRequest(slaveID byte, startAddr uint16, qty uint16) []byte {
	req := []byte{
		slaveID,
		0x03,
		byte(startAddr >> 8), byte(startAddr & 0xFF),
		byte(qty >> 8), byte(qty & 0xFF),
	}
	crc := modbusCRC16(req)
	req = append(req, byte(crc&0xFF), byte(crc>>8)) // CRC Low, CRC High
	return req
}

func readExact(port serial.Port, expected int, timeout time.Duration) ([]byte, error) {
	_ = port.SetReadTimeout(timeout)
	buf := make([]byte, expected)
	total := 0
	deadline := time.Now().Add(timeout)
	for total < expected {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for %d bytes", expected)
		}
		n, err := port.Read(buf[total:])
		if err != nil {
			return nil, err
		}
		if n == 0 {
			// small sleep to avoid busy loop if device is slow
			time.Sleep(5 * time.Millisecond)
			continue
		}
		total += n
	}
	return buf, nil
}

func readHoldingRegisters(port serial.Port, slaveID byte, startAddr uint16, qty uint16, timeout time.Duration) ([]byte, error) {
	req := buildReadHoldingRegistersRequest(slaveID, startAddr, qty)
	_, err := port.Write(req)
	if err != nil {
		return nil, err
	}
	expected := 5 + int(2*qty) // addr(1)+func(1)+byteCount(1)+data(2*qty)+crc(2)
	resp, err := readExact(port, expected, timeout)
	if err != nil {
		return nil, err
	}
	if len(resp) < 5 {
		return nil, errors.New("short response")
	}
	if resp[0] != slaveID {
		return nil, fmt.Errorf("unexpected slave id: got %d", resp[0])
	}
	if resp[1] != 0x03 {
		return nil, fmt.Errorf("unexpected function code: 0x%X", resp[1])
	}
	byteCount := int(resp[2])
	if byteCount != int(2*qty) {
		return nil, fmt.Errorf("unexpected byte count: %d", byteCount)
	}
	// Verify CRC
	crcCalc := modbusCRC16(resp[:len(resp)-2])
	crcRecv := uint16(resp[len(resp)-1])<<8 | uint16(resp[len(resp)-2])
	if crcCalc != crcRecv {
		return nil, fmt.Errorf("crc mismatch: calc 0x%X recv 0x%X", crcCalc, crcRecv)
	}
	return resp, nil
}

func parseRegisterValue(resp []byte, regIndex int, signed bool) int32 {
	// Data starts at resp[3]
	offset := 3 + regIndex*2
	hi := resp[offset]
	lo := resp[offset+1]
	u := uint16(hi)<<8 | uint16(lo)
	if signed {
		return int32(int16(u))
	}
	return int32(u)
}
