package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"aqua/internal/llm"
	"aqua/internal/notifier"
	"aqua/internal/scheduler"
)

// scheduleAction es el payload del marker <orchestrate kind="schedule">.
// Soporta create/cancel/list. La acción la decide el LLM según lo que pida
// el usuario en lenguaje natural.
type scheduleAction struct {
	Action  string `json:"action"`             // create | cancel | list
	ID      string `json:"id,omitempty"`       // para cancel
	Label   string `json:"label,omitempty"`    // descripción humana
	Command string `json:"command,omitempty"`  // texto a ejecutar (puede empezar con /)
	NextRun string `json:"next_run,omitempty"` // ISO8601 con offset, para one-shot
	Cron    string `json:"cron,omitempty"`     // expresión cron de 5 campos
}

// RunScheduleAdapter procesa el marker schedule. Devuelve (artifact, summary, err).
// Para esta clase de adapter no hay artifact (no genera archivo); el summary es el
// feedback humano de qué se programó/canceló.
func (a *Agent) RunScheduleAdapter(payload string) (artifact, summary string, err error) {
	var act scheduleAction
	if err := json.Unmarshal([]byte(payload), &act); err != nil {
		return "", "", fmt.Errorf("payload de schedule inválido: %w", err)
	}
	switch act.Action {
	case "create":
		return a.scheduleCreate(act)
	case "cancel":
		return a.scheduleCancel(act)
	case "list":
		return a.scheduleList()
	default:
		return "", "", fmt.Errorf("action desconocida: %q (usar create|cancel|list)", act.Action)
	}
}

func (a *Agent) scheduleCreate(act scheduleAction) (string, string, error) {
	if a.scheduler == nil {
		return "", "", fmt.Errorf("scheduler no inicializado")
	}
	if strings.TrimSpace(act.Command) == "" {
		return "", "", fmt.Errorf("falta el campo command")
	}
	if act.NextRun == "" && act.Cron == "" {
		return "", "", fmt.Errorf("falta trigger: pasá 'next_run' (ISO8601) o 'cron' (5 campos)")
	}
	sched := &scheduler.Schedule{
		Label:   act.Label,
		Command: act.Command,
		Cron:    strings.TrimSpace(act.Cron),
	}
	if act.NextRun != "" {
		t, err := parseScheduleTime(act.NextRun)
		if err != nil {
			return "", "", fmt.Errorf("next_run inválido %q: %w", act.NextRun, err)
		}
		sched.NextRun = t
	}
	if err := a.scheduler.Add(sched); err != nil {
		return "", "", err
	}
	summary := fmt.Sprintf("Programada %s: %q · próximo disparo: %s",
		sched.ID,
		firstNonEmpty(sched.Label, sched.Command),
		formatScheduleTime(sched.NextRun))
	if sched.Cron != "" {
		summary += fmt.Sprintf(" (cron: %s)", sched.Cron)
	}
	a.Emit("schedule_created", sched.ID, map[string]any{
		"label":    sched.Label,
		"command":  sched.Command,
		"next_run": sched.NextRun.Format(time.RFC3339),
		"cron":     sched.Cron,
	})
	return "", summary, nil
}

func (a *Agent) scheduleCancel(act scheduleAction) (string, string, error) {
	if a.scheduler == nil {
		return "", "", fmt.Errorf("scheduler no inicializado")
	}
	if act.ID == "" {
		return "", "", fmt.Errorf("falta el campo id")
	}
	if err := a.scheduler.Cancel(act.ID); err != nil {
		return "", "", err
	}
	a.Emit("schedule_cancelled", act.ID, nil)
	return "", fmt.Sprintf("Cancelada %s.", act.ID), nil
}

func (a *Agent) scheduleList() (string, string, error) {
	if a.scheduler == nil {
		return "", "", fmt.Errorf("scheduler no inicializado")
	}
	items := a.scheduler.List()
	if len(items) == 0 {
		return "", "No hay tareas programadas.", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d tarea(s) programada(s):\n", len(items)))
	for _, s := range items {
		state := "activa"
		if !s.Enabled {
			state = "inactiva"
		}
		line := fmt.Sprintf("- **%s** (%s) · próx: %s · %q",
			s.ID, state, formatScheduleTime(s.NextRun), firstNonEmpty(s.Label, s.Command))
		if s.Cron != "" {
			line += fmt.Sprintf(" · cron: %s", s.Cron)
		}
		if s.RunCount > 0 {
			line += fmt.Sprintf(" · corridas: %d", s.RunCount)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return "", strings.TrimRight(sb.String(), "\n"), nil
}

// RunScheduled es el callback que el scheduler invoca cuando una tarea dispara.
// Crea una sesión nueva por corrida para no contaminar la conversación del usuario.
// El output queda persistido en sessions/ y los artifacts en reports/.
func (a *Agent) RunScheduled(ctx context.Context, sched *scheduler.Schedule) {
	start := time.Now()
	sessionName := fmt.Sprintf("sched-%s-%d", strings.TrimPrefix(sched.ID, "sch-"), start.Unix())

	// Resolvemos slash commands antes (igual que en terminal/UI).
	// Si el comando es /algo y la skill no existe, abortamos: pasar el string
	// crudo al LLM hace que evalúe inline en formato libre, que es lo opuesto
	// de lo que se quiere en un schedule.
	input := sched.Command
	if strings.HasPrefix(input, "/") {
		cmd, args, _ := strings.Cut(input[1:], " ")
		args = strings.TrimSpace(args)
		rendered, ok := a.skills.Render(cmd, args)
		if !ok {
			errMsg := fmt.Sprintf("skill /%s no existe. Editá el schedule para usar una skill cargada.", cmd)
			fmt.Printf("[sched %s] ERROR: %s\n", sched.ID, errMsg)
			a.Emit("schedule_error", sched.ID, map[string]any{"error": errMsg})
			a.notifySchedule(ctx, sched, fmt.Sprintf("❌ **%s** no corrió: %s",
				firstNonEmpty(sched.Label, sched.Command), errMsg), "")
			return
		}
		input = rendered
	}

	fmt.Printf("[sched %s] disparando: %q (sesión: %s)\n", sched.ID, sched.Command, sessionName)
	a.Emit("schedule_fired", sched.ID, map[string]any{
		"command":      sched.Command,
		"label":        sched.Label,
		"session_name": sessionName,
	})

	history := []llm.Message{}
	reply, artifact, err := a.SendAndDispatch(ctx, &history, sessionName, input)
	elapsed := time.Since(start).Round(time.Second)
	if err != nil {
		fmt.Printf("[sched %s] ERROR tras %s: %v\n", sched.ID, elapsed, err)
		a.Emit("schedule_error", sched.ID, map[string]any{
			"error":   err.Error(),
			"elapsed": elapsed.String(),
		})
		a.notifySchedule(ctx, sched, fmt.Sprintf("❌ **%s** falló tras %s\nError: %s",
			firstNonEmpty(sched.Label, sched.Command), elapsed, err.Error()), "")
		return
	}
	a.scheduler.MarkRun(sched.ID, start)
	fmt.Printf("[sched %s] ok en %s · sesión: %s · artifact: %q\n", sched.ID, elapsed, sessionName, artifact)
	a.Emit("schedule_done", sched.ID, map[string]any{
		"session_name": sessionName,
		"artifact":     artifact,
		"reply":        truncateForLog(reply, 200),
		"elapsed":      elapsed.String(),
	})

	var msg strings.Builder
	fmt.Fprintf(&msg, "✅ **%s** listo en %s\n", firstNonEmpty(sched.Label, sched.Command), elapsed)
	fmt.Fprintf(&msg, "Sesión: `%s`\n", sessionName)
	if artifact != "" {
		msg.WriteString("Reporte adjunto.")
	} else {
		msg.WriteString("\n")
		msg.WriteString(strings.TrimSpace(reply))
	}
	a.notifySchedule(ctx, sched, msg.String(), artifact)
}

// notifySchedule envía notificación al notifier configurado (noop si no hay
// webhook). Errores de notificación se loggean pero no abortan el flujo.
func (a *Agent) notifySchedule(ctx context.Context, sched *scheduler.Schedule, message, attachment string) {
	notifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.notifier.Notify(notifyCtx, message, notifier.Opts{
		Attachment: attachment,
		Username:   "aqua · scheduler",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[sched %s] warning notificando: %v\n", sched.ID, err)
	}
}

// parseScheduleTime acepta ISO8601 con offset y también formatos relajados.
func parseScheduleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05", // sin offset → asume local
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("formato no reconocido")
}

func formatScheduleTime(t time.Time) string {
	if t.IsZero() {
		return "(sin fecha)"
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// truncateForLog acorta strings para logs, reemplazando newlines por espacios.
func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
