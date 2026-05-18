// aqua-http-mcp es un MCP server stdio que expone una tool `http_request`
// para hacer requests HTTP arbitrarios. Pensado para que aqua pueda llamar
// webhooks (n8n) y APIs REST sin depender de un MCP de terceros buggeado.
//
// Compila a un binario chico y aqua lo invoca via stdio igual que cualquier
// otro server MCP. Sin keychain, sin Azure auth, sin deps nativas.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultTimeout = 60 * time.Second
	// Tope de tamaño de response que reportamos al LLM (16 KB). Más arriba
	// inflamos el prompt sin aportar.
	maxResponseBody = 16 * 1024
)

// httpRequestArgs es el schema de la tool. Los tags `jsonschema` los lee el
// SDK para generar la spec que ve el LLM.
type httpRequestArgs struct {
	URL     string            `json:"url" jsonschema:"URL completa del endpoint (https://...)"`
	Method  string            `json:"method,omitempty" jsonschema:"método HTTP: GET (default), POST, PUT, DELETE, PATCH"`
	Headers map[string]string `json:"headers,omitempty" jsonschema:"headers extra (ej. {\"Authorization\": \"Bearer xxx\"})"`
	Body    string            `json:"body,omitempty" jsonschema:"cuerpo del request, generalmente JSON serializado como string"`
	// TimeoutSeconds permite overridear el default de 60s. Útil para webhooks
	// que tardan; el LLM lo subirá explícitamente si lo necesita.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"timeout en segundos (default 60)"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "aqua-http",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "http_request",
		Description: "Hace un request HTTP arbitrario (GET/POST/PUT/DELETE/PATCH) " +
			"y devuelve status + headers + body. Pensado para webhooks y APIs REST. " +
			"El body se envía tal cual; si es JSON el LLM debe serializarlo antes y " +
			"setear Content-Type: application/json en los headers.",
	}, doRequest)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("server failed: %v", err)
	}
}

func doRequest(ctx context.Context, _ *mcp.CallToolRequest, args httpRequestArgs) (*mcp.CallToolResult, any, error) {
	if args.URL == "" {
		return textResult("error: url es obligatoria"), nil, nil
	}
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = http.MethodGet
	}
	timeout := defaultTimeout
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if args.Body != "" {
		body = bytes.NewReader([]byte(args.Body))
	}
	req, err := http.NewRequestWithContext(reqCtx, method, args.URL, body)
	if err != nil {
		return textResult(fmt.Sprintf("error armando request: %v", err)), nil, nil
	}
	for k, v := range args.Headers {
		req.Header.Set(k, v)
	}
	// Default razonable si el caller no lo seteó y mandó body que parece JSON.
	if body != nil && req.Header.Get("Content-Type") == "" && looksLikeJSON(args.Body) {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return textResult(fmt.Sprintf("error en request: %v", err)), nil, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return textResult(fmt.Sprintf("error leyendo response: %v", err)), nil, nil
	}
	truncated := false
	if len(respBody) > maxResponseBody {
		respBody = respBody[:maxResponseBody]
		truncated = true
	}

	return textResult(formatResponse(resp, respBody, truncated)), nil, nil
}

// formatResponse arma la representación human-readable que ve el LLM.
// Mantenemos status + headers relevantes + body en bloque. Sin keys ruidosas
// como Date/Server.
func formatResponse(resp *http.Response, body []byte, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d %s\n", resp.StatusCode, resp.Status)
	// Solo headers que aportan: content-type, location, retry-after, etag, etc.
	for _, h := range []string{"Content-Type", "Content-Length", "Location", "Retry-After", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", h, v)
		}
	}
	b.WriteString("\n")
	b.Write(body)
	if truncated {
		fmt.Fprintf(&b, "\n\n[response truncada a %d bytes]", maxResponseBody)
	}
	return b.String()
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	first := s[0]
	return first == '{' || first == '['
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// unused but kept for future use if we add structured output schemas.
var _ = json.Marshal
