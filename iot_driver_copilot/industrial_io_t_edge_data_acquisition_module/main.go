package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// MQTT manager (optional, only if broker provided)
	var mqttMgr *MQTTManager
	if cfg.MQTTBroker != "" {
		mqttMgr, err = NewMQTTManager(cfg)
		if err != nil {
			log.Fatalf("mqtt init error: %v", err)
		}
		if err := mqttMgr.Connect(); err != nil {
			log.Printf("mqtt connect error: %v", err)
		}
		defer mqttMgr.Close()
	}

	// Device collector (Modbus TCP)
	collector := NewCollector(cfg)
	collector.Start(ctx)
	defer collector.Stop()

	// HTTP server
	server := NewHTTPServer(cfg, collector, mqttMgr)

	go func() {
		addr := cfg.HTTPHost + ":" + cfg.HTTPPort
		log.Printf("HTTP listening on http://%s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	// graceful shutdown
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	s := <-sigC
	log.Printf("signal received: %v, shutting down", s)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	// collector and mqtt will be closed by defers/cancellation
}
