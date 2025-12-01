package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPHost          string
	HTTPPort          int

	DeviceBaseURL     string
	DeviceInfoPath    string

	PollInterval      time.Duration
	RequestTimeout    time.Duration

	BackoffInitial    time.Duration
	BackoffMax        time.Duration
	BackoffMultiplier float64

	ShutdownGrace     time.Duration

	AuthType          string // "", "basic", "bearer"
	Username          string
	Password          string
	Token             string
}

func LoadConfig() (*Config, error) {
	var cfg Config

	var err error

	cfg.HTTPHost, err = mustEnv("HTTP_HOST")
	if err != nil {
		return nil, err
	}
	portStr, err := mustEnv("HTTP_PORT")
	if err != nil {
		return nil, err
	}
	cfg.HTTPPort, err = strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP_PORT: %w", err)
	}

	cfg.DeviceBaseURL, err = mustEnv("DEVICE_URL")
	if err != nil {
		return nil, err
	}
	cfg.DeviceInfoPath, err = mustEnv("DEVICE_INFO_PATH")
	if err != nil {
		return nil, err
	}

	pollStr, err := mustEnv("POLL_INTERVAL_SEC")
	if err != nil {
		return nil, err
	}
	pollSec, err := parsePositiveFloat(pollStr)
	if err != nil {
		return nil, fmt.Errorf("invalid POLL_INTERVAL_SEC: %w", err)
	}
	cfg.PollInterval = time.Duration(pollSec * float64(time.Second))

	reqTimeoutStr, err := mustEnv("REQUEST_TIMEOUT_SEC")
	if err != nil {
		return nil, err
	}
	reqTimeoutSec, err := parsePositiveFloat(reqTimeoutStr)
	if err != nil {
		return nil, fmt.Errorf("invalid REQUEST_TIMEOUT_SEC: %w", err)
	}
	cfg.RequestTimeout = time.Duration(reqTimeoutSec * float64(time.Second))

	backoffInitStr, err := mustEnv("BACKOFF_INITIAL_SEC")
	if err != nil {
		return nil, err
	}
	backoffInitSec, err := parsePositiveFloat(backoffInitStr)
	if err != nil {
		return nil, fmt.Errorf("invalid BACKOFF_INITIAL_SEC: %w", err)
	}
	cfg.BackoffInitial = time.Duration(backoffInitSec * float64(time.Second))

	backoffMaxStr, err := mustEnv("BACKOFF_MAX_SEC")
	if err != nil {
		return nil, err
	}
	backoffMaxSec, err := parsePositiveFloat(backoffMaxStr)
	if err != nil {
		return nil, fmt.Errorf("invalid BACKOFF_MAX_SEC: %w", err)
	}
	cfg.BackoffMax = time.Duration(backoffMaxSec * float64(time.Second))

	backoffMultStr, err := mustEnv("BACKOFF_MULT")
	if err != nil {
		return nil, err
	}
	cfg.BackoffMultiplier, err = strconv.ParseFloat(backoffMultStr, 64)
	if err != nil || cfg.BackoffMultiplier <= 1.0 {
		return nil, errors.New("invalid BACKOFF_MULT (must be float > 1.0)")
	}

	shutdownGraceStr, err := mustEnv("SHUTDOWN_GRACE_SEC")
	if err != nil {
		return nil, err
	}
	shutdownGraceSec, err := parsePositiveFloat(shutdownGraceStr)
	if err != nil {
		return nil, fmt.Errorf("invalid SHUTDOWN_GRACE_SEC: %w", err)
	}
	cfg.ShutdownGrace = time.Duration(shutdownGraceSec * float64(time.Second))

	cfg.AuthType = strings.ToLower(strings.TrimSpace(os.Getenv("DEVICE_AUTH_TYPE")))
	switch cfg.AuthType {
	case "":
		// no auth
	case "basic":
		cfg.Username = os.Getenv("DEVICE_USERNAME")
		cfg.Password = os.Getenv("DEVICE_PASSWORD")
		if cfg.Username == "" || cfg.Password == "" {
			return nil, errors.New("DEVICE_AUTH_TYPE=basic requires DEVICE_USERNAME and DEVICE_PASSWORD")
		}
	case "bearer":
		cfg.Token = os.Getenv("DEVICE_TOKEN")
		if cfg.Token == "" {
			return nil, errors.New("DEVICE_AUTH_TYPE=bearer requires DEVICE_TOKEN")
		}
	default:
		return nil, fmt.Errorf("unsupported DEVICE_AUTH_TYPE: %s", cfg.AuthType)
	}

	return &cfg, nil
}

func mustEnv(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("missing required env: %s", key)
	}
	return v, nil
}

func parsePositiveFloat(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f <= 0 {
		return 0, fmt.Errorf("value must be > 0, got %f", f)
	}
	return f, nil
}
