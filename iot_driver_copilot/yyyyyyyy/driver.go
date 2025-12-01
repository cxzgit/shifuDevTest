package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	if err := ensureHTTPURL(cfg.DeviceBaseURL); err != nil {
		log.Fatalf("device url error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// capture signals
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	buf := &SampleBuffer{}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		CollectLoop(ctx, cfg, buf)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/device/info", func(w http.ResponseWriter, r *http.Request) {
		data, ts, ok := buf.Get()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "{\"error\":\"no data available yet\"}")
			return
		}
		// Log request and serve latest sample
		log.Printf("http: /device/info served at %s (last update %s, size=%dB)", time.Now().Format(time.RFC3339), ts.Format(time.RFC3339), len(data))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	addr := net.JoinHostPort(cfg.HTTPHost, fmt.Sprintf("%d", cfg.HTTPPort))
	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("http: listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http: server error: %v", err)
			// If server fails, cancel context to stop collector
			cancel()
		}
	}()

	// Wait for termination signal
	s := <-sigCh
	log.Printf("main: received signal %s, shutting down", s.String())
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http: graceful shutdown error: %v", err)
	} else {
		log.Printf("http: server stopped")
	}

	wg.Wait()
	log.Printf("main: exited cleanly")
}
