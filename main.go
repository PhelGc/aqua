package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var errMaxToolIterations = errors.New("max tool iterations exceeded")

const (
	defaultEndpoint        = "https://opencode.ai/zen/go/v1/chat/completions"
	defaultModel           = "deepseek-v4-flash"
	defaultPersonalityPath = "personality.md"
	defaultMaxToolIters    = 16
)

type message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiToolFunc `json:"function"`
}

type openaiToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model    string       `json:"model"`
	Messages []message    `json:"messages"`
	Tools    []openaiTool `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type agent struct {
	endpoint    string
	model       string
	apiKey      string
	personality string
	history     []message
	http        *http.Client
	mcp         *mcpManager
	skills      *skillRegistry
	sessions    *sessionManager
	// label identifica al agente en los logs de tool-call. Vacío = agente
	// principal (sale como [tool]); con valor sale como [tool/<label>].
	label string
	// events recibe eventos del runtime (tool-calls, orquestador, etc.).
	// nil = sin emisión (chat/discord modes funcionan igual). Workers
	// heredan el sink del padre en runIsolated.
	events EventSink
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

func newAgent(ctx context.Context) (*agent, error) {
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
	cfg, err := loadMCPConfig()
	if err != nil {
		return nil, fmt.Errorf("leyendo config MCP: %w", err)
	}
	manager, err := newMCPManager(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("iniciando MCP: %w", err)
	}
	skills, err := loadSkills()
	if err != nil {
		return nil, fmt.Errorf("cargando skills: %w", err)
	}
	sessions, err := newSessionManager()
	if err != nil {
		return nil, fmt.Errorf("iniciando sesiones: %w", err)
	}
	history, err := sessions.load(sessions.current())
	if err != nil {
		return nil, fmt.Errorf("cargando sesión %q: %w", sessions.current(), err)
	}
	return &agent{
		endpoint:    endpoint,
		model:       model,
		apiKey:      key,
		personality: personality,
		history:     history,
		http:        &http.Client{Timeout: 120 * time.Second},
		mcp:         manager,
		skills:      skills,
		sessions:    sessions,
	}, nil
}

func (a *agent) callAPI(ctx context.Context, msgs []message) (*message, error) {
	body, err := json.Marshal(chatRequest{
		Model:    a.model,
		Messages: msgs,
		Tools:    a.mcp.tools(),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("respuesta inválida (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		msg := resp.Status
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, msg)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("respuesta sin choices")
	}
	return &parsed.Choices[0].Message, nil
}

func (a *agent) send(ctx context.Context, history *[]message, sessionName, userInput string) (string, error) {
	checkpoint := len(*history)
	*history = append(*history, message{Role: "user", Content: userInput})

	reply, err := a.runConversation(ctx, history)
	if err != nil {
		if !errors.Is(err, errMaxToolIterations) {
			*history = (*history)[:checkpoint]
		}
		return "", err
	}
	if a.sessions != nil && sessionName != "" {
		if saveErr := a.sessions.save(sessionName, *history); saveErr != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión:", saveErr)
		}
	}
	return reply, nil
}

// sendAndDispatch wraps send() para detectar un marker <orchestrate> en la
// respuesta del LLM. Si lo encuentra, despacha al adaptador correspondiente y
// devuelve (texto-consolidado, path-del-artifact). Si no, devuelve (reply, "").
// Cuando dispatcha, el último mensaje del assistant en *history se reemplaza
// por el texto consolidado para que el LLM no vuelva a ver su propio marker.
func (a *agent) sendAndDispatch(ctx context.Context, history *[]message, sessionName, userInput string) (text, artifact string, err error) {
	reply, err := a.send(ctx, history, sessionName, userInput)
	if err != nil {
		return "", "", err
	}
	m, prose, ok := parseOrchestrate(reply)
	if !ok {
		return reply, "", nil
	}
	path, summary, derr := a.dispatchOrchestrate(ctx, m)
	if derr != nil {
		fail := strings.TrimSpace(prose) + "\n\n(error orquestando: " + derr.Error() + ")"
		return fail, "", derr
	}
	consolidated := strings.TrimSpace(prose)
	if consolidated == "" {
		consolidated = summary
	} else {
		consolidated = consolidated + "\n\n" + summary
	}
	for i := len(*history) - 1; i >= 0; i-- {
		if (*history)[i].Role == "assistant" {
			(*history)[i].Content = consolidated
			(*history)[i].ToolCalls = nil
			break
		}
	}
	if a.sessions != nil && sessionName != "" {
		if saveErr := a.sessions.save(sessionName, *history); saveErr != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión:", saveErr)
		}
	}
	return consolidated, path, nil
}

// dispatchOrchestrate despacha el marker al adaptador correspondiente según kind.
// Cada adaptador devuelve (path-del-artifact, summary-texto, err).
func (a *agent) dispatchOrchestrate(ctx context.Context, m orchestrateMarker) (artifact, summary string, err error) {
	switch m.Kind {
	case "report":
		return a.runReport(ctx, m.Payload)
	default:
		return "", "", fmt.Errorf("kind de orchestrate desconocido: %q", m.Kind)
	}
}

// runConversation ejecuta el loop de chat hasta que el modelo deja de pedir tools
// o se alcanza maxToolIters(). Modifica *history (append-only) pero no
// persiste sesión ni hace rollback. Reusable por workers del orquestador.
func (a *agent) runConversation(ctx context.Context, history *[]message) (string, error) {
	limit := maxToolIters()
	for i := 0; i < limit; i++ {
		msgs := *history
		if a.personality != "" {
			msgs = append([]message{{Role: "system", Content: a.personality}}, *history...)
		}

		reply, err := a.callAPI(ctx, msgs)
		if err != nil {
			return "", err
		}

		*history = append(*history, *reply)

		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}

		for _, tc := range reply.ToolCalls {
			if a.label != "" {
				fmt.Printf("[tool/%s] %s\n", a.label, tc.Function.Name)
			} else {
				fmt.Printf("[tool] %s\n", tc.Function.Name)
			}
			a.emit("tool_call", a.label, map[string]any{"tool": tc.Function.Name})
			result, callErr := a.mcp.callTool(ctx, tc.Function.Name, tc.Function.Arguments)
			content := result
			if callErr != nil {
				if content == "" {
					content = callErr.Error()
				} else {
					content = content + "\n(error: " + callErr.Error() + ")"
				}
			}
			*history = append(*history, message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
	}

	return "", fmt.Errorf("se alcanzó el máximo de iteraciones de tools (%d): %w", limit, errMaxToolIterations)
}

// maxToolIters lee el límite de iteraciones de tool-call por turno.
// Default: defaultMaxToolIters. Override: env var OPENCODE_MAX_TOOL_ITERS.
func maxToolIters() int {
	if v := os.Getenv("OPENCODE_MAX_TOOL_ITERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxToolIters
}

func main() {
	mode := flag.String("mode", "terminal", "interfaz: terminal | discord")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newAgent(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer a.mcp.Close()

	switch *mode {
	case "terminal", "console":
		runTerminal(ctx, a)
	case "discord":
		if err := runDiscord(ctx, a); err != nil {
			fmt.Fprintln(os.Stderr, "discord:", err)
			os.Exit(1)
		}
	case "ui", "web":
		if err := runWeb(ctx, a); err != nil {
			fmt.Fprintln(os.Stderr, "ui:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "modo desconocido: %q (usar: terminal | discord | ui)\n", *mode)
		os.Exit(1)
	}
}

func runTerminal(ctx context.Context, a *agent) {
	personalityStatus := "sin personalidad"
	if a.personality != "" {
		personalityStatus = fmt.Sprintf("personalidad: %d chars", len(a.personality))
	}
	toolStatus := "sin tools"
	if n := len(a.mcp.tools()); n > 0 {
		toolStatus = fmt.Sprintf("%d tools de %d servidores MCP", n, len(a.mcp.sessions))
	}
	skillStatus := "sin skills"
	if n := len(a.skills.list()); n > 0 {
		skillStatus = fmt.Sprintf("%d skills", n)
	}
	fmt.Printf("aqua · modelo: %s · %s · %s · %s · sesión: %s (%d mensajes)\n",
		a.model, personalityStatus, toolStatus, skillStatus, a.sessions.current(), len(a.history))
	fmt.Println("comandos: /exit, /reset, /tools, /skills [reload], /sessions, /<skill> [args]")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			cmd, args, _ := strings.Cut(input[1:], " ")
			args = strings.TrimSpace(args)
			switch cmd {
			case "exit":
				return
			case "reset":
				a.history = nil
				if err := a.sessions.save(a.sessions.current(), a.history); err != nil {
					fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión:", err)
				}
				fmt.Println("(historial limpio)")
				continue
			case "sessions":
				handleSessions(a, args)
				continue
			case "tools":
				tools := a.mcp.tools()
				if len(tools) == 0 {
					fmt.Println("(sin tools cargadas)")
				} else {
					for _, t := range tools {
						fmt.Printf("- %s: %s\n", t.Function.Name, t.Function.Description)
					}
				}
				continue
			case "skills":
				if args == "reload" {
					reloaded, err := loadSkills()
					if err != nil {
						fmt.Fprintln(os.Stderr, "error recargando skills:", err)
						continue
					}
					a.skills = reloaded
					fmt.Printf("(recargadas %d skills)\n", len(reloaded.list()))
					continue
				}
				skills := a.skills.list()
				if len(skills) == 0 {
					fmt.Println("(sin skills cargadas)")
				} else {
					for _, s := range skills {
						desc := s.description
						if desc == "" {
							desc = "(sin descripción)"
						}
						fmt.Printf("- /%s: %s\n", s.name, desc)
					}
				}
				continue
			default:
				rendered, ok := a.skills.render(cmd, args)
				if !ok {
					fmt.Fprintf(os.Stderr, "comando desconocido: /%s\n", cmd)
					continue
				}
				input = rendered
			}
		}

		reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Minute)
		reply, artifact, err := a.sendAndDispatch(reqCtx, &a.history, a.sessions.current(), input)
		reqCancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Println(reply)
		if artifact != "" {
			fmt.Printf("(archivo: %s)\n", artifact)
		}
		fmt.Println()
	}
}

func handleSessions(a *agent, args string) {
	sub, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch sub {
	case "":
		names, err := a.sessions.list()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error listando sesiones:", err)
			return
		}
		if len(names) == 0 {
			fmt.Printf("(sin sesiones guardadas; actual: %s)\n", a.sessions.current())
			return
		}
		for _, n := range names {
			marker := "  "
			if n == a.sessions.current() {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, n)
		}
	case "new":
		if rest == "" {
			fmt.Fprintln(os.Stderr, "uso: /sessions new <nombre>")
			return
		}
		if err := a.sessions.save(a.sessions.current(), a.history); err != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión actual:", err)
		}
		if err := a.sessions.switchTo(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		a.history = nil
		if err := a.sessions.save(rest, a.history); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		fmt.Printf("(sesión actual: %s)\n", rest)
	case "load":
		if rest == "" {
			fmt.Fprintln(os.Stderr, "uso: /sessions load <nombre>")
			return
		}
		if err := a.sessions.save(a.sessions.current(), a.history); err != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión actual:", err)
		}
		history, err := a.sessions.load(rest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		if err := a.sessions.switchTo(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		a.history = history
		fmt.Printf("(sesión actual: %s · %d mensajes)\n", rest, len(history))
	case "delete":
		if rest == "" {
			fmt.Fprintln(os.Stderr, "uso: /sessions delete <nombre>")
			return
		}
		if err := a.sessions.delete(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		fmt.Printf("(borrada: %s)\n", rest)
	default:
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %s\nuso: /sessions [new|load|delete] <nombre>\n", sub)
	}
}
