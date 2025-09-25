package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/goburrow/modbus"
)

type Sample struct {
	Timestamp   time.Time `json:"timestamp"`
	Analog      []uint16  `json:"analog"`
	Digital     []bool    `json:"digital"`
}

type ringBuffer struct {
	mu   sync.Mutex
	s    []Sample
	cap  int
	head int
	len  int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{s: make([]Sample, capacity), cap: capacity}
}

func (r *ringBuffer) add(x Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s[r.head] = x
	r.head = (r.head + 1) % r.cap
	if r.len < r.cap { r.len++ }
}

func (r *ringBuffer) lenUnsafe() int { return r.len }
func (r *ringBuffer) Len() int { r.mu.Lock(); defer r.mu.Unlock(); return r.len }
func (r *ringBuffer) LastTimestamp() (time.Time, bool) {
	r.mu.Lock(); defer r.mu.Unlock()
	if r.len == 0 { return time.Time{}, false }
	idx := (r.head - 1 + r.cap) % r.cap
	return r.s[idx].Timestamp, true
}

type Collector struct {
	cfg *Config

	buf *ringBuffer

	mu          sync.RWMutex
	connected   bool
	running     bool
	lastSuccess time.Time
	lastErr     error

	stopOnce sync.Once
}

func NewCollector(cfg *Config) *Collector {
	return &Collector{
		cfg: cfg,
		buf: newRingBuffer(cfg.BufferSize),
	}
}

func (c *Collector) Start(ctx context.Context) {
	c.mu.Lock(); c.running = true; c.mu.Unlock()
	go c.loop(ctx)
}

func (c *Collector) Stop() { c.stopOnce.Do(func() { /* loop governed by ctx */ }) }

func (c *Collector) loop(ctx context.Context) {
	backoff := c.cfg.BackoffInitial
	for {
		select {
		case <-ctx.Done():
			c.setConnected(false, nil)
			log.Printf("collector stopped")
			return
		default:
		}

		h, client, err := c.newModbusClient()
		if err != nil {
			c.recordError(err)
			log.Printf("modbus client init error: %v", err)
			if !sleepCtx(ctx, backoff) { return }
			backoff = nextBackoff(backoff, c.cfg.BackoffMax)
			continue
		}

		if err := h.Connect(); err != nil {
			c.recordError(err)
			log.Printf("modbus connect error: %v", err)
			h.Close()
			if !sleepCtx(ctx, backoff) { return }
			backoff = nextBackoff(backoff, c.cfg.BackoffMax)
			continue
		}
		log.Printf("modbus connected to %s (unit %d)", c.cfg.ModbusTCPAddr, c.cfg.ModbusUnitID)
		c.setConnected(true, nil)
		backoff = c.cfg.BackoffInitial

		for {
			select {
			case <-ctx.Done():
				h.Close()
				c.setConnected(false, nil)
				return
			default:
			}

			start := time.Now()
			analog, errA := readInputRegisters(client, 0, 8)
			dig, errD := readDiscreteInputs(client, 0, 4)
			if errA != nil || errD != nil {
				err := firstErr(errA, errD)
				c.recordError(err)
				log.Printf("read error, reconnecting: %v", err)
				h.Close()
				c.setConnected(false, err)
				break // reconnect
			}

			s := Sample{Timestamp: time.Now(), Analog: analog, Digital: dig}
			c.buf.add(s)
			c.mu.Lock(); c.lastSuccess = time.Now(); c.mu.Unlock()

			// maintain interval including time spent reading
			elapsed := time.Since(start)
			wait := c.cfg.AcqInterval - elapsed
			if wait < 0 { wait = 0 }
			if !sleepCtx(ctx, wait) { h.Close(); return }
		}
	}
}

func (c *Collector) newModbusClient() (*modbus.TCPClientHandler, modbus.Client, error) {
	h := modbus.NewTCPClientHandler(c.cfg.ModbusTCPAddr)
	h.Timeout = c.cfg.RequestTimeout
	h.SlaveId = c.cfg.ModbusUnitID
	client := modbus.NewClient(h)
	return h, client, nil
}

func readInputRegisters(client modbus.Client, address, quantity uint16) ([]uint16, error) {
	if quantity == 0 { return []uint16{}, nil }
	b, err := client.ReadInputRegisters(address, quantity)
	if err != nil { return nil, err }
	if len(b) != int(quantity*2) { return nil, fmt.Errorf("unexpected length: %d", len(b)) }
	out := make([]uint16, quantity)
	for i := 0; i < int(quantity); i++ {
		out[i] = binary.BigEndian.Uint16(b[i*2:])
	}
	return out, nil
}

func readDiscreteInputs(client modbus.Client, address, quantity uint16) ([]bool, error) {
	if quantity == 0 { return []bool{}, nil }
	b, err := client.ReadDiscreteInputs(address, quantity)
	if err != nil { return nil, err }
	out := make([]bool, quantity)
	for i := 0; i < int(quantity); i++ {
		byteIdx := i / 8
		bitIdx := uint(i % 8)
		if byteIdx >= len(b) { return nil, fmt.Errorf("unexpected length: %d", len(b)) }
		out[i] = ((b[byteIdx] >> bitIdx) & 0x01) == 0x01
	}
	return out, nil
}

func (c *Collector) recordError(err error) { c.mu.Lock(); c.lastErr = err; c.mu.Unlock() }
func (c *Collector) setConnected(v bool, err error) { c.mu.Lock(); c.connected = v; c.lastErr = err; c.mu.Unlock() }

func (c *Collector) Status() map[string]any {
	c.mu.RLock()
	connected := c.connected
	running := c.running
	last := c.lastSuccess
	c.mu.RUnlock()

	// online if last success within TTL
	online := false
	if !last.IsZero() {
		online = time.Since(last) <= time.Duration(c.cfg.OnlineTTLSec)*time.Second
	}

	lastTS, ok := c.buf.LastTimestamp()
	var lastStr string
	if ok { lastStr = lastTS.UTC().Format(time.RFC3339Nano) } else { lastStr = "" }

	status := map[string]any{
		"network":      ternary(online, "online", "offline"),
		"acquisition":  ternary(running && connected, "running", "stopped"),
		"buffer_fill":  c.buf.Len(),
		"buffer_capacity": c.buf.cap,
		"last_update":  lastStr,
	}
	return status
}

func (c *Collector) SetPublishIntervalModbus(intervalMs int) error {
	if c.cfg.ModbusPubIntervalReg == nil {
		return errors.New("MODBUS_PUB_INTERVAL_REG not configured")
	}
	valScaled := float64(intervalMs) / float64(c.cfg.ModbusPubIntervalScale)
	if valScaled < 0 || valScaled > float64(math.MaxUint16) {
		return fmt.Errorf("interval_ms out of range for single register with scale %d", c.cfg.ModbusPubIntervalScale)
	}
	regVal := uint16(valScaled + 0.5)

	h, client, _ := c.newModbusClient()
	h.Timeout = c.cfg.RequestTimeout
	if err := h.Connect(); err != nil { return err }
	defer h.Close()

	addr := *c.cfg.ModbusPubIntervalReg
	_, err := client.WriteSingleRegister(addr, regVal)
	return err
}

func nextBackoff(cur, max time.Duration) time.Duration {
	n := cur * 2
	if n > max { return max }
	return n
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 { return true }
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs { if e != nil { return e } }
	return nil
}

func ternary[T any](cond bool, a, b T) T { if cond { return a }; return b }
