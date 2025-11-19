package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	serial "go.bug.st/serial"
)

type Readings struct {
	Temperature float64   `json:"temperature"`
	Humidity    float64   `json:"humidity"`
	CO2         float64   `json:"co2"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ReadingBuffer struct {
	mu        sync.RWMutex
	connected bool
	last      Readings
}

func NewReadingBuffer() *ReadingBuffer {
	return &ReadingBuffer{}
}

func (b *ReadingBuffer) SetConnected(c bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connected = c
}

func (b *ReadingBuffer) Update(r Readings) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = r
	b.connected = true
}

func (b *ReadingBuffer) Get() (Readings, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.last, b.connected
}

func computeBackoff(base, max time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Exponential backoff: base * 2^(attempt-1)
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

func openSerial(cfg *Config) (serial.Port, error) {
	mode := &serial.Mode{
		BaudRate: cfg.BaudRate,
		DataBits: cfg.DataBits,
		StopBits: cfg.StopBits,
	}
	switch cfg.Parity {
	case "N":
		mode.Parity = serial.NoParity
	case "E":
		mode.Parity = serial.EvenParity
	case "O":
		mode.Parity = serial.OddParity
	}
	p, err := serial.Open(cfg.SerialPort, mode)
	if err != nil {
		return nil, err
	}
	_ = p.SetReadTimeout(cfg.ReadTimeout)
	return p, nil
}

func pollOnce(port serial.Port, cfg *Config) (Readings, error) {
	// Read each register individually with FC=0x03 and qty=1
	respT, err := readHoldingRegisters(port, cfg.SlaveID, cfg.RegTemp, 1, cfg.ReadTimeout)
	if err != nil {
		return Readings{}, fmt.Errorf("temp read failed: %w", err)
	}
	rawT := parseRegisterValue(respT, 0, cfg.TempSigned)

	respH, err := readHoldingRegisters(port, cfg.SlaveID, cfg.RegHum, 1, cfg.ReadTimeout)
	if err != nil {
		return Readings{}, fmt.Errorf("hum read failed: %w", err)
	}
	rawH := parseRegisterValue(respH, 0, cfg.HumSigned)

	respC, err := readHoldingRegisters(port, cfg.SlaveID, cfg.RegCO2, 1, cfg.ReadTimeout)
	if err != nil {
		return Readings{}, fmt.Errorf("co2 read failed: %w", err)
	}
	rawC := parseRegisterValue(respC, 0, cfg.CO2Signed)

	r := Readings{
		Temperature: float64(rawT) * cfg.ScaleTemp,
		Humidity:    float64(rawH) * cfg.ScaleHum,
		CO2:         float64(rawC) * cfg.ScaleCO2,
		UpdatedAt:   time.Now(),
	}
	return r, nil
}

func readingsHandler(buf *ReadingBuffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("method not allowed"))
			return
		}
		reading, connected := buf.Get()
		w.Header().Set("Content-Type", "application/json")
		if !connected || reading.UpdatedAt.IsZero() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not connected"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(reading)
	}
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	buf := NewReadingBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	var port serial.Port
	var portMu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		failCount := 0
		for {
			select {
			case <-ctx.Done():
				portMu.Lock()
				if port != nil {
					_ = port.Close()
					port = nil
				}
				portMu.Unlock()
				return
			default:
			}

			portMu.Lock()
			if port == nil {
				p, err := openSerial(cfg)
				if err != nil {
					portMu.Unlock()
					failCount++
					d := computeBackoff(cfg.BackoffBase, cfg.BackoffMax, failCount)
					log.Printf("serial connect failed: %v; retry in %s", err, d)
					time.Sleep(d)
					continue
				}
				port = p
				failCount = 0
				log.Printf("serial connected to %s (baud=%d %d%s%d)", cfg.SerialPort, cfg.BaudRate, cfg.DataBits, cfg.Parity, cfg.StopBits)
			}
			p := port
			portMu.Unlock()

			reading, err := pollOnce(p, cfg)
			if err != nil {
				buf.SetConnected(false)
				log.Printf("poll error: %v", err)
				portMu.Lock()
				if port != nil {
					_ = port.Close()
					port = nil
					log.Printf("serial disconnected")
				}
				portMu.Unlock()
				failCount++
				d := computeBackoff(cfg.BackoffBase, cfg.BackoffMax, failCount)
				log.Printf("retry in %s", d)
				time.Sleep(d)
				continue
			}
			buf.Update(reading)
			log.Printf("updated readings at %s: temp=%.3f hum=%.3f co2=%.3f", reading.UpdatedAt.Format(time.RFC3339), reading.Temperature, reading.Humidity, reading.CO2)
			time.Sleep(cfg.PollInterval)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/readings", readingsHandler(buf))
	addr := fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("HTTP server listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server error: %v", err)
			cancel()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("shutting down...")
	cancel()
	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(ctxShutdown)
	wg.Wait()
	log.Printf("exited")
}
