package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPConfigPath = "mcp.json"
	toolNameSep          = "__"
)

type mcpServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type mcpConfig struct {
	Servers map[string]mcpServerSpec `json:"mcpServers"`
}

type mcpManager struct {
	sessions   map[string]*mcp.ClientSession
	toolServer map[string]string
	toolDefs   []openaiTool
}

func loadMCPConfig() (*mcpConfig, error) {
	path := os.Getenv("OPENCODE_MCP_CONFIG")
	if path == "" {
		path = defaultMCPConfigPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &mcpConfig{}, nil
		}
		return nil, err
	}
	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parseando %s: %w", path, err)
	}
	return &cfg, nil
}

func newMCPManager(ctx context.Context, cfg *mcpConfig) (*mcpManager, error) {
	m := &mcpManager{
		sessions:   make(map[string]*mcp.ClientSession),
		toolServer: make(map[string]string),
	}
	if cfg == nil || len(cfg.Servers) == 0 {
		return m, nil
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "aqua", Version: "0.1.0"}, nil)

	for name, spec := range cfg.Servers {
		cmd := exec.Command(spec.Command, spec.Args...)
		if len(spec.Env) > 0 {
			env := os.Environ()
			for k, v := range spec.Env {
				env = append(env, k+"="+v)
			}
			cmd.Env = env
		}
		transport := &mcp.CommandTransport{Command: cmd}
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			m.Close()
			return nil, fmt.Errorf("conectando al servidor MCP %q: %w", name, err)
		}
		m.sessions[name] = session

		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			m.Close()
			return nil, fmt.Errorf("listando tools del servidor %q: %w", name, err)
		}
		for _, t := range listed.Tools {
			full := name + toolNameSep + t.Name
			m.toolServer[full] = name
			var params map[string]any
			if t.InputSchema != nil {
				raw, _ := json.Marshal(t.InputSchema)
				_ = json.Unmarshal(raw, &params)
			}
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			m.toolDefs = append(m.toolDefs, openaiTool{
				Type: "function",
				Function: openaiToolFunc{
					Name:        full,
					Description: t.Description,
					Parameters:  params,
				},
			})
		}
	}
	return m, nil
}

func (m *mcpManager) tools() []openaiTool {
	return m.toolDefs
}

func (m *mcpManager) callTool(ctx context.Context, fullName, argsJSON string) (string, error) {
	server, ok := m.toolServer[fullName]
	if !ok {
		return "", fmt.Errorf("tool desconocida: %s", fullName)
	}
	session := m.sessions[server]
	idx := strings.Index(fullName, toolNameSep)
	bareName := fullName[idx+len(toolNameSep):]

	var args map[string]any
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("args inválidos para %s: %w", fullName, err)
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      bareName,
		Arguments: args,
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tc.Text)
		}
	}
	if result.IsError {
		return sb.String(), fmt.Errorf("tool reportó error")
	}
	return sb.String(), nil
}

func (m *mcpManager) Close() {
	for _, s := range m.sessions {
		_ = s.Close()
	}
}
