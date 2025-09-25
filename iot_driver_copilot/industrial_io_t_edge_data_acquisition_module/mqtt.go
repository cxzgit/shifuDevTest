package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type MQTTManager struct {
	cfg       *Config
	client    mqtt.Client
	connected atomic.Bool
}

func NewMQTTManager(cfg *Config) (*MQTTManager, error) {
	if cfg.MQTTBroker == "" {
		return &MQTTManager{cfg: cfg}, nil
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.MQTTBroker)
	opts.SetClientID(cfg.MQTTClientID)
	if cfg.MQTTUsername != "" {
		opts.SetUsername(cfg.MQTTUsername)
		opts.SetPassword(cfg.MQTTPassword)
	}
	// allow tcp by default; if ssl scheme used, accept default tls config with InsecureSkipVerify false
	opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: false})
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(2 * time.Second)
	m := &MQTTManager{cfg: cfg}
	opts.OnConnect = func(c mqtt.Client) {
		m.connected.Store(true)
		log.Printf("mqtt connected to %s", cfg.MQTTBroker)
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		m.connected.Store(false)
		log.Printf("mqtt connection lost: %v", err)
	}
	m.client = mqtt.NewClient(opts)
	return m, nil
}

func (m *MQTTManager) Connect() error {
	if m.client == nil { return nil }
	to := 5 * time.Second
	if token := m.client.Connect(); token.WaitTimeout(to) && token.Error() != nil {
		return token.Error()
	}
	if !tokenWaitOk(m.client.OptionsReader().ConnectTimeout()) { /* noop */ }
	return nil
}

func tokenWaitOk(_ time.Duration) bool { return true }

func (m *MQTTManager) IsConnected() bool {
	if m.client == nil { return false }
	return m.connected.Load()
}

func (m *MQTTManager) Close() {
	if m.client != nil { m.client.Disconnect(250) }
}

func (m *MQTTManager) SetPublishIntervalMQTT(intervalMs int) error {
	if m.client == nil || m.cfg.MQTTCommandTopic == "" {
		return errors.New("mqtt not configured or MQTT_COMMAND_TOPIC missing")
	}
	payload := map[string]any{"interval_ms": intervalMs}
	b, _ := json.Marshal(payload)
	tok := m.client.Publish(m.cfg.MQTTCommandTopic, 1, false, b)
	if !tok.WaitTimeout(5 * time.Second) {
		return errors.New("mqtt publish timeout")
	}
	return tok.Error()
}
