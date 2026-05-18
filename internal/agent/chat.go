package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"aqua/internal/llm"
	"aqua/internal/marker"
	"aqua/internal/memory"
)

// Send envía un mensaje del usuario y devuelve la respuesta del modelo.
// Hace rollback de history en caso de error (salvo si se agotaron iteraciones
// de tools, donde dejamos el progreso parcial). Equivalente a SendStreaming
// con onDelta=nil; mantenida para callers que no necesitan stream.
func (a *Agent) Send(ctx context.Context, history *[]llm.Message, sessionName, userInput string) (string, error) {
	return a.SendStreaming(ctx, history, sessionName, userInput, nil)
}

// SendStreaming es como Send pero invoca onDelta por cada fragmento que
// llega del LLM. Si onDelta es nil, no pide stream al endpoint.
func (a *Agent) SendStreaming(ctx context.Context, history *[]llm.Message, sessionName, userInput string, onDelta DeltaCallback) (string, error) {
	checkpoint := len(*history)
	*history = append(*history, llm.Message{Role: "user", Content: userInput})

	reply, err := a.RunConversationStreaming(ctx, history, onDelta)
	if err != nil {
		if !errors.Is(err, errMaxToolIterations) {
			*history = (*history)[:checkpoint]
		}
		return "", err
	}
	if a.sessions != nil && sessionName != "" {
		if saveErr := a.sessions.Save(sessionName, *history); saveErr != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión:", saveErr)
		}
	}
	return reply, nil
}

// SendAndDispatch envuelve Send para detectar un marker <orchestrate> en la
// respuesta del LLM. Si lo encuentra, despacha al adaptador correspondiente y
// devuelve (texto-consolidado, path-del-artifact). Si no, devuelve (reply, "").
// Cuando dispatcha, el último mensaje del assistant en *history se reemplaza
// por el texto consolidado para que el LLM no vuelva a ver su propio marker.
func (a *Agent) SendAndDispatch(ctx context.Context, history *[]llm.Message, sessionName, userInput string) (text, artifact string, err error) {
	return a.SendAndDispatchStreaming(ctx, history, sessionName, userInput, nil)
}

// SendAndDispatchStreaming es como SendAndDispatch pero invoca onDelta por
// cada fragmento del LLM (útil para TUI con feedback en vivo).
func (a *Agent) SendAndDispatchStreaming(ctx context.Context, history *[]llm.Message, sessionName, userInput string, onDelta DeltaCallback) (text, artifact string, err error) {
	reply, err := a.SendStreaming(ctx, history, sessionName, userInput, onDelta)
	if err != nil {
		return "", "", err
	}
	m, prose, ok := marker.Parse(reply)
	if !ok {
		return reply, "", nil
	}
	path, summary, derr := a.DispatchOrchestrate(ctx, m)
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
		if saveErr := a.sessions.Save(sessionName, *history); saveErr != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión:", saveErr)
		}
	}
	return consolidated, path, nil
}

// RunConversation ejecuta el loop de chat hasta que el modelo deja de pedir tools
// o se alcanza maxToolIters(). Equivalente a RunConversationStreaming con
// onDelta=nil; mantenida para workers del orquestador.
func (a *Agent) RunConversation(ctx context.Context, history *[]llm.Message) (string, error) {
	return a.RunConversationStreaming(ctx, history, nil)
}

// RunConversationStreaming es como RunConversation pero invoca onDelta por
// cada fragmento del LLM. Si onDelta es nil usa callAPI plano (sin stream).
//
// La memoria persistente (memory.md) se lee una vez por llamada para que cambios
// hechos por aqua en turnos anteriores queden visibles.
func (a *Agent) RunConversationStreaming(ctx context.Context, history *[]llm.Message, onDelta DeltaCallback) (string, error) {
	limit := maxToolIters()
	mem, _ := memory.Load()
	for i := 0; i < limit; i++ {
		msgs := *history
		var systems []llm.Message
		if a.personality != "" {
			systems = append(systems, llm.Message{Role: "system", Content: a.personality})
		}
		if mem != "" {
			systems = append(systems, llm.Message{Role: "system", Content: "## Tu memoria persistente (memory.md)\n\n" + mem})
		}
		if len(systems) > 0 {
			msgs = append(systems, *history...)
		}

		var (
			reply *llm.Message
			err   error
		)
		if onDelta != nil {
			reply, err = a.callAPIStream(ctx, msgs, onDelta)
		} else {
			reply, err = a.callAPI(ctx, msgs)
		}
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
			a.Emit("tool_call", a.label, map[string]any{"tool": tc.Function.Name})
			result, callErr := a.mcp.CallTool(ctx, tc.Function.Name, tc.Function.Arguments)
			content := result
			if callErr != nil {
				if content == "" {
					content = callErr.Error()
				} else {
					content = content + "\n(error: " + callErr.Error() + ")"
				}
			}
			*history = append(*history, llm.Message{
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
