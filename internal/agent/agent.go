// Package agent es el núcleo de aqua: coordina el LLM, MCP, skills,
// sessions, scheduler, notifier y emisión de eventos. Los transports
// (terminal, discord, web) lo usan vía getters; no acceden a campos privados.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"aqua/internal/events"
	"aqua/internal/llm"
	"aqua/internal/mcp"
	"aqua/internal/notifier"
	"aqua/internal/scheduler"
	"aqua/internal/sessions"
	"aqua/internal/skills"
)

var errMaxToolIterations = errors.New("max tool iterations exceeded")

const (
	defaultEndpoint        = "https://opencode.ai/zen/go/v1/chat/completions"
	defaultModel           = "deepseek-v4-flash"
	defaultPersonalityPath = "personality.md"
	defaultMaxToolIters    = 16
)

// Agent es el núcleo del runtime. Mantiene la conversación principal y
// expone helpers que los transports invocan.
type Agent struct {
	endpoint    string
	model       string
	apiKey      string
	personality string
	// historyMu protege history y serializa SendMain. Discord usa convos por
	// usuario y scheduler usa history local, así que ninguno toca este campo;
	// el lock cubre el modo terminal/web sobre el agente principal.
	historyMu sync.Mutex
	history   []llm.Message
	http      *http.Client
	mcp       *mcp.Manager
	skills    *skills.Registry
	sessions  *sessions.Manager
	scheduler *scheduler.Scheduler
	// label identifica al agente en los logs de tool-call. Vacío = agente
	// principal (sale como [tool]); con valor sale como [tool/<label>].
	label string
	// events recibe eventos del runtime (tool-calls, orquestador, etc.).
	// nil = sin emisión (chat/discord modes funcionan igual). Workers
	// heredan el sink del padre en RunIsolated.
	events events.Sink
	// notifier despacha notificaciones a un canal externo (noop si no está
	// configurado el webhook).
	notifier notifier.Notifier
}

func loadPersonality() (string, error) {
	path := os.Getenv("OPENCODE_PERSONALITY")
	if path == "" {
		path = defaultPersonalityPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// New construye el Agent leyendo configuración desde env y arrancando el
// scheduler en background.
func New(ctx context.Context) (*Agent, error) {
	key := os.Getenv("OPENCODE_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENCODE_API_KEY no está definida")
	}
	endpoint := os.Getenv("OPENCODE_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	model := os.Getenv("OPENCODE_MODEL")
	if model == "" {
		model = defaultModel
	}
	personality, err := loadPersonality()
	if err != nil {
		return nil, fmt.Errorf("leyendo personalidad: %w", err)
	}
	cfg, err := mcp.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("leyendo config MCP: %w", err)
	}
	manager, err := mcp.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("iniciando MCP: %w", err)
	}
	sk, err := skills.Load()
	if err != nil {
		return nil, fmt.Errorf("cargando skills: %w", err)
	}
	sess, err := sessions.New()
	if err != nil {
		return nil, fmt.Errorf("iniciando sesiones: %w", err)
	}
	history, err := sess.Load(sess.Current())
	if err != nil {
		return nil, fmt.Errorf("cargando sesión %q: %w", sess.Current(), err)
	}
	sched, err := scheduler.New(scheduler.DefaultDir)
	if err != nil {
		return nil, fmt.Errorf("iniciando scheduler: %w", err)
	}
	a := &Agent{
		endpoint:    endpoint,
		model:       model,
		apiKey:      key,
		personality: personality,
		history:     history,
		http:        &http.Client{Timeout: 120 * time.Second},
		mcp:         manager,
		skills:      sk,
		sessions:    sess,
		scheduler:   sched,
		notifier:    notifier.NewDiscordWebhook(),
	}
	sched.Runner = a.RunScheduled
	go sched.Start(ctx)
	return a, nil
}

// Emit publica un evento si hay sink configurado. No-op si a.events es nil.
// Concurrent-safe siempre que la implementación de events.Sink lo sea.
func (a *Agent) Emit(typ, jobID string, payload map[string]any) {
	if a.events == nil {
		return
	}
	a.events.Publish(events.Event{
		Type:    typ,
		Time:    time.Now(),
		JobID:   jobID,
		Payload: payload,
	})
}

// SetEvents asigna el sink de eventos. Pasar nil deshabilita la emisión.
func (a *Agent) SetEvents(s events.Sink) {
	a.events = s
}

// History devuelve una copia defensiva del historial principal. Pensada para
// lectores observacionales (UI/log) que no pueden coordinar con SendMain.
// Los flujos que modifican el historial usan SendMain, no manipulan el slice.
func (a *Agent) History() []llm.Message {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	out := make([]llm.Message, len(a.history))
	copy(out, a.history)
	return out
}

// SetHistory reemplaza el historial completo (lo usa /sessions load).
func (a *Agent) SetHistory(h []llm.Message) {
	a.historyMu.Lock()
	a.history = h
	a.historyMu.Unlock()
}

// HistoryLen devuelve la cantidad de mensajes sin copiar el slice. Útil para
// /api/state y banners de status.
func (a *Agent) HistoryLen() int {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	return len(a.history)
}

// SendMain serializa un Send sobre el historial principal del agente con
// lock interno. Es la API que deben usar transports concurrentes (web).
// Terminal puede seguir usando SendAndDispatch directamente porque su loop
// es single-threaded.
func (a *Agent) SendMain(ctx context.Context, sessionName, input string) (text, artifact string, err error) {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	return a.SendAndDispatch(ctx, &a.history, sessionName, input)
}

// HistoryPtr devuelve un puntero al slice de historial para callers
// single-threaded (terminal). Web y otros transports concurrentes deben usar
// SendMain en su lugar.
func (a *Agent) HistoryPtr() *[]llm.Message {
	return &a.history
}

// Reset limpia el historial y guarda la sesión vacía a disco.
func (a *Agent) Reset() error {
	a.historyMu.Lock()
	a.history = nil
	a.historyMu.Unlock()
	if a.sessions == nil {
		return nil
	}
	return a.sessions.Save(a.sessions.Current(), nil)
}

func (a *Agent) Sessions() *sessions.Manager { return a.sessions }
func (a *Agent) Skills() *skills.Registry    { return a.skills }
func (a *Agent) SetSkills(r *skills.Registry) {
	a.skills = r
}
func (a *Agent) MCP() *mcp.Manager                { return a.mcp }
func (a *Agent) Scheduler() *scheduler.Scheduler  { return a.scheduler }
func (a *Agent) Personality() string              { return a.personality }
func (a *Agent) Model() string                    { return a.model }
