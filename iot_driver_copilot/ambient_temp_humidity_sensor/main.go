package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	serial "github.com/tarm/serial"
)

type Sample struct {
	TemperatureC      float64 `json:"temperature_c"`
	HumidityRH        float64 `json:"humidity_rh"`
	SampleIntervalSec int     `json:"sample_interval_sec"`
	RawTemperature    uint16  `json:"raw_temperature"`
	RawHumidity       uint16  `json:"raw_humidity"`
}

type ErrorCounters struct {
	Timeouts   int64 `json:"timeouts"`
	CRCErrors  int64 `json:"crc_errors"`
	IOErrors   int64 `json:"io_errors"`
	Retries    int64 `json:"retries"`
	Reconnects int64 `json:"reconnects"`
}

type Timestamps struct {
	LastPollAttempt string `json:"last_poll_attempt"`
	LastSuccess     string `json:"last_success"`
	LastError       string `json:"last_error"`
}

type Status struct {
	SerialConnected bool   `json:"serial_connected"`
	ModbusSlaveOK   bool   `json:"modbus_slave_ok"`
	LastSample      Sample `json:"last_sample"`
	ErrorCounters   ErrorCounters `json:"error_counters"`
	Timestamps      Timestamps    `json:"timestamps"`
}

type Driver struct {
	cfg   Config
	port  *serial.Port
	mu    sync.RWMutex
	state Status
	wg    sync.WaitGroup
}

func (d *Driver) logf(level string, format string, args ...any) {
	lvl := map[string]int{"debug": 10, "info": 20, "warn": 30, "error": 40}
	cur := lvl[d.cfg.LogLevel]
	msgLvl := lvl[level]
	if msgLvl >= cur {
		log.Printf("%s: "+format, append([]any{strings.ToUpper(level)}, args...)...)
	}
}

func toParity(p string) serial.Parity {
	switch p {
	case "E":
		return serial.ParityEven
	case "O":
		return serial.ParityOdd
	default:
		return serial.ParityNone
	}
}

func toStopBits(sb int) serial.StopBits {
	if sb == 2 {
		return serial.Stop2
	}
	return serial.Stop1
}

func (d *Driver) connect() error {
	c := &serial.Config{
		Name:        d.cfg.SerialPort,
		Baud:        d.cfg.SerialBaud,
		ReadTimeout: d.cfg.SerialChunkTimeout(),
		Size:        byte(d.cfg.SerialDataBits),
		Parity:      toParity(d.cfg.SerialParity),
		StopBits:    toStopBits(d.cfg.SerialStopBits),
	}
	p, err := serial.OpenPort(c)
	if err != nil {
		return err
	}
	d.port = p
	d.mu.Lock()
	d.state.SerialConnected = true
	d.mu.Unlock()
	log.Printf("INFO: serial connected to %s @ %dbps %d%s%d", d.cfg.SerialPort, d.cfg.SerialBaud, d.cfg.SerialDataBits, d.cfg.SerialParity, d.cfg.SerialStopBits)
	return nil
}

func (d *Driver) closePort() {
	if d.port != nil {
		_ = d.port.Close()
		d.port = nil
	}
	d.mu.Lock()
	d.state.SerialConnected = false
	d.mu.Unlock()
	log.Printf("WARN: serial disconnected")
}

func clamp(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func (d *Driver) readRegistersContiguous(slave byte, regs []uint16) (map[uint16]uint16, error) {
	// Determine if regs are contiguous
	arr := make([]uint16, len(regs))
	copy(arr, regs)
	sort.Slice(arr, func(i, j int) bool { return arr[i] < arr[j] })
	start := arr[0]
	end := arr[len(arr)-1]
	if int(end-start+1) == len(regs) {
		qty := end - start + 1
		vals, err := ReadHoldingRegisters(d.port, byte(d.cfg.ModbusSlaveID), start, qty, d.cfg.ReadTimeout())
		if err != nil {
			return nil, err
		}
		res := make(map[uint16]uint16, len(regs))
		for i := range vals {
			off := start + uint16(i)
			res[off] = vals[i]
		}
		return res, nil
	}
	// Not contiguous, read individually
	res := make(map[uint16]uint16, len(regs))
	for _, r := range regs {
		vals, err := ReadHoldingRegisters(d.port, byte(d.cfg.ModbusSlaveID), r, 1, d.cfg.ReadTimeout())
		if err != nil {
			return nil, err
		}
		res[r] = vals[0]
	}
	return res, nil
}

func (d *Driver) pollOnce() error {
	d.mu.Lock()
	d.state.Timestamps.LastPollAttempt = time.Now().UTC().Format(time.RFC3339Nano)
	d.mu.Unlock()

	regs := []uint16{uint16(d.cfg.RegTemperature), uint16(d.cfg.RegHumidity), uint16(d.cfg.RegSampleInterval)}
	vals, err := d.readRegistersContiguous(byte(d.cfg.ModbusSlaveID), regs)
	if err != nil {
		var mErr ModbusError
		if errors.As(err, &mErr) {
			switch mErr.Type {
			case ErrTimeout:
				d.mu.Lock(); d.state.ErrorCounters.Timeouts++; d.mu.Unlock()
			case ErrCRC:
				d.mu.Lock(); d.state.ErrorCounters.CRCErrors++; d.mu.Unlock()
			case ErrException, ErrIO:
				d.mu.Lock(); d.state.ErrorCounters.IOErrors++; d.mu.Unlock()
			}
		} else {
			d.mu.Lock(); d.state.ErrorCounters.IOErrors++; d.mu.Unlock()
		}
		d.mu.Lock()
		d.state.ModbusSlaveOK = false
		d.state.Timestamps.LastError = time.Now().UTC().Format(time.RFC3339Nano)
		d.mu.Unlock()
		return err
	}
	tempRaw := vals[uint16(d.cfg.RegTemperature)]
	humRaw := vals[uint16(d.cfg.RegHumidity)]
	sampleRaw := vals[uint16(d.cfg.RegSampleInterval)]

	// Convert
	temp := float64(tempRaw) * d.cfg.ScaleTemperature
	hum := float64(humRaw) * d.cfg.ScaleHumidity
	sample := clamp(int(sampleRaw), 1, 60)

	d.mu.Lock()
	d.state.LastSample = Sample{
		TemperatureC:      temp,
		HumidityRH:        hum,
		SampleIntervalSec: sample,
		RawTemperature:    tempRaw,
		RawHumidity:       humRaw,
	}
	d.state.ModbusSlaveOK = true
	d.state.Timestamps.LastSuccess = time.Now().UTC().Format(time.RFC3339Nano)
	d.mu.Unlock()
	return nil
}

func (d *Driver) backoffSleep(attempt int) {
	ms := float64(d.cfg.BackoffBaseMs) * math.Pow(2, float64(attempt))
	if ms > float64(d.cfg.BackoffMaxMs) {
		ms = float64(d.cfg.BackoffMaxMs)
	}
	t := time.Duration(int(ms)) * time.Millisecond
	time.Sleep(t)
}

func (d *Driver) runPoller(ctx context.Context) {
	defer d.wg.Done()
	connAttempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if d.port == nil {
			if err := d.connect(); err != nil {
				log.Printf("ERROR: connect failed: %v", err)
				d.mu.Lock(); d.state.ErrorCounters.Reconnects++; d.mu.Unlock()
				connAttempt++
				d.backoffSleep(connAttempt)
				continue
			}
			connAttempt = 0
		}

		// Read with retries
		var ok bool
		for attempt := 0; attempt < d.cfg.RetryMax; attempt++ {
			if err := d.pollOnce(); err != nil {
				log.Printf("WARN: poll attempt %d failed: %v", attempt+1, err)
				d.mu.Lock(); d.state.ErrorCounters.Retries++; d.mu.Unlock()
				d.backoffSleep(attempt)
				select {
				case <-ctx.Done():
					return
				default:
				}
				continue
			}
			ok = true
			break
		}
		if !ok {
			// Force reconnect
			d.closePort()
			d.mu.Lock(); d.state.ErrorCounters.Reconnects++; d.mu.Unlock()
			// short backoff before next attempt
			d.backoffSleep(1)
			continue
		}

		// Sleep until next poll
		interval := time.Duration(d.cfg.PollIntervalSec) * time.Second
		d.mu.RLock()
		if d.cfg.PollUseSampleInterval && d.state.LastSample.SampleIntervalSec > 0 {
			interval = time.Duration(d.state.LastSample.SampleIntervalSec) * time.Second
		}
		d.mu.RUnlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (d *Driver) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.RLock()
	st := d.state
	d.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(st)
}

func main() {
	cfg := LoadConfig()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	driver := &Driver{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())

	// Start poller
	driver.wg.Add(1)
	go driver.runPoller(ctx)

	// HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/status", driver.statusHandler)

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort), Handler: mux}
	go func() {
		log.Printf("INFO: HTTP server listening on %s:%d", cfg.HTTPHost, cfg.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ERROR: HTTP server: %v", err)
			cancel()
		}
	}()

	// Wait for termination
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	log.Printf("INFO: shutting down...")
	cancel()
	_ = srv.Shutdown(context.Background())
	if driver.port != nil {
		driver.closePort()
	}
	driver.wg.Wait()
	log.Printf("INFO: stopped")
}
