package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goburrow/modbus"
)

type SampleBuffer struct {
	mu            sync.RWMutex
	analog        []float64
	lastUpdate    time.Time
	connected     bool
	retries       uint64
	backoff       time.Duration
	sampleCount   uint64
}

func NewSampleBuffer(n int) *SampleBuffer {
	return &SampleBuffer{analog: make([]float64, n)}
}

func (b *SampleBuffer) UpdateAnalog(values []float64, ts time.Time) {
	b.mu.Lock()
	copy(b.analog, values)
	b.lastUpdate = ts
	b.connected = true
	b.sampleCount++
	b.mu.Unlock()
}

func (b *SampleBuffer) SetDisconnected() {
	b.mu.Lock()
	b.connected = false
	b.mu.Unlock()
}

func (b *SampleBuffer) SetBackoff(d time.Duration) {
	b.mu.Lock()
	b.backoff = d
	b.mu.Unlock()
}

func (b *SampleBuffer) IncRetry() {
	atomic.AddUint64(&b.retries, 1)
}

func (b *SampleBuffer) Snapshot() (values []float64, ts time.Time, connected bool, retries uint64, backoff time.Duration, samples uint64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	values = make([]float64, len(b.analog))
	copy(values, b.analog)
	ts = b.lastUpdate
	connected = b.connected
	retries = atomic.LoadUint64(&b.retries)
	backoff = b.backoff
	samples = b.sampleCount
	return
}

type Collector struct {
	cfg     Config
	buf     *SampleBuffer
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewCollector(cfg Config, buf *SampleBuffer) *Collector {
	ctx, cancel := context.WithCancel(context.Background())
	return &Collector{cfg: cfg, buf: buf, ctx: ctx, cancel: cancel}
}

func (c *Collector) Start() {
	go c.loop()
}

func (c *Collector) Stop() {
	c.cancel()
}

func (c *Collector) loop() {
	backoff := c.cfg.BackoffInitial
	c.buf.SetBackoff(backoff)
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		handler := &modbus.TCPClientHandler{Address: c.cfg.ModbusTCPAddr, Timeout: c.cfg.ModbusTimeout, SlaveId: c.cfg.ModbusUnitID}
		if err := handler.Connect(); err != nil {
			log.Printf("modbus connect error: %v", err)
			c.buf.SetDisconnected()
			c.buf.IncRetry()
			c.sleepBackoff(&backoff)
			continue
		}
		log.Printf("modbus connected to %s (unit %d)", c.cfg.ModbusTCPAddr, c.cfg.ModbusUnitID)
		client := modbus.NewClient(handler)

		for {
			select {
			case <-c.ctx.Done():
				_ = handler.Close()
				return
			default:
			}

			values, err := c.readAnalogs(client)
			if err != nil {
				log.Printf("modbus read error: %v", err)
				c.buf.SetDisconnected()
				c.buf.IncRetry()
				_ = handler.Close()
				c.sleepBackoff(&backoff)
				break // break inner loop to reconnect
			}

			c.buf.UpdateAnalog(values, time.Now())
			backoff = c.cfg.BackoffInitial
			c.buf.SetBackoff(backoff)

			if !c.sleepPoll() {
				_ = handler.Close()
				return
			}
		}
	}
}

func (c *Collector) sleepPoll() bool {
	t := time.NewTimer(c.cfg.PollInterval)
	defer t.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (c *Collector) sleepBackoff(backoff *time.Duration) {
	if *backoff <= 0 {
		*backoff = c.cfg.BackoffInitial
	}
	if *backoff > c.cfg.BackoffMax {
		*backoff = c.cfg.BackoffMax
	}
	c.buf.SetBackoff(*backoff)
	t := time.NewTimer(*backoff)
	defer t.Stop()
	select {
	case <-c.ctx.Done():
		return
	case <-t.C:
	}
	// exponential backoff
	next := 2 * (*backoff)
	if next > c.cfg.BackoffMax {
		next = c.cfg.BackoffMax
	}
	*backoff = next
}

func (c *Collector) readAnalogs(client modbus.Client) ([]float64, error) {
	count := c.cfg.AnalogCount
	values := make([]float64, count)

	switch c.cfg.AnalogRegWidthBits {
	case 16:
		qty := uint16(count)
		addr := uint16(c.cfg.AnalogBaseAddr)
		resp, err := client.ReadHoldingRegisters(addr, qty)
		if err != nil {
			return nil, err
		}
		if len(resp) < int(qty)*2 {
			return nil, fmt.Errorf("short read: got %d bytes, expected %d", len(resp), int(qty)*2)
		}
		for i := 0; i < count; i++ {
			off := i * 2
			r := binary.BigEndian.Uint16(resp[off : off+2])
			var v int64
			if c.cfg.AnalogSigned {
				v = int64(int16(r))
			} else {
				v = int64(r)
			}
			values[i] = float64(v) / c.cfg.AnalogScale
		}
	case 32:
		qty := uint16(count * 2)
		addr := uint16(c.cfg.AnalogBaseAddr)
		resp, err := client.ReadHoldingRegisters(addr, qty)
		if err != nil {
			return nil, err
		}
		if len(resp) < int(qty)*2 {
			return nil, fmt.Errorf("short read: got %d bytes, expected %d", len(resp), int(qty)*2)
		}
		for i := 0; i < count; i++ {
			off := i * 4
			w1 := binary.BigEndian.Uint16(resp[off : off+2])
			w2 := binary.BigEndian.Uint16(resp[off+2 : off+4])
			var u32 uint32
			if c.cfg.AnalogEndian == "little" {
				u32 = uint32(w2)<<16 | uint32(w1)
			} else {
				u32 = uint32(w1)<<16 | uint32(w2)
			}
			values[i] = float64(u32) / c.cfg.AnalogScale
		}
	default:
		return nil, fmt.Errorf("unsupported reg width: %d", c.cfg.AnalogRegWidthBits)
	}

	return values, nil
}
