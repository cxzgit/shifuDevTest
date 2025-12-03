package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime configuration populated from environment variables.
type Config struct {
	HTTPHost          string
	HTTPPort          int
	Device            string
	Width             int
	Height            int
	FrameRate         int
	PixelFormat       string // "MJPEG" or "YUYV"
	Autostart         bool
	RetryBaseMs       int
	RetryMaxMs        int
	CaptureTimeoutMs  int
	MMapBuffers       int
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func parseIntEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func parseBoolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	s := strings.ToLower(strings.TrimSpace(v))
	return s == "1" || s == "true" || s == "yes" || s == "on"
}

func LoadConfig() (Config, error) {
	cfg := Config{
		HTTPHost:         getenv("HTTP_HOST", "0.0.0.0"),
		HTTPPort:         parseIntEnv("HTTP_PORT", 8080),
		Device:           getenv("CAM_DEVICE", "/dev/video0"),
		Width:            parseIntEnv("CAM_WIDTH", 640),
		Height:           parseIntEnv("CAM_HEIGHT", 480),
		FrameRate:        parseIntEnv("CAM_FRAME_RATE", 30),
		PixelFormat:      strings.ToUpper(strings.TrimSpace(getenv("CAM_PIXEL_FORMAT", "MJPEG"))),
		Autostart:        parseBoolEnv("AUTOSTART", false),
		RetryBaseMs:      parseIntEnv("RETRY_BASE_MS", 250),
		RetryMaxMs:       parseIntEnv("RETRY_MAX_MS", 5000),
		CaptureTimeoutMs: parseIntEnv("CAPTURE_TIMEOUT_MS", 1000),
		MMapBuffers:      parseIntEnv("MMAP_BUFFERS", 4),
	}

	if cfg.HTTPPort <= 0 || cfg.HTTPPort > 65535 {
		return cfg, fmt.Errorf("invalid HTTP_PORT: %d", cfg.HTTPPort)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return cfg, fmt.Errorf("invalid resolution: %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.FrameRate <= 0 {
		return cfg, fmt.Errorf("invalid frame rate: %d", cfg.FrameRate)
	}
	pf := strings.ToUpper(cfg.PixelFormat)
	if pf != "MJPEG" && pf != "YUYV" {
		return cfg, fmt.Errorf("invalid CAM_PIXEL_FORMAT, must be MJPEG or YUYV")
	}
	if cfg.MMapBuffers < 2 {
		cfg.MMapBuffers = 2
	}
	return cfg, nil
}

// FourCC maps human-readable pixel format to V4L2 fourcc value.
func (c Config) FourCC() uint32 {
	switch strings.ToUpper(c.PixelFormat) {
	case "MJPEG":
		return FourCCMJPG
	case "YUYV":
		return FourCCYUYV
	default:
		return FourCCMJPG
	}
}

// Clone returns a shallow copy of the config.
func (c Config) Clone() Config { return c }
