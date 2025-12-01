package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type SampleBuffer struct {
	mu         sync.RWMutex
	data       []byte
	lastUpdate time.Time
}

func (b *SampleBuffer) Update(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = make([]byte, len(data))
	copy(b.data, data)
	b.lastUpdate = time.Now()
}

func (b *SampleBuffer) Get() ([]byte, time.Time, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.data) == 0 {
		return nil, time.Time{}, false
	}
	out := make([]byte, len(b.data))
	copy(out, b.data)
	return out, b.lastUpdate, true
}

func joinURL(base, path string) string {
	if base == "" {
		return path
	}
	if path == "" {
		return base
	}
	b := base
	p := path
	if strings.HasSuffix(b, "/") && strings.HasPrefix(p, "/") {
		return b + p[1:]
	}
	if !strings.HasSuffix(b, "/") && !strings.HasPrefix(p, "/") {
		return b + "/" + p
	}
	return b + p
}

func validateJSON(data []byte) error {
	var js interface{}
	return json.Unmarshal(data, &js)
}

func CollectLoop(ctx context.Context, cfg *Config, buf *SampleBuffer) {
	client := &http.Client{Timeout: cfg.RequestTimeout}
	url := joinURL(cfg.DeviceBaseURL, cfg.DeviceInfoPath)

	backoff := cfg.BackoffInitial
	connected := false
	log.Printf("collector: starting, polling %s every %s", url, cfg.PollInterval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("collector: stopping")
			return
		default:
		}

		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			log.Printf("collector: build request error: %v", err)
			sleepCtx(ctx, backoff)
			backoff = nextBackoff(backoff, cfg)
			continue
		}
		req.Header.Set("Accept", "application/json")
		// auth
		switch cfg.AuthType {
		case "basic":
			req.SetBasicAuth(cfg.Username, cfg.Password)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}

		resp, err := client.Do(req)
		if err != nil {
			if connected {
				log.Printf("collector: device disconnect or request error: %v", err)
				connected = false
			} else {
				log.Printf("collector: request error: %v", err)
			}
			sleepCtx(ctx, backoff)
			backoff = nextBackoff(backoff, cfg)
			continue
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				if connected {
					log.Printf("collector: non-2xx status %d (disconnect)", resp.StatusCode)
					connected = false
				} else {
					log.Printf("collector: non-2xx status %d", resp.StatusCode)
				}
				sleepCtx(ctx, backoff)
				backoff = nextBackoff(backoff, cfg)
				return
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("collector: read body error: %v", err)
				sleepCtx(ctx, backoff)
				backoff = nextBackoff(backoff, cfg)
				return
			}
			if err := validateJSON(body); err != nil {
				log.Printf("collector: invalid JSON: %v", err)
				sleepCtx(ctx, backoff)
				backoff = nextBackoff(backoff, cfg)
				return
			}
			buf.Update(body)
			if !connected {
				log.Printf("collector: device connected")
				connected = true
			}
			log.Printf("collector: updated at %s (size=%dB, latency=%s)", time.Now().Format(time.RFC3339), len(body), time.Since(start))
			// reset backoff after success
			backoff = cfg.BackoffInitial
		}()

		// wait until next poll or context cancellation
		if !sleepCtx(ctx, cfg.PollInterval) {
			return
		}
	}
}

func nextBackoff(current time.Duration, cfg *Config) time.Duration {
	next := time.Duration(float64(current) * cfg.BackoffMultiplier)
	if next > cfg.BackoffMax {
		next = cfg.BackoffMax
	}
	return next
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Helper for ensuring required device URL format
func ensureHTTPURL(u string) error {
	if u == "" {
		return errors.New("empty URL")
	}
	lower := strings.ToLower(u)
	if !(strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) {
		return errors.New("DEVICE_URL must start with http:// or https://")
	}
	return nil
}
