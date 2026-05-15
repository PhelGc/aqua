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
	defaultEndpoint = "https://opencode.ai/zen/go/v1/chat/completions"
	defaultModel    = "deepseek-v4-flash"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type agent struct {
	endpoint string
	model    string
	apiKey   string
	history  []message
	http     *http.Client
}

func newAgent() (*agent, error) {
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
	return &agent{
		endpoint: endpoint,
		model:    model,
		apiKey:   key,
		http:     &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (a *agent) send(ctx context.Context, userInput string) (string, error) {
	a.history = append(a.history, message{Role: "user", Content: userInput})

	body, err := json.Marshal(chatRequest{Model: a.model, Messages: a.history})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.http.Do(req)
	if err != nil {
		a.history = a.history[:len(a.history)-1]
		return "", err
	}
	defer resp.Body.Close()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		a.history = a.history[:len(a.history)-1]
		return "", fmt.Errorf("respuesta inválida (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		a.history = a.history[:len(a.history)-1]
		msg := resp.Status
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, msg)
	}
	if len(parsed.Choices) == 0 {
		a.history = a.history[:len(a.history)-1]
		return "", fmt.Errorf("respuesta sin choices")
	}

	reply := parsed.Choices[0].Message.Content
	a.history = append(a.history, message{Role: "assistant", Content: reply})
	return reply, nil
}

func main() {
	a, err := newAgent()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Printf("aqua · modelo: %s\n", a.model)
	fmt.Println("comandos: /exit, /reset")
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
		}

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		reply, err := a.send(ctx, input)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Println(reply)
		fmt.Println()
	}
}
