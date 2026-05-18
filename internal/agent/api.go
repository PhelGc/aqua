package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"aqua/internal/llm"
)

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
