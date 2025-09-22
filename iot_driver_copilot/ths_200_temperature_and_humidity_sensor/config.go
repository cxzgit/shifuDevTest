package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

type Settings struct {
	HTTPHost              string
	HTTPPort              string
	MQTTBroker            string
	MQTTTopic             string
	MQTTClientID          string
	MQTTUsername          string
	MQTTPassword          string
	PublishInterval       time.Duration
	MQTTConnectTimeout    time.Duration
	RetryBaseDelay        time.Duration
	RetryMaxDelay         time.Duration
	QoS                   byte
	ShutdownTimeout       time.Duration
}

func loadSettingsFromEnv() Settings {
	st := Settings{}

	st.HTTPHost = getEnv("HTTP_HOST", "0.0.0.0")
	st.HTTPPort = getEnv("HTTP_PORT", "8080")

	st.MQTTBroker = strings.TrimSpace(os.Getenv("MQTT_BROKER"))
	st.MQTTTopic = strings.TrimSpace(os.Getenv("MQTT_TOPIC"))
	st.MQTTClientID = strings.TrimSpace(os.Getenv("MQTT_CLIENT_ID"))
	st.MQTTUsername = os.Getenv("MQTT_USERNAME")
	st.MQTTPassword = os.Getenv("MQTT_PASSWORD")

	st.PublishInterval = time.Duration(getEnvInt("PUBLISH_INTERVAL_SEC", 10)) * time.Second
	st.MQTTConnectTimeout = time.Duration(getEnvInt("MQTT_CONNECT_TIMEOUT_SEC", 5)) * time.Second
	st.RetryBaseDelay = time.Duration(getEnvInt("MQTT_RETRY_BASE_DELAY_MS", 500)) * time.Millisecond
	st.RetryMaxDelay = time.Duration(getEnvInt("MQTT_RETRY_MAX_DELAY_SEC", 30)) * time.Second
	st.QoS = byte(getEnvInt("MQTT_QOS", 0))
	st.ShutdownTimeout = time.Duration(getEnvInt("SHUTDOWN_TIMEOUT_SEC", 5)) * time.Second

	if st.MQTTClientID == "" {
		st.MQTTClientID = "ths200-driver-" + randHex(6)
	}

	log.Printf("Settings: HTTP %s:%s, MQTT broker='%s', topic='%s', interval=%s, qos=%d", st.HTTPHost, st.HTTPPort, st.MQTTBroker, st.MQTTTopic, st.PublishInterval, st.QoS)
	return st
}

func getEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("Invalid int for %s='%s', using default %d", key, v, def)
		return def
	}
	return i
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
