package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

type HTTPApp struct {
	cfg       *Config
	collector *Collector
	mqtt      *MQTTManager
}

func NewHTTPServer(cfg *Config, collector *Collector, mqtt *MQTTManager) *http.Server {
	app := &HTTPApp{cfg: cfg, collector: collector, mqtt: mqtt}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", app.handleStatus)
	mux.HandleFunc("/config/publish", app.handleConfigPublish)

	return &http.Server{
		Addr:    cfg.HTTPHost + ":" + cfg.HTTPPort,
		Handler: mux,
	}
}

func (a *HTTPApp) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	status := a.collector.Status()
	mqttState := "disabled"
	if a.mqtt != nil {
		if a.mqtt.IsConnected() { mqttState = "connected" } else { mqttState = "disconnected" }
	}
	status["mqtt"] = mqttState

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *HTTPApp) handleConfigPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil { http.Error(w, "failed to read body", http.StatusBadRequest); return }
	var req struct{ IntervalMs int `json:"interval_ms"` }
	if err := json.Unmarshal(body, &req); err != nil || req.IntervalMs <= 0 {
		http.Error(w, "invalid JSON or interval_ms", http.StatusBadRequest)
		return
	}

	// Prefer MQTT if configured, otherwise Modbus register if available
	var via string
	if a.mqtt != nil && a.cfg.MQTTCommandTopic != "" {
		if err := a.mqtt.SetPublishIntervalMQTT(req.IntervalMs); err != nil {
			log.Printf("mqtt set publish interval error: %v", err)
			http.Error(w, "mqtt publish failed", http.StatusBadGateway)
			return
		}
		via = "mqtt"
	} else if a.cfg.ModbusPubIntervalReg != nil {
		if err := a.collector.SetPublishIntervalModbus(req.IntervalMs); err != nil {
			log.Printf("modbus write interval error: %v", err)
			http.Error(w, "modbus write failed", http.StatusBadGateway)
			return
		}
		via = "modbus"
	} else {
		http.Error(w, "no publish interval method configured (need MQTT_COMMAND_TOPIC/MQTT_BROKER or MODBUS_PUB_INTERVAL_REG)", http.StatusBadRequest)
		return
	}

	resp := map[string]any{"status": "ok", "interval_ms": req.IntervalMs, "via": via}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
