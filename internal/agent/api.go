package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"aqua/internal/llm"
)

// DeltaCallback recibe cada delta del stream. nil = no streaming.
type DeltaCallback func(llm.StreamDelta)

// callAPI envía un request no-streaming clásico. Mantiene la firma simple
// para callers que no necesitan progreso parcial (web, discord, workers).
func (a *Agent) callAPI(ctx context.Context, msgs []llm.Message) (*llm.Message, error) {
	body, err := json.Marshal(llm.ChatRequest{
		Model:    a.model,
		Messages: msgs,
		Tools:    a.mcp.Tools(),
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

	return decodeChatResponse(resp)
}

// callAPIStream pide stream=true al endpoint y va llamando onDelta por cada
// chunk recibido. Si el servidor NO devuelve text/event-stream (no soporta
// streaming), parsea como respuesta normal y devuelve el mensaje completo
// sin invocar onDelta. Esto da fallback transparente.
func (a *Agent) callAPIStream(ctx context.Context, msgs []llm.Message, onDelta DeltaCallback) (*llm.Message, error) {
	body, err := json.Marshal(llm.ChatRequest{
		Model:    a.model,
		Messages: msgs,
		Tools:    a.mcp.Tools(),
		Stream:   true,
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
	req.Header.Set("Accept", "text/event-stream")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Fallback: si el endpoint no banca SSE, devuelve JSON normal.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		return decodeChatResponse(resp)
	}

	return parseStream(resp.Body, onDelta)
}

// decodeChatResponse parsea un cuerpo no-streaming. Compartido entre callAPI
// y el fallback de callAPIStream.
func decodeChatResponse(resp *http.Response) (*llm.Message, error) {
	var parsed llm.ChatResponse
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

// parseStream consume eventos SSE línea por línea, invoca onDelta por cada
// chunk válido, y va acumulando el mensaje final que devuelve al cerrar.
//
// Formato SSE OpenAI: cada línea "data: {...}", separadas por blank lines.
// La marca de fin es "data: [DONE]".
func parseStream(body io.Reader, onDelta DeltaCallback) (*llm.Message, error) {
	scanner := bufio.NewScanner(body)
	// Buffer grande para chunks de respuesta larga (el default 64KB se queda
	// corto con tool_calls.arguments JSON acumulado).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	final := llm.Message{Role: "assistant"}
	// Tool-calls indexados por StreamToolCallDelta.Index porque vienen
	// fragmentados (uno por chunk con incrementos en arguments).
	toolCalls := map[int]*llm.ToolCall{}
	var toolCallOrder []int

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk llm.StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// chunk malo: lo loggeamos saltando (no abortamos el stream).
			continue
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("stream API error: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		// Acumulamos en el mensaje final.
		final.Content += delta.Content
		final.ReasoningContent += delta.ReasoningContent
		for _, tcd := range delta.ToolCalls {
			tc, ok := toolCalls[tcd.Index]
			if !ok {
				tc = &llm.ToolCall{Type: "function"}
				toolCalls[tcd.Index] = tc
				toolCallOrder = append(toolCallOrder, tcd.Index)
			}
			if tcd.ID != "" {
				tc.ID = tcd.ID
			}
			if tcd.Type != "" {
				tc.Type = tcd.Type
			}
			if tcd.Function.Name != "" {
				tc.Function.Name = tcd.Function.Name
			}
			if tcd.Function.Arguments != "" {
				tc.Function.Arguments += tcd.Function.Arguments
			}
		}

		// Notificamos al caller (puede ser nil si solo queremos el final).
		if onDelta != nil {
			onDelta(delta)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("leyendo stream: %w", err)
	}

	// Ensamblamos los tool_calls finales en orden.
	for _, idx := range toolCallOrder {
		final.ToolCalls = append(final.ToolCalls, *toolCalls[idx])
	}

	return &final, nil
}
