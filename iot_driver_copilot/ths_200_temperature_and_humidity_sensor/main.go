package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Driver struct {
	mu            sync.RWMutex
	settings      Settings
	mqttClient    mqtt.Client
	pubCtx        context.Context
	pubCancel     context.CancelFunc
	pubWG         sync.WaitGroup
	connected     bool
	lastError     string
	lastPublish   time.Time
	lastPayload   string
}

type mqttConfigRequest struct {
	BrokerAddress string `json:"broker_address"`
	Topic         string `json:"topic"`
}

func newDriver(st Settings) *Driver {
	return &Driver{settings: st}
}

func (d *Driver) startPublisherIfConfigured() {
	d.mu.Lock()
	broker := d.settings.MQTTBroker
	topic := d.settings.MQTTTopic
	d.mu.Unlock()
	if broker == "" || topic == "" {
		log.Printf("MQTT not configured. Waiting for /config/mqtt to be set.")
		return
	}
	d.restartPublisher()
}

func (d *Driver) restartPublisher() {
	d.stopPublisher()
	d.mu.Lock()
	d.pubCtx, d.pubCancel = context.WithCancel(context.Background())
	ctx := d.pubCtx
	st := d.settings
	d.mu.Unlock()

	d.pubWG.Add(1)
	go func() {
		defer d.pubWG.Done()
		d.runPublisher(ctx, st)
	}()
}

func (d *Driver) stopPublisher() {
	d.mu.Lock()
	if d.pubCancel != nil {
		log.Printf("Stopping publisher...")
		d.pubCancel()
	}
	client := d.mqttClient
	d.mqttClient = nil
	d.connected = false
	d.mu.Unlock()

	if client != nil {
		client.Disconnect(250)
	}

	d.pubWG.Wait()
}

func (d *Driver) runPublisher(ctx context.Context, st Settings) {
	// Build MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(st.MQTTBroker)
	if st.MQTTUsername != "" {
		opts.SetUsername(st.MQTTUsername)
		opts.SetPassword(st.MQTTPassword)
	}
	opts.SetClientID(st.MQTTClientID)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(false) // we'll manage retry manually with backoff
	opts.SetKeepAlive(30 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetConnectTimeout(st.MQTTConnectTimeout)
	opts.OnConnect = func(c mqtt.Client) {
		d.mu.Lock()
		d.connected = true
		d.lastError = ""
		d.mu.Unlock()
		log.Printf("Connected to MQTT broker %s", st.MQTTBroker)
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		d.mu.Lock()
		d.connected = false
		d.lastError = fmt.Sprintf("connection lost: %v", err)
		d.mu.Unlock()
		log.Printf("MQTT connection lost: %v", err)
	}

	client := mqtt.NewClient(opts)
	// store client
	d.mu.Lock()
	d.mqttClient = client
	d.mu.Unlock()

	// Exponential backoff connect loop
	backoff := st.RetryBaseDelay
	for {
		if ctx.Err() != nil {
			return
		}
		tok := client.Connect()
		if tok.WaitTimeout(st.MQTTConnectTimeout) && tok.Error() == nil {
			break
		}
		var err error
		if tok.Error() != nil {
			err = tok.Error()
		} else {
			err = errors.New("connect timeout")
		}
		d.mu.Lock()
		d.lastError = fmt.Sprintf("connect failed: %v", err)
		d.mu.Unlock()
		log.Printf("MQTT connect failed: %v; retrying in %s", err, backoff)
		select {
		case <-time.After(backoff):
			backoff = minDuration(st.RetryMaxDelay, backoff*2)
			continue
		case <-ctx.Done():
			return
		}
	}

	log.Printf("MQTT initial connection established. Starting publish loop to topic '%s' every %s", st.MQTTTopic, st.PublishInterval)
	ticker := time.NewTicker(st.PublishInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload := d.generateSensorPayload()
			d.mu.RLock()
			topic := d.settings.MQTTTopic
			qos := d.settings.QoS
			cl := d.mqttClient
			timeout := d.settings.MQTTConnectTimeout
			d.mu.RUnlock()

			if cl == nil {
				log.Printf("MQTT client not initialized, skipping publish")
				continue
			}
			if !cl.IsConnectionOpen() {
				log.Printf("MQTT not connected, skipping publish")
				continue
			}
			tok := cl.Publish(topic, qos, false, payload)
			if !tok.WaitTimeout(timeout) || tok.Error() != nil {
				perr := tok.Error()
				if perr == nil {
					perr = errors.New("publish timeout")
				}
				d.mu.Lock()
				d.lastError = fmt.Sprintf("publish failed: %v", perr)
				d.mu.Unlock()
				log.Printf("Publish failed: %v", perr)
				continue
			}
			d.mu.Lock()
			d.lastPublish = time.Now().UTC()
			d.lastPayload = string(payload)
			d.mu.Unlock()
			log.Printf("Published to %s: %s", topic, string(payload))
		}
	}
}

func (d *Driver) generateSensorPayload() []byte {
	// Simulate temperature and humidity readings in realistic ranges
	now := time.Now().UTC()
	secs := float64(now.Unix()%3600) / 3600.0 // within an hour cycle
	// Temperature swings between 20 and 30 C
	temp := 25 + 5*math.Sin(2*math.Pi*secs)
	// Humidity between 40% and 70%
	hum := 55 + 15*math.Sin(2*math.Pi*secs+1)
	reading := map[string]float64{
		"temperature": roundTo(temp, 1),
		"humidity":    roundTo(hum, 1),
	}
	b, _ := json.Marshal(reading)
	return b
}

func roundTo(v float64, places int) float64 {
	m := math.Pow(10, float64(places))
	return math.Round(v*m) / m
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// HTTP Handlers

func (d *Driver) handleGetConfigMQTT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.mu.RLock()
	resp := map[string]interface{}{
		"broker_address": d.settings.MQTTBroker,
		"topic":          d.settings.MQTTTopic,
		"client_id":      d.settings.MQTTClientID,
		"qos":            d.settings.QoS,
		"interval_sec":   int(d.settings.PublishInterval / time.Second),
	}
	d.mu.RUnlock()
	writeJSON(w, http.StatusOK, resp)
}

func (d *Driver) handlePutConfigMQTT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mqttConfigRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	if req.BrokerAddress == "" || req.Topic == "" {
		http.Error(w, "broker_address and topic are required", http.StatusBadRequest)
		return
	}

	log.Printf("Updating MQTT config: broker='%s', topic='%s'", req.BrokerAddress, req.Topic)

	// Apply config
	d.mu.Lock()
	d.settings.MQTTBroker = req.BrokerAddress
	d.settings.MQTTTopic = req.Topic
	d.mu.Unlock()

	// Restart publisher with new settings
	d.restartPublisher()

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d *Driver) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.mu.RLock()
	resp := map[string]interface{}{
		"connected":          d.connected,
		"publishing":         d.settings.MQTTBroker != "" && d.settings.MQTTTopic != "",
		"broker_address":     d.settings.MQTTBroker,
		"topic":              d.settings.MQTTTopic,
		"interval_sec":       int(d.settings.PublishInterval / time.Second),
		"last_publish_unix":  d.lastPublish.Unix(),
		"last_payload":       d.lastPayload,
		"last_error":         d.lastError,
	}
	d.mu.RUnlock()
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	st := loadSettingsFromEnv()
	drv := newDriver(st)

	mux := http.NewServeMux()
	mux.HandleFunc("/config/mqtt", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			drv.handleGetConfigMQTT(w, r)
		case http.MethodPut:
			drv.handlePutConfigMQTT(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/status", drv.handleGetStatus)

	httpServer := &http.Server{Addr: st.HTTPHost + ":" + st.HTTPPort, Handler: mux}

	// Start HTTP server
	go func() {
		log.Printf("HTTP server listening on %s:%s", st.HTTPHost, st.HTTPPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Start publisher if env provided
	drv.startPublisherIfConfigured()

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	s := <-sigCh
	log.Printf("Received signal %v, shutting down...", s)

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), st.ShutdownTimeout)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	drv.stopPublisher()
	log.Printf("Shutdown complete")
}
