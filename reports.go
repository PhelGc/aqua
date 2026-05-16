package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ReportRequest es el payload que el LLM emite dentro del marker
// <orchestrate kind="report">.
//
// El LLM tiene que:
//   1. traducir el filtro NL del usuario a JQL,
//   2. llamar el tool de Jira para listar los tickets,
//   3. emitir el marker con los tickets ya pre-fetched.
//
// Cada ticket carga lo necesario para que el worker lo evalúe sin volver a
// llamar a la API de Jira (a menos que necesite algo puntual).
type ReportRequest struct {
	Descripcion string         `json:"descripcion"`
	Tickets     []ReportTicket `json:"tickets"`
}

// ReportTicket es lo que el LLM le pasa al worker para evaluar.
// Es un map para no encorsetarlo: el LLM decide qué campos meter según el caso.
type ReportTicket struct {
	Key    string                 `json:"key"`
	Fields map[string]any         `json:"fields,omitempty"`
}

// ReportJob implementa Job para el orquestador.
type ReportJob struct {
	Ticket ReportTicket
}

func (j ReportJob) ID() string { return j.Ticket.Key }

func (j ReportJob) System() []string {
	return []string{
		"Sos una evaluadora de tickets de Jira. Vas a recibir UN ticket. " +
			"Aplicá los criterios de evaluación que ya conocés (DoD, claridad, valor, vínculos, " +
			"estimación, título correcto). Devolvé markdown puro y breve con:\n" +
			"- **Veredicto**: aprobado | con observaciones | rechazado\n" +
			"- **Motivos**: bullets cortos.\n" +
			"- **Observaciones**: bullets cortos si aplica.\n" +
			"Nada de markers, nada de JSON, solo texto markdown. Sin saludos ni floritura.",
	}
}

func (j ReportJob) Prompt() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Ticket: %s\n", j.Ticket.Key)
	if len(j.Ticket.Fields) > 0 {
		sb.WriteString("\nCampos:\n")
		// Imprimimos los fields como JSON indentado: el LLM los lee bien y
		// no perdemos estructura para campos anidados.
		if data, err := json.MarshalIndent(j.Ticket.Fields, "", "  "); err == nil {
			sb.Write(data)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// runReport ejecuta el reporte: parsea payload, fan-out a workers, escribe .md.
// Devuelve path del archivo y un summary corto para mostrarle al usuario.
func (a *agent) runReport(ctx context.Context, payload string) (artifact, summary string, err error) {
	var req ReportRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", "", fmt.Errorf("payload inválido: %w", err)
	}
	if len(req.Tickets) == 0 {
		return "", "", fmt.Errorf("payload sin tickets")
	}

	maxTickets := envInt("REPORT_MAX_TICKETS", 50)
	truncated := false
	if len(req.Tickets) > maxTickets {
		req.Tickets = req.Tickets[:maxTickets]
		truncated = true
	}

	jobs := make([]Job, len(req.Tickets))
	for i, t := range req.Tickets {
		jobs[i] = ReportJob{Ticket: t}
	}

	opts := PoolOptions{
		Size:          envInt("ORCH_POOL_SIZE", 5),
		MaxRetries:    envInt("ORCH_MAX_RETRIES", 2),
		BackoffBase:   envDuration("ORCH_BACKOFF_BASE", 200*time.Millisecond),
		PerJobTimeout: envDuration("ORCH_PER_JOB_TIMEOUT", 2*time.Minute),
		OnProgress: func(done, total int) {
			fmt.Printf("[orch] %d/%d\n", done, total)
		},
	}

	fmt.Printf("[orch] iniciando reporte: %d tickets, pool=%d\n", len(jobs), opts.Size)
	results := a.runPool(ctx, jobs, opts)

	ok, fail := 0, 0
	for _, r := range results {
		if r.Err != nil {
			fail++
		} else {
			ok++
		}
	}

	path, err := writeReport(req, results, truncated)
	if err != nil {
		return "", "", fmt.Errorf("escribiendo reporte: %w", err)
	}

	summary = fmt.Sprintf("Reporte listo: %d tickets evaluados (%d ok, %d no evaluados). Archivo: %s",
		len(results), ok, fail, path)
	if truncated {
		summary += fmt.Sprintf(" (truncado a %d, había más)", maxTickets)
	}
	return path, summary, nil
}

// writeReport vuelca el reporte a un .md y devuelve el path.
func writeReport(req ReportRequest, results []Result, truncated bool) (string, error) {
	dir := os.Getenv("REPORT_OUTPUT_DIR")
	if dir == "" {
		dir = "reports"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	slug := slugify(req.Descripcion)
	if slug == "" {
		slug = "reporte"
	}
	filename := time.Now().Format("2006-01-02-1504") + "-" + slug + ".md"
	path := filepath.Join(dir, filename)

	ok, fail := 0, 0
	for _, r := range results {
		if r.Err == nil {
			ok++
		} else {
			fail++
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Reporte: %s\n\n", req.Descripcion)
	fmt.Fprintf(&sb, "- **Generado**: %s\n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&sb, "- **Tickets evaluados**: %d (%d ok, %d no evaluados)\n", len(results), ok, fail)
	if truncated {
		fmt.Fprintln(&sb, "- **Nota**: la lista original superaba el tope; se procesaron los primeros N.")
	}
	sb.WriteString("\n## Detalle\n\n")

	for _, r := range results {
		fmt.Fprintf(&sb, "### %s\n\n", r.JobID)
		if r.Err != nil {
			fmt.Fprintf(&sb, "**No evaluado**\n- Motivo: %s\n- Reintentos: %d\n\n", r.Err, r.Retries)
			continue
		}
		body := strings.TrimSpace(r.Output)
		if body == "" {
			body = "_(respuesta vacía)_"
		}
		sb.WriteString(body)
		sb.WriteString("\n\n")
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
