package main

import (
	contextpkg "context"
	encodingjson "encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, cancel := contextpkg.WithCancel(contextpkg.Background())
	defer cancel()

	dm := NewDeviceManager(cfg)
	if err := dm.Start(ctx); err != nil {
		log.Fatalf("failed to start device manager: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		st := dm.SnapshotStatus()
		w.Header().Set("Content-Type", "application/json")
		enc := encodingjson.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logJSON("info", "http_server_start", map[string]any{
			"addr": srv.Addr,
		})
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logJSON("error", "http_server_error", map[string]any{"error": err.Error()})
			cancel()
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logJSON("info", "shutdown_signal", nil)
	cancel()

	shutdownCtx, shutdownCancel := contextpkg.WithTimeout(contextpkg.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logJSON("error", "http_server_shutdown_error", map[string]any{"error": err.Error()})
	}
	if err := dm.Stop(shutdownCtx); err != nil {
		logJSON("error", "device_manager_stop_error", map[string]any{"error": err.Error()})
	}
	logJSON("info", "shutdown_complete", nil)
}

func logJSON(level, msg string, fields map[string]any) {
	entry := map[string]any{
		"ts":    time.Now().Format(time.RFC3339Nano),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		entry[k] = v
	}
	b, _ := encodingjson.Marshal(entry)
	log.Println(string(b))
}
