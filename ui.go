package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

//go:embed web/index.html
var indexHTML string

//go:embed web/app.css
var appCSS string

//go:embed web/app.js
var appJS string

const (
	pollTimeout = 4 * time.Second
	cmdTimeout  = 3 * time.Second

	// A fresh multicast join takes a moment to reach the switch.
	joinSettle = 250 * time.Millisecond
)

// Server ties HTTP to the device. Commands need an open session.
type Server struct {
	dev *Device
	bus *EventBus
}

func (s *Server) Routes(mux *http.ServeMux) {
	// UI
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(indexHTML, "{{VERSION}}", version)))
	})
	mux.HandleFunc("GET /app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(appCSS))
	})
	mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(appJS))
	})

	// SSE -- this connection is what keeps the session to the device open.
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// Reads without network traffic: both answer from the cached state.
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/state", s.handleState)

	// Commands -- the only code in the program that sends to the device.
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/gain", s.handleGain)
	mux.HandleFunc("POST /api/flag", s.handleFlag)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// 409 for a closed session, 502 for a device that does not answer.
func deviceErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoSession) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeErr(w, http.StatusBadGateway, err)
}

type configPayload struct {
	Version   string    `json:"version"`
	Device    string    `json:"device"`
	GainMin   int       `json:"gain_min"`
	GainMax   int       `json:"gain_max"`
	Flags     []flagDef `json:"flags"`
	Lock48V   bool      `json:"lock_48v"`
	LingerSec float64   `json:"linger_seconds"`
}

func (s *Server) config() configPayload {
	return configPayload{
		Version:   version,
		Device:    s.dev.cfg.DeviceIP,
		GainMin:   gainMinDB,
		GainMax:   gainMaxDB,
		Flags:     flagDefs,
		Lock48V:   s.dev.cfg.LockPhantom,
		LingerSec: sessionLinger.Seconds(),
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.config())
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.dev.Snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Subscribe before the session opens so we do not miss the first messages.
	ch := s.bus.Subscribe()
	defer s.bus.Unsubscribe(ch)

	// Address comes from the UI field; empty falls back to -device.
	started, err := s.dev.Acquire(r.URL.Query().Get("device"))
	if err == nil {
		defer s.dev.Release()
	}

	send := func(v any) bool {
		b, mErr := json.Marshal(v)
		if mErr != nil {
			return true
		}
		if _, wErr := w.Write([]byte("data: " + string(b) + "\n\n")); wErr != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// A failed Acquire already published its error on the bus.
	if !send(map[string]any{"type": "hello", "config": s.config(), "state": s.dev.Snapshot()}) {
		return
	}

	// Opening the session reads the switch state in; a second window does not.
	if started {
		go func() {
			time.Sleep(joinSettle)
			_, _ = s.dev.Poll(pollTimeout)
		}()
	}

	// Local heartbeat to the browser only.
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case msg := <-ch:
			if _, err := w.Write([]byte("data: " + msg + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-s.bus.done:
			return
		}
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	st, err := s.dev.Poll(pollTimeout)
	if err != nil {
		deviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type gainRequest struct {
	DB int `json:"db"`
}

func (s *Server) handleGain(w http.ResponseWriter, r *http.Request) {
	var req gainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.DB < gainMinDB || req.DB > gainMaxDB {
		writeErr(w, http.StatusBadRequest, errors.New("gain must be between 10 and 65 dB"))
		return
	}
	st, err := s.dev.SetGain(req.DB, cmdTimeout)
	if err != nil {
		deviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type flagRequest struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
	Yes  bool   `json:"yes"` // explicit confirmation, required for 48V
}

func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	var req flagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	f, ok := flagByName(req.Name)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown switch: "+req.Name))
		return
	}
	if f.Danger && req.On && !req.Yes {
		writeErr(w, http.StatusForbidden, errors.New("48V requires explicit confirmation"))
		return
	}
	st, err := s.dev.SetFlag(req.Name, req.On, req.Yes, cmdTimeout)
	if err != nil {
		deviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
