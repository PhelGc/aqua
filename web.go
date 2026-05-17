package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

const (
	defaultWebAddr   = "127.0.0.1:7777"
	webEventBuffer   = 128
	webHistoryMaxLen = 500
	webRequestTimeout = 30 * time.Minute
)

// fanoutSink implementa EventSink fanout a todos los clientes SSE conectados.
// Guarda un historial corto para que clientes que se conectan tarde puedan
// reconstruir el estado actual.
type fanoutSink struct {
	mu      sync.Mutex
	clients map[chan Event]struct{}
	history []Event
}

func newFanoutSink() *fanoutSink {
	return &fanoutSink{clients: make(map[chan Event]struct{})}
}

func (f *fanoutSink) Publish(evt Event) {
	f.mu.Lock()
	f.history = append(f.history, evt)
	if len(f.history) > webHistoryMaxLen {
		f.history = f.history[len(f.history)-webHistoryMaxLen:]
	}
	for ch := range f.clients {
		// Non-blocking: si el cliente está lento, dropeamos en vez de bloquear el publisher.
		select {
		case ch <- evt:
		default:
		}
	}
	f.mu.Unlock()
}

func (f *fanoutSink) subscribe() (<-chan Event, []Event, func()) {
	ch := make(chan Event, webEventBuffer)
	f.mu.Lock()
	f.clients[ch] = struct{}{}
	histCopy := make([]Event, len(f.history))
	copy(histCopy, f.history)
	f.mu.Unlock()
	cleanup := func() {
		f.mu.Lock()
		delete(f.clients, ch)
		f.mu.Unlock()
		close(ch)
	}
	return ch, histCopy, cleanup
}

type webServer struct {
	agent *agent
	sink  *fanoutSink
	mu    sync.Mutex
	busy  bool
}

// runWeb levanta el dashboard HTTP+SSE y lo conecta al agente como EventSink.
func runWeb(ctx context.Context, a *agent) error {
	addr := os.Getenv("AQUA_WEB_ADDR")
	if addr == "" {
		addr = defaultWebAddr
	}

	sink := newFanoutSink()
	a.events = sink

	s := &webServer{agent: a, sink: sink}

	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("preparando assets embebidos: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/command", s.handleCommand)
	mux.HandleFunc("/reports/", s.handleReport)
	mux.HandleFunc("/api/state", s.handleState)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	toolN := len(a.mcp.tools())
	skillN := len(a.skills.list())
	fmt.Printf("aqua · modo: ui · http://%s · %d tools · %d skills\n", addr, toolN, skillN)
	fmt.Println("ctrl+c para salir")

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *webServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, history, cleanup := s.sink.subscribe()
	defer cleanup()

	for _, evt := range history {
		writeSSE(w, evt)
	}
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, evt)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, evt Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

type commandReq struct {
	Text string `json:"text"`
}

func (s *webServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req commandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "json inválido: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		http.Error(w, "texto vacío", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		http.Error(w, "ya hay un comando en progreso, esperá a que termine", http.StatusConflict)
		return
	}
	s.busy = true
	s.mu.Unlock()

	input := req.Text
	if strings.HasPrefix(input, "/") {
		cmd, args, _ := strings.Cut(input[1:], " ")
		args = strings.TrimSpace(args)
		switch cmd {
		case "reset":
			s.agent.history = nil
			_ = s.agent.sessions.save(s.agent.sessions.current(), s.agent.history)
			s.sink.Publish(Event{Type: "chat_system", Time: time.Now(), Payload: map[string]any{"text": "(historial limpio)"}})
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		default:
			if rendered, ok := s.agent.skills.render(cmd, args); ok {
				input = rendered
			}
		}
	}

	s.sink.Publish(Event{
		Type:    "chat_user",
		Time:    time.Now(),
		Payload: map[string]any{"text": req.Text},
	})

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))

	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), webRequestTimeout)
		defer cancel()
		reply, artifact, err := s.agent.sendAndDispatch(ctx, &s.agent.history, s.agent.sessions.current(), input)
		if err != nil {
			s.sink.Publish(Event{
				Type:    "chat_error",
				Time:    time.Now(),
				Payload: map[string]any{"error": err.Error()},
			})
			return
		}
		payload := map[string]any{"text": reply}
		if artifact != "" {
			payload["artifact"] = artifact
		}
		s.sink.Publish(Event{
			Type:    "chat_assistant",
			Time:    time.Now(),
			Payload: payload,
		})
	}()
}

// handleReport sirve archivos dentro de reports/ con protección contra path traversal.
func (s *webServer) handleReport(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/reports/")
	if rel == "" {
		http.NotFound(w, r)
		return
	}
	cleaned := filepath.Clean(rel)
	if strings.Contains(cleaned, "..") || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, `\`) {
		http.Error(w, "ruta inválida", http.StatusBadRequest)
		return
	}
	dir := os.Getenv("REPORT_OUTPUT_DIR")
	if dir == "" {
		dir = "reports"
	}
	path := filepath.Join(dir, cleaned)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	http.ServeFile(w, r, path)
}

// handleState devuelve estado actual (rest informativo). No usado por la UI
// principal pero útil para debug.
func (s *webServer) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	busy := s.busy
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"busy":     busy,
		"session":  s.agent.sessions.current(),
		"messages": len(s.agent.history),
	})
}
