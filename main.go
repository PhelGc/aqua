package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultEndpoint        = "https://opencode.ai/zen/go/v1/chat/completions"
	defaultModel           = "deepseek-v4-flash"
	defaultPersonalityPath = "personality.md"
	maxToolIterations      = 8
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
	return &agent{
		endpoint:    endpoint,
		model:       model,
		apiKey:      key,
		personality: personality,
		http:        &http.Client{Timeout: 120 * time.Second},
		mcp:         manager,
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

func (a *agent) send(ctx context.Context, userInput string) (string, error) {
	checkpoint := len(a.history)
	a.history = append(a.history, message{Role: "user", Content: userInput})

	for i := 0; i < maxToolIterations; i++ {
		msgs := a.history
		if a.personality != "" {
			msgs = append([]message{{Role: "system", Content: a.personality}}, a.history...)
		}

		reply, err := a.callAPI(ctx, msgs)
		if err != nil {
			a.history = a.history[:checkpoint]
			return "", err
		}

		a.history = append(a.history, *reply)

		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}

		for _, tc := range reply.ToolCalls {
			fmt.Printf("[tool] %s\n", tc.Function.Name)
			result, callErr := a.mcp.callTool(ctx, tc.Function.Name, tc.Function.Arguments)
			content := result
			if callErr != nil {
				if content == "" {
					content = callErr.Error()
				} else {
					content = content + "\n(error: " + callErr.Error() + ")"
				}
			}
			a.history = append(a.history, message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
	}

	return "", fmt.Errorf("se alcanzó el máximo de iteraciones de tools (%d)", maxToolIterations)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newAgent(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer a.mcp.Close()

	personalityStatus := "sin personalidad"
	if a.personality != "" {
		personalityStatus = fmt.Sprintf("personalidad: %d chars", len(a.personality))
	}
	toolStatus := "sin tools"
	if n := len(a.mcp.tools()); n > 0 {
		toolStatus = fmt.Sprintf("%d tools de %d servidores MCP", n, len(a.mcp.sessions))
	}
	fmt.Printf("aqua · modelo: %s · %s · %s\n", a.model, personalityStatus, toolStatus)
	fmt.Println("comandos: /exit, /reset, /tools")
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
		switch input {
		case "/exit":
			return
		case "/reset":
			a.history = nil
			fmt.Println("(historial limpio)")
			continue
		case "/tools":
			tools := a.mcp.tools()
			if len(tools) == 0 {
				fmt.Println("(sin tools cargadas)")
			} else {
				for _, t := range tools {
					fmt.Printf("- %s: %s\n", t.Function.Name, t.Function.Description)
				}
			}
			continue
		}

		reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Minute)
		reply, err := a.send(reqCtx, input)
		reqCancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Println(reply)
		fmt.Println()
	}
}
