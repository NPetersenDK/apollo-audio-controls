package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var version = "dev"

// --- EventBus for SSE ---

type EventBus struct {
	mu      sync.Mutex
	clients map[chan string]struct{}

	done      chan struct{} // closed on shutdown to release SSE handlers
	closeOnce sync.Once
}

func NewEventBus() *EventBus {
	return &EventBus{
		clients: make(map[chan string]struct{}),
		done:    make(chan struct{}),
	}
}

func (eb *EventBus) Close() {
	eb.closeOnce.Do(func() { close(eb.done) })
}

func (eb *EventBus) Subscribe() chan string {
	ch := make(chan string, 100)
	eb.mu.Lock()
	eb.clients[ch] = struct{}{}
	eb.mu.Unlock()
	return ch
}

func (eb *EventBus) Unsubscribe(ch chan string) {
	eb.mu.Lock()
	delete(eb.clients, ch)
	eb.mu.Unlock()
}

func (eb *EventBus) Publish(data string) {
	eb.mu.Lock()
	for ch := range eb.clients {
		select {
		case ch <- data:
		default:
		}
	}
	eb.mu.Unlock()
}

func (eb *EventBus) PublishJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	eb.Publish(string(b))
}

// --- logging ---

func setupLogging() {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	log.SetOutput(os.Stdout)
	log.SetFlags(0)
}

func stdlogger() *log.Logger {
	return slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"dur", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

// statusWriter keeps the status code and http.Flusher for SSE.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// --- env helpers ---

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func main() {
	var (
		port    = flag.String("port", env("PORT", "8090"), "HTTP port")
		listen  = flag.String("listen", env("LISTEN", ""), "listen address, e.g. 127.0.0.1 (empty = all interfaces)")
		device  = flag.String("device", env("E1X_DEVICE", ""), "device IP to prefill in the interface")
		iface   = flag.String("iface", env("E1X_IFACE", ""), "local interface IP on the Dante network (empty = work it out)")
		lock48v = flag.Bool("lock-48v", envBool("E1X_LOCK_48V", false), "block 48V entirely, including from the UI")
	)
	flag.Parse()

	setupLogging()

	bus := NewEventBus()
	dev := NewDevice(Config{
		DeviceIP:    *device,
		IfaceIP:     *iface,
		LockPhantom: *lock48v,
	}, bus)

	srv := &Server{dev: dev, bus: bus}
	mux := http.NewServeMux()
	srv.Routes(mux)

	addr := fmt.Sprintf("%s:%s", *listen, *port)
	httpServer := &http.Server{
		Addr:     addr,
		Handler:  logMiddleware(mux),
		ErrorLog: stdlogger(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("apollo-e1x web interface listening",
			"addr", addr, "device", *device, "version", version)
		slog.Info("no traffic is sent to the device until a browser connects")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("http server failed", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	// Release the SSE connections first, or Shutdown blocks.
	bus.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	dev.Shutdown()
	slog.Info("stopped")
}
