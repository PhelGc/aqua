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
	"aqua/internal/attachments"
	"aqua/internal/events"
	"aqua/internal/llm"
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
	atts  *attachments.Store
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

	atts, err := attachments.New(attachments.DefaultDir)
	if err != nil {
		return fmt.Errorf("inicializando attachments: %w", err)
	}

	s := &webServer{agent: a, sink: sink, atts: atts}

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
	mux.HandleFunc("/api/sessions", s.handleSessionsList)         // GET
	mux.HandleFunc("/api/sessions/new", s.handleSessionsNew)      // POST {name}
	mux.HandleFunc("/api/sessions/switch", s.handleSessionsSwitch) // POST {name}
	mux.HandleFunc("/api/sessions/", s.handleSessionsItem)         // DELETE /api/sessions/<name>
	mux.HandleFunc("/upload", s.handleUpload)                      // POST multipart

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
	Text        string   `json:"text"`
	Attachments []string `json:"attachments,omitempty"` // IDs de uploads previos
}

// handleCommand procesa un input del usuario y devuelve el resultado como un
// stream SSE. Tipos de eventos:
//
//	event: user      → echo del texto crudo del usuario
//	event: delta     → {content?, reasoning?} chunk del LLM
//	event: tool      → {name} cada vez que el agente invoca una tool MCP
//	event: error     → {message} algo falló (también incluido en done si aplica)
//	event: done      → {text, artifact} respuesta consolidada del turno
//	event: system    → {text} mensajes de sistema (reset, skill desconocida)
//
// Para slash commands locales (/reset, builtins) se responde solo con `system`
// y `done` sin tocar al LLM. Para skills (/<nombre>) se renderiza y se manda
// al agente como input.
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
	if req.Text == "" && len(req.Attachments) == 0 {
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
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(eventType string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Echo del input crudo (la UI lo usa para mostrar el bubble del usuario).
	send("user", map[string]any{"text": req.Text})

	input := req.Text
	if strings.HasPrefix(input, "/") {
		cmd, args, _ := strings.Cut(input[1:], " ")
		args = strings.TrimSpace(args)
		switch cmd {
		case "reset":
			if err := s.agent.Reset(); err != nil {
				send("error", map[string]any{"message": "reset: " + err.Error()})
			} else {
				send("system", map[string]any{"text": "(historial limpio)"})
			}
			send("done", map[string]any{"text": "", "artifact": ""})
			return
		case "exit", "quit":
			send("error", map[string]any{"message": "/" + cmd + " no aplica en la UI web"})
			send("done", map[string]any{"text": "", "artifact": ""})
			return
		default:
			rendered, found := s.agent.Skills().Render(cmd, args)
			if !found {
				send("error", map[string]any{"message": "comando desconocido: /" + cmd})
				send("done", map[string]any{"text": "", "artifact": ""})
				return
			}
			input = rendered
		}
	}

	// Prepend de los attachments al input. Cada uno se extrae a markdown
	// (tabla para xlsx/csv, texto para pdf/txt). Si la extracción falla,
	// metemos un placeholder con el error para que el LLM no tenga que
	// adivinar y el usuario sepa qué pasó.
	if len(req.Attachments) > 0 {
		var attBlock strings.Builder
		for _, id := range req.Attachments {
			text, err := s.atts.Extract(id)
			if err != nil {
				fmt.Fprintf(&attBlock, "[error leyendo attachment %s: %v]\n\n", id, err)
				continue
			}
			attBlock.WriteString(text)
			attBlock.WriteString("\n")
		}
		if input == "" {
			input = attBlock.String()
		} else {
			input = attBlock.String() + "\n---\n\n" + input
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), webRequestTimeout)
	defer cancel()

	// Filtramos chunks vacíos (delta sin content ni reasoning) para no
	// inundar la UI con eventos no-op.
	onDelta := func(d llm.StreamDelta) {
		if d.Content == "" && d.ReasoningContent == "" {
			// Tool-call: emitir uno por cada tool en el delta.
			for _, tc := range d.ToolCalls {
				if tc.Function.Name != "" {
					send("tool", map[string]any{"name": tc.Function.Name})
				}
			}
			return
		}
		out := map[string]any{}
		if d.Content != "" {
			out["content"] = d.Content
		}
		if d.ReasoningContent != "" {
			out["reasoning"] = d.ReasoningContent
		}
		send("delta", out)
	}

	text, artifact, err := s.agent.SendMainStreaming(ctx, s.agent.Sessions().Current(), input, onDelta)
	if err != nil {
		send("error", map[string]any{"message": err.Error()})
		send("done", map[string]any{"text": "", "artifact": ""})
		return
	}
	send("done", map[string]any{"text": text, "artifact": artifact})
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

// handleUpload recibe uno o más archivos en multipart/form-data (campo "file")
// y los guarda en el Store. Devuelve un array de Meta como JSON.
//
// Tope global: MaxFilesPerBatch archivos por request; MaxBytesPerFile por
// cada uno (validado dentro de SaveMultipart).
func (s *webServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// Limit del body completo: tope por archivo × cantidad + slack.
	r.Body = http.MaxBytesReader(w, r.Body,
		int64(attachments.MaxBytesPerFile)*int64(attachments.MaxFilesPerBatch)+1024*1024)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "multipart inválido: "+err.Error(), http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Error(w, "sin archivos (campo esperado: file)", http.StatusBadRequest)
		return
	}
	if len(files) > attachments.MaxFilesPerBatch {
		http.Error(w, fmt.Sprintf("máximo %d archivos por upload", attachments.MaxFilesPerBatch), http.StatusBadRequest)
		return
	}
	results := make([]attachments.Meta, 0, len(files))
	for _, fh := range files {
		m, err := s.atts.SaveMultipart(fh)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results = append(results, m)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
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

// ─── /api/sessions ───────────────────────────────────────────────────────────

type sessionItem struct {
	Name     string `json:"name"`
	Messages int    `json:"messages"`
}

type sessionsListResponse struct {
	Current string        `json:"current"`
	Items   []sessionItem `json:"items"`
}

// handleSessionsList responde GET con la lista de sesiones persistidas + la
// activa. Lee cada sesión para contar mensajes; si una falla la incluye con
// messages=-1 para que la UI muestre "?".
func (s *webServer) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	mgr := s.agent.Sessions()
	names, err := mgr.List()
	if err != nil {
		http.Error(w, "listando sesiones: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Aseguramos que la actual aparezca aunque todavía no esté persistida.
	cur := mgr.Current()
	hasCur := false
	for _, n := range names {
		if n == cur {
			hasCur = true
			break
		}
	}
	if !hasCur && cur != "" {
		names = append(names, cur)
	}
	items := make([]sessionItem, 0, len(names))
	for _, n := range names {
		count := -1
		if h, lerr := mgr.Load(n); lerr == nil {
			count = len(h)
		}
		items = append(items, sessionItem{Name: n, Messages: count})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessionsListResponse{Current: cur, Items: items})
}

type sessionNameReq struct {
	Name string `json:"name"`
}

// handleSessionsNew crea una sesión nueva, persiste la actual con su history
// vigente, y switchea hacia la nueva. El history del agente queda vacío.
func (s *webServer) handleSessionsNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.busyLocked() {
		http.Error(w, "hay un comando en progreso, esperá a que termine", http.StatusConflict)
		return
	}
	var req sessionNameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "json inválido: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name vacío", http.StatusBadRequest)
		return
	}
	mgr := s.agent.Sessions()
	// Persistir la actual antes de cambiar.
	if err := mgr.Save(mgr.Current(), s.agent.History()); err != nil {
		http.Error(w, "guardando sesión actual: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := mgr.SwitchTo(name); err != nil {
		http.Error(w, "switch: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.agent.SetHistory(nil)
	// Guardamos la nueva vacía para que aparezca en List() la próxima.
	if err := mgr.Save(name, nil); err != nil {
		http.Error(w, "guardando nueva sesión: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "current": name})
}

// handleSessionsSwitch cambia a una sesión existente. Persiste la actual y
// carga el history de la objetivo en el agente.
func (s *webServer) handleSessionsSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.busyLocked() {
		http.Error(w, "hay un comando en progreso, esperá a que termine", http.StatusConflict)
		return
	}
	var req sessionNameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "json inválido: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name vacío", http.StatusBadRequest)
		return
	}
	mgr := s.agent.Sessions()
	if name == mgr.Current() {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"current":"` + name + `"}`))
		return
	}
	if err := mgr.Save(mgr.Current(), s.agent.History()); err != nil {
		http.Error(w, "guardando sesión actual: "+err.Error(), http.StatusInternalServerError)
		return
	}
	hist, err := mgr.Load(name)
	if err != nil {
		http.Error(w, "cargando "+name+": "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := mgr.SwitchTo(name); err != nil {
		http.Error(w, "switch: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.agent.SetHistory(hist)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"current":  name,
		"messages": len(hist),
	})
}

// handleSessionsItem maneja DELETE /api/sessions/<name>. Borra el archivo
// pero no la activa (sessions.Manager ya lo valida). Usamos el path para
// el nombre, no el body, porque DELETE rara vez tiene body.
func (s *webServer) handleSessionsItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	if s.busyLocked() {
		http.Error(w, "hay un comando en progreso, esperá a que termine", http.StatusConflict)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	name = strings.TrimSpace(name)
	if name == "" {
		http.Error(w, "name requerido en path", http.StatusBadRequest)
		return
	}
	if err := s.agent.Sessions().Delete(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// busyLocked es un chequeo no-bloqueante: true si s.busy está seteado.
// Lo usamos para devolver 409 sin tomar el lock por mucho tiempo.
func (s *webServer) busyLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
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
		"messages": s.agent.HistoryLen(),
	})
}
