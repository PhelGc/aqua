// Package notifier publica notificaciones a un canal externo (Discord, hoy via webhook).
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Opts permite ajustar el envío.
type Opts struct {
	// Attachment es un path absoluto/relativo a un archivo local a adjuntar
	// junto al mensaje. Si está vacío, solo se manda el texto.
	Attachment string
	// Username sobrescribe el nombre que muestra el webhook (default: el del
	// canal). Útil para diferenciar "aqua · scheduler" de otras fuentes.
	Username string
}

// Notifier publica notificaciones a un canal externo (Discord, hoy via webhook).
// Implementaciones deben ser concurrent-safe.
type Notifier interface {
	Notify(ctx context.Context, message string, opts Opts) error
}

// discordWebhook envía mensajes a un webhook de Discord vía HTTP POST.
// Soporta texto y attachment de archivo (multipart cuando hay file, JSON
// cuando es solo texto).
type discordWebhook struct {
	url    string
	client *http.Client
}

// NewDiscordWebhook devuelve un notifier si DISCORD_NOTIFY_WEBHOOK
// está seteada; si no, devuelve nil (sin error: la feature es opcional).
func NewDiscordWebhook() Notifier {
	url := os.Getenv("DISCORD_NOTIFY_WEBHOOK")
	if url == "" {
		return nil
	}
	return &discordWebhook{
		url:    url,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (n *discordWebhook) Notify(ctx context.Context, message string, opts Opts) error {
	chunks := splitForWebhook(message)
	for i, chunk := range chunks {
		// El attachment va solo en el último chunk para mantener el flujo visual.
		att := ""
		if i == len(chunks)-1 {
			att = opts.Attachment
		}
		var err error
		if att != "" {
			err = n.sendWithFile(ctx, chunk, Opts{Username: opts.Username, Attachment: att})
		} else {
			err = n.sendText(ctx, chunk, Opts{Username: opts.Username})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (n *discordWebhook) sendText(ctx context.Context, message string, opts Opts) error {
	payload := map[string]any{"content": message}
	if opts.Username != "" {
		payload["username"] = opts.Username
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return n.do(req)
}

func (n *discordWebhook) sendWithFile(ctx context.Context, message string, opts Opts) error {
	f, err := os.Open(opts.Attachment)
	if err != nil {
		return fmt.Errorf("abriendo attachment %s: %w", opts.Attachment, err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// El "payload_json" lleva los campos del mensaje en JSON.
	payload := map[string]any{"content": message}
	if opts.Username != "" {
		payload["username"] = opts.Username
	}
	pjson, _ := json.Marshal(payload)
	if err := mw.WriteField("payload_json", string(pjson)); err != nil {
		return err
	}
	// El archivo va como files[0].
	fw, err := mw.CreateFormFile("files[0]", filepath.Base(opts.Attachment))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return n.do(req)
}

func (n *discordWebhook) do(req *http.Request) error {
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("webhook respondió %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// splitForWebhook parte el texto en chunks <= webhookMaxContent respetando
// saltos de línea / espacios cuando es posible. Mismo enfoque que el bot
// Discord para DMs largos — preferimos varios mensajes que truncar.
const webhookMaxContent = 1900

func splitForWebhook(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"(sin contenido)"}
	}
	var out []string
	for len(text) > webhookMaxContent {
		cut := strings.LastIndex(text[:webhookMaxContent], "\n")
		if cut <= webhookMaxContent/4 {
			cut = strings.LastIndex(text[:webhookMaxContent], " ")
		}
		if cut <= 0 {
			cut = webhookMaxContent
		}
		out = append(out, strings.TrimRight(text[:cut], " \n"))
		text = strings.TrimLeft(text[cut:], " \n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}
