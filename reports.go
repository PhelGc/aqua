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
// <orchestrate kind="report">. El LLM solo manda la lista de KEYs; cada worker
// es autónomo y fetchea su propio ticket vía MCP.
type ReportRequest struct {
	Descripcion string   `json:"descripcion"`
	Tickets     []string `json:"tickets"`
}

// ReportJob implementa Job. Cada worker corre el skill /evaluar-tarea sobre
// un ticket de forma 100% autónoma: lee docs, llama Jira, decide.
type ReportJob struct {
	Key      string
	Rendered string // /evaluar-tarea renderizado con la KEY como {{input}}
}

const reportStyleOverride = `Estás procesando un ticket dentro de un reporte batch.

REGLAS DE FORMATO ESTRICTAS (sobreescriben tu personalidad por completo):
- Tu mensaje final DEBE empezar literalmente con "**Veredicto general:**".
- CERO preámbulo. NADA antes de esa primera línea. Ni "Procedo con la evaluación.", ni "Ya tengo la info.", ni "Acá va.", ni una sola palabra. Si lo hacés, el reporte queda inconsistente.
- Headers de sección **exactamente** así: --- Título --- / --- Descripción --- / --- Conclusión --- / --- Armonía --- (SIN ### adelante, SIN ## adelante, SIN bold).
- Claves de criterio SIN bold: "- Prefijo: ✅ — ..." (NO "- **Prefijo:** ✅").
- Omití líneas de criterios que NO aplican al tipo (no imprimas "Story Points: ✅ — No aplica").
- Omití "**Sugerencia:**" entera si el título está bien (no la imprimas vacía ni con guión).
- NO uses voz teatral, NO saludos, NO floritura, NO tercera persona.
- NO emitas markers <orchestrate> ni JSON. Solo el markdown del veredicto.

Por lo demás trabajás como siempre: leé el INDEX si te hace falta, consultá Jira, aplicá las reglas internas. La autonomía es total para investigar; el formato del output final es rígido.`

func (j ReportJob) ID() string         { return j.Key }
func (j ReportJob) System() []string   { return []string{reportStyleOverride} }
func (j ReportJob) Prompt() string     { return j.Rendered }

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
	for i, key := range req.Tickets {
		rendered, ok := a.skills.render("evaluar-tarea", key)
		if !ok {
			return "", "", fmt.Errorf("skill 'evaluar-tarea' no está cargada; cada worker la necesita")
		}
		jobs[i] = ReportJob{Key: key, Rendered: rendered}
	}

	opts := PoolOptions{
		Size:          envInt("ORCH_POOL_SIZE", 5),
		MaxRetries:    envInt("ORCH_MAX_RETRIES", 2),
		BackoffBase:   envDuration("ORCH_BACKOFF_BASE", 200*time.Millisecond),
		PerJobTimeout: envDuration("ORCH_PER_JOB_TIMEOUT", 2*time.Minute),
		OnProgress: func(done, total int, r Result) {
			status := "ok"
			if r.Err != nil {
				status = fmt.Sprintf("FAIL: %v", r.Err)
			}
			fmt.Printf("[orch] %d/%d  %s  (%s, %s)\n", done, total, r.JobID, r.Elapsed.Round(time.Millisecond), status)
		},
	}

	fmt.Printf("[orch] iniciando reporte: %d tickets, pool=%d\n", len(jobs), opts.Size)
	a.emit("orch_start", "", map[string]any{
		"kind":        "report",
		"descripcion": req.Descripcion,
		"total":       len(jobs),
		"pool_size":   opts.Size,
		"job_ids":     req.Tickets,
	})
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
	a.emit("orch_done", "", map[string]any{
		"kind":        "report",
		"descripcion": req.Descripcion,
		"artifact":    path,
		"ok":          ok,
		"fail":        fail,
		"truncated":   truncated,
	})
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
		body := stripPreamble(strings.TrimSpace(r.Output))
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

// stripPreamble corta lo que el worker haya escrito antes del "**Veredicto general:**"
// que la plantilla exige como primera línea. Red de seguridad cuando el style
// override no fue suficiente. Si el marker no aparece, devuelve el body tal cual.
func stripPreamble(body string) string {
	const marker = "**Veredicto general:**"
	if idx := strings.Index(body, marker); idx > 0 {
		return strings.TrimSpace(body[idx:])
	}
	return body
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
