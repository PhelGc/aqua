// Package web sirve el dashboard HTTP+SSE de aqua y la API mínima que
// la UI usa para mandar comandos y descargar reportes.
package web

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

	"aqua/internal/agent"
	"aqua/internal/events"
)

//go:embed assets/*
var webFS embed.FS

const (
	defaultWebAddr    = "127.0.0.1:7777"
	webEventBuffer    = 128
	webHistoryMaxLen  = 500
	webRequestTimeout = 30 * time.Minute
)

type webServer struct {
	agent *agent.Agent
	sink  *events.FanoutSink
	mu    sync.Mutex
	busy  bool
}

// Run levanta el dashboard HTTP+SSE y lo conecta al agente como events.Sink.
func Run(ctx context.Context, a *agent.Agent) error {
	addr := os.Getenv("AQUA_WEB_ADDR")
	if addr == "" {
		addr = defaultWebAddr
	}

	sink := events.NewFanout(webEventBuffer, webHistoryMaxLen)
	a.SetEvents(sink)

	s := &webServer{agent: a, sink: sink}

	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "assets")
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

	toolN := len(a.MCP().Tools())
	skillN := len(a.Skills().List())
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

	ch, history, cleanup := s.sink.Subscribe()
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

func writeSSE(w http.ResponseWriter, evt events.Event) {
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
			_ = s.agent.Reset()
			s.sink.Publish(events.Event{Type: "chat_system", Time: time.Now(), Payload: map[string]any{"text": "(historial limpio)"}})
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		default:
			if rendered, ok := s.agent.Skills().Render(cmd, args); ok {
				input = rendered
			}
		}
	}

	s.sink.Publish(events.Event{
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
		reply, artifact, err := s.agent.SendAndDispatch(ctx, s.agent.HistoryPtr(), s.agent.Sessions().Current(), input)
		if err != nil {
			s.sink.Publish(events.Event{
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
		s.sink.Publish(events.Event{
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
	dir := os.Getenv("REPORT_OUTPUT_DIR")
	if dir == "" {
		dir = "reports"
	}
	path, ok := safeReportPath(dir, rel)
	if !ok {
		http.Error(w, "ruta inválida", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	http.ServeFile(w, r, path)
}

// safeReportPath valida que rel apunte a un archivo .md dentro de baseDir.
// Devuelve el path absoluto resuelto cuando es seguro, ("", false) si:
//   - rel es absoluto (en Unix o Windows)
//   - rel contiene segmentos que escapan baseDir (..)
//   - rel no termina en .md
//
// Verificamos con filepath.Rel sobre los Abs en vez de buscar ".." en el string,
// porque "foo..bar.md" es legítimo y los chequeos por substring lo rechazan.
func safeReportPath(baseDir, rel string) (string, bool) {
	if rel == "" {
		return "", false
	}
	// Rechazar paths absolutos (incluye unidades Windows como C:\foo).
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return "", false
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", false
	}
	candidate := filepath.Join(absBase, filepath.FromSlash(rel))
	rel2, err := filepath.Rel(absBase, candidate)
	if err != nil {
		return "", false
	}
	// Si Rel devuelve algo que empieza con ".." el candidate está fuera del base.
	if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
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
		"session":  s.agent.Sessions().Current(),
		"messages": len(s.agent.History()),
	})
}
