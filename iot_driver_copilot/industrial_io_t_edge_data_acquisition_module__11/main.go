package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type StatusResponse struct {
	Status         string    `json:"status"`
	ModbusAddr     string    `json:"modbus_addr"`
	UnitID         int       `json:"unit_id"`
	Connected      bool      `json:"connected"`
	LastUpdate     string    `json:"last_update"`
	Samples        uint64    `json:"samples_collected"`
	Retries        uint64    `json:"retries"`
	BackoffMS      int64     `json:"backoff_ms"`
	PollIntervalMS int64     `json:"poll_interval_ms"`
	UptimeS        int64     `json:"uptime_s"`
}

type AnalogResponse struct {
	Values map[string]float64 `json:"values"`
	TS     string             `json:"ts,omitempty"`
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	buf := NewSampleBuffer(cfg.AnalogCount)
	collector := NewCollector(cfg, buf)
	collector.Start()

	start := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		values, ts, connected, retries, backoff, samples := buf.Snapshot()
		_ = values // not used here but snapshot ensures consistency
		status := "down"
		if connected {
			status = "ok"
		}
		resp := StatusResponse{
			Status:         status,
			ModbusAddr:     cfg.ModbusTCPAddr,
			UnitID:         int(cfg.ModbusUnitID),
			Connected:      connected,
			LastUpdate:     ts.UTC().Format(time.RFC3339Nano),
			Samples:        samples,
			Retries:        retries,
			BackoffMS:      backoff.Milliseconds(),
			PollIntervalMS: cfg.PollInterval.Milliseconds(),
			UptimeS:        int64(time.Since(start).Seconds()),
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/analog", func(w http.ResponseWriter, r *http.Request) {
		vals, ts, connected, _, _, samples := buf.Snapshot()
		if samples == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no data yet"})
			return
		}
		chParam := strings.TrimSpace(r.URL.Query().Get("channels"))
		includeTS := parseBoolDefault(r.URL.Query().Get("include_ts"), false)

		selected := make([]int, 0)
		if chParam == "" {
			for i := 0; i < len(vals); i++ {
				selected = append(selected, i)
			}
		} else {
			parts := strings.Split(chParam, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" { continue }
				i, err := strconv.Atoi(p)
				if err != nil || i < 0 || i >= len(vals) {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid channel index: " + p})
					return
				}
				selected = append(selected, i)
			}
		}

		res := AnalogResponse{Values: make(map[string]float64)}
		for _, ch := range selected {
			res.Values[strconv.Itoa(ch)] = vals[ch]
		}
		if includeTS {
			res.TS = ts.UTC().Format(time.RFC3339Nano)
		}

		status := http.StatusOK
		if !connected {
			// still return data but indicate possibly stale by 200; clients can check /status or ts
			status = http.StatusOK
		}
		writeJSON(w, status, res)
	})

	httpServer := &http.Server{
		Addr:    cfg.HTTPHost + ":" + strconv.Itoa(cfg.HTTPPort),
		Handler: withLogging(mux),
	}

	go func() {
		log.Printf("HTTP server listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("shutdown signal received")
	collector.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Printf("driver exited cleanly")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseBoolDefault(v string, def bool) bool {
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %v", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
