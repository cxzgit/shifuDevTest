package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Camera implements V4L2 UVC camera handling and HTTP bridge.
type Camera struct {
	mu            sync.Mutex
	cfg           Config
	streaming     bool
	dev           v4l2Device
	lastFrame     atomic.Value // []byte
	lastUpdate    atomic.Value // time.Time
	frameSeq      uint64
	stopCh        chan struct{}
	stoppedCh     chan struct{}
}

func NewCamera(cfg Config) *Camera {
	c := &Camera{cfg: cfg}
	c.lastFrame.Store([]byte(nil))
	c.lastUpdate.Store(time.Time{})
	return c
}

func (c *Camera) Status() map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := map[string]interface{}{
		"device":       c.cfg.Device,
		"streaming":    c.streaming,
		"width":        c.cfg.Width,
		"height":       c.cfg.Height,
		"frame_rate":   c.cfg.FrameRate,
		"pixel_format": c.cfg.PixelFormat,
		"autostart":    c.cfg.Autostart,
		"last_update":  c.lastUpdate.Load().(time.Time).Format(time.RFC3339Nano),
		"frame_seq":    atomic.LoadUint64(&c.frameSeq),
	}
	return s
}

// ApplyConfig opens the device, tries to apply the configuration (without streaming), then closes.
func (c *Camera) ApplyConfig(cfg Config) error {
	if c.streaming {
		return errors.New("streaming active; stop first")
	}
	// Open, set format & fps, then close to validate.
	if err := c.dev.Open(cfg.Device); err != nil {
		return fmt.Errorf("open device: %w", err)
	}
	defer c.dev.Close()
	if err := c.dev.Configure(cfg.Width, cfg.Height, cfg.FourCC(), cfg.FrameRate); err != nil {
		return fmt.Errorf("configure device: %w", err)
	}
	return nil
}

func (c *Camera) Start() error {
	c.mu.Lock()
	if c.streaming {
		c.mu.Unlock()
		return nil
	}
	c.stopCh = make(chan struct{})
	c.stoppedCh = make(chan struct{})
	c.streaming = true
	cfg := c.cfg.Clone()
	c.mu.Unlock()

	go c.runStreaming(cfg)
	return nil
}

func (c *Camera) Stop() error {
	c.mu.Lock()
	if !c.streaming {
		c.mu.Unlock()
		return nil
	}
	close(c.stopCh)
	c.mu.Unlock()
	<-c.stoppedCh

	c.mu.Lock()
	c.streaming = false
	c.mu.Unlock()
	return nil
}

func (c *Camera) runStreaming(cfg Config) {
	log.Printf("[camera] stream loop starting: device=%s fmt=%s %dx%d @%dfps", cfg.Device, cfg.PixelFormat, cfg.Width, cfg.Height, cfg.FrameRate)
	defer close(c.stoppedCh)

	backoff := time.Duration(cfg.RetryBaseMs) * time.Millisecond
	maxBackoff := time.Duration(cfg.RetryMaxMs) * time.Millisecond
	capTimeout := cfg.CaptureTimeoutMs

	for {
		select {
		case <-c.stopCh:
			// Stop requested
			c.dev.Stop()
			c.dev.Close()
			log.Printf("[camera] stream loop stopped")
			return
		default:
		}

		// Initialize device
		if err := c.dev.Open(cfg.Device); err != nil {
			log.Printf("[camera] open failed: %v; retry in %v", err, backoff)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		log.Printf("[camera] device opened")

		if err := c.dev.Configure(cfg.Width, cfg.Height, cfg.FourCC(), cfg.FrameRate); err != nil {
			log.Printf("[camera] configure failed: %v; retry in %v", err, backoff)
			c.dev.Close()
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		log.Printf("[camera] device configured: %dx%d fmt=%s", cfg.Width, cfg.Height, cfg.PixelFormat)

		if err := c.dev.InitMMap(cfg.MMapBuffers); err != nil {
			log.Printf("[camera] mmap init failed: %v; retry in %v", err, backoff)
			c.dev.Close()
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		log.Printf("[camera] mmap buffers initialized")

		if err := c.dev.Start(); err != nil {
			log.Printf("[camera] stream start failed: %v; retry in %v", err, backoff)
			c.dev.Stop()
			c.dev.Close()
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		log.Printf("[camera] streaming started")
		backoff = time.Duration(cfg.RetryBaseMs) * time.Millisecond

		// Capture loop
		for {
			select {
			case <-c.stopCh:
				c.dev.Stop()
				c.dev.Close()
				log.Printf("[camera] stop requested; closing device")
				return
			default:
			}

			ptr, length, index, timeout, err := c.dev.Dequeue(capTimeout)
			if timeout {
				continue
			}
			if err != nil {
				log.Printf("[camera] dequeue error: %v; restarting", err)
				break
			}

			// Copy frame to Go memory
			if length > 0 && ptr != nil {
				buf := C.GoBytes(ptr, C.int(length))
				c.lastFrame.Store(buf)
				c.lastUpdate.Store(time.Now())
				atomic.AddUint64(&c.frameSeq, 1)
			}
			// Requeue buffer
			if err := c.dev.Enqueue(index); err != nil {
				log.Printf("[camera] enqueue error: %v; restarting", err)
				break
			}
		}

		// Restart on errors
		c.dev.Stop()
		c.dev.Close()
		log.Printf("[camera] restarting after error; backoff %v", backoff)
		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// Snapshot captures a single frame. If streaming is active, returns the latest frame.
func (c *Camera) Snapshot() ([]byte, string, error) {
	c.mu.Lock()
	streaming := c.streaming
	fmtStr := c.cfg.PixelFormat
	capTimeout := c.cfg.CaptureTimeoutMs
	device := c.cfg.Device
	width := c.cfg.Width
	height := c.cfg.Height
	fps := c.cfg.FrameRate
	fourcc := c.cfg.FourCC()
	c.mu.Unlock()

	if streaming {
		buf := c.lastFrame.Load().([]byte)
		if buf == nil || len(buf) == 0 {
			return nil, "", errors.New("no frame available")
		}
		ct := "application/octet-stream"
		if strings.ToUpper(fmtStr) == "MJPEG" {
			ct = "image/jpeg"
		}
		return buf, ct, nil
	}

	// Not streaming: open device temporarily, grab one frame, close
	var dev v4l2Device
	if err := dev.Open(device); err != nil {
		return nil, "", fmt.Errorf("open device: %w", err)
	}
	defer dev.Close()
	if err := dev.Configure(width, height, fourcc, fps); err != nil {
		return nil, "", fmt.Errorf("configure: %w", err)
	}
	if err := dev.InitMMap(2); err != nil {
		return nil, "", fmt.Errorf("init mmap: %w", err)
	}
	if err := dev.Start(); err != nil {
		return nil, "", fmt.Errorf("start: %w", err)
	}
	defer func() { dev.Stop() }()

	deadline := time.Now().Add(time.Duration(capTimeout) * time.Millisecond)
	for time.Now().Before(deadline) {
		ptr, length, index, timeout, err := dev.Dequeue(capTimeout)
		if timeout {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("dequeue: %w", err)
		}
		var out []byte
		if length > 0 && ptr != nil {
			out = C.GoBytes(ptr, C.int(length))
		}
		_ = dev.Enqueue(index)
		if len(out) > 0 {
			ct := "application/octet-stream"
			if strings.ToUpper(fmtStr) == "MJPEG" {
				ct = "image/jpeg"
			}
			return out, ct, nil
		}
	}
	return nil, "", errors.New("snapshot timeout")
}

// Configure updates the camera configuration and applies it using V4L2 without starting the stream.
func (c *Camera) Configure(newCfg Config) error {
	c.mu.Lock()
	if c.streaming {
		c.mu.Unlock()
		return errors.New("stream running; stop before reconfig")
	}
	c.mu.Unlock()
	// Validate typical resolutions
	if !(newCfg.Width == 640 && newCfg.Height == 480 || newCfg.Width == 1280 && newCfg.Height == 720 || newCfg.Width == 1920 && newCfg.Height == 1080) {
		log.Printf("[config] non-typical resolution %dx%d requested; attempting anyway", newCfg.Width, newCfg.Height)
	}
	if err := c.ApplyConfig(newCfg); err != nil {
		return err
	}
	// Save
	c.mu.Lock()
	c.cfg = newCfg
	c.mu.Unlock()
	return nil
}

// HTTP Handlers

func (c *Camera) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.Status())
}

func (c *Camera) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Device      *string `json:"device"`
		Width       *int    `json:"width"`
		Height      *int    `json:"height"`
		FrameRate   *int    `json:"frame_rate"`
		PixelFormat *string `json:"pixel_format"`
		Autostart   *bool   `json:"autostart"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	newCfg := c.cfg.Clone()
	if req.Device != nil { newCfg.Device = *req.Device }
	if req.Width != nil { newCfg.Width = *req.Width }
	if req.Height != nil { newCfg.Height = *req.Height }
	if req.FrameRate != nil { newCfg.FrameRate = *req.FrameRate }
	if req.PixelFormat != nil { newCfg.PixelFormat = strings.ToUpper(strings.TrimSpace(*req.PixelFormat)) }
	if req.Autostart != nil { newCfg.Autostart = *req.Autostart }

	// Validate basics
	if newCfg.FrameRate <= 0 {
		http.Error(w, "invalid frame_rate", http.StatusBadRequest)
		return
	}
	if strings.ToUpper(newCfg.PixelFormat) != "MJPEG" && strings.ToUpper(newCfg.PixelFormat) != "YUYV" {
		http.Error(w, "pixel_format must be MJPEG or YUYV", http.StatusBadRequest)
		return
	}

	if err := c.Configure(newCfg); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.Status())
}

func (c *Camera) handleStreamStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := c.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status":"started"})
}

func (c *Camera) handleStreamStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := c.Stop(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status":"stopped"})
}

func (c *Camera) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	buf, ct, err := c.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(buf)))
	_, _ = w.Write(buf)
}

func (c *Camera) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	c.mu.Lock()
	streaming := c.streaming
	fmtStr := strings.ToUpper(c.cfg.PixelFormat)
	c.mu.Unlock()
	if !streaming {
		http.Error(w, "stream not running; call /stream/start", http.StatusConflict)
		return
	}
	if fmtStr != "MJPEG" {
		http.Error(w, "current pixel_format is not MJPEG; switch via /config", http.StatusUnsupportedMediaType)
		return
	}

	boundary := "frame"
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	flusher, _ := w.(http.Flusher)

	prevSeq := atomic.LoadUint64(&c.frameSeq)
	for {
		if cn, ok := w.(http.CloseNotifier); ok {
			select {
			case <-cn.CloseNotify():
				return
			default:
			}
		}
		seq := atomic.LoadUint64(&c.frameSeq)
		if seq > prevSeq {
			buf := c.lastFrame.Load().([]byte)
			if buf != nil && len(buf) > 0 {
				_, _ = fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(buf))
				_, _ = w.Write(buf)
				_, _ = fmt.Fprintf(w, "\r\n")
				if flusher != nil { flusher.Flush() }
				prevSeq = seq
			}
			continue
		}
		// Polling wait
		time.Sleep(30 * time.Millisecond)
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	cam := NewCamera(cfg)

	// Optional autostart
	if cfg.Autostart {
		if err := cam.Start(); err != nil {
			log.Printf("[init] autostart failed: %v", err)
		} else {
			log.Printf("[init] autostart streaming")
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", cam.handleStatus)
	mux.HandleFunc("/config", cam.handleConfig)
	mux.HandleFunc("/stream/start", cam.handleStreamStart)
	mux.HandleFunc("/stream/stop", cam.handleStreamStop)
	mux.HandleFunc("/snapshot", cam.handleSnapshot)
	mux.HandleFunc("/stream", cam.handleStream)

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.HTTPHost, cfg.HTTPPort),
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		log.Printf("[main] shutting down HTTP server")
		_ = srv.Shutdown(context.Background())
		_ = cam.Stop()
	}()

	log.Printf("[main] listening on %s:%d", cfg.HTTPHost, cfg.HTTPPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
