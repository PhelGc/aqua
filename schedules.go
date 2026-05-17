package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// scheduleAction es el payload del marker <orchestrate kind="schedule">.
// Soporta create/cancel/list. La acción la decide el LLM según lo que pida
// el usuario en lenguaje natural.
type scheduleAction struct {
	Action  string `json:"action"`            // create | cancel | list
	ID      string `json:"id,omitempty"`      // para cancel
	Label   string `json:"label,omitempty"`   // descripción humana
	Command string `json:"command,omitempty"` // texto a ejecutar (puede empezar con /)
	NextRun string `json:"next_run,omitempty"` // ISO8601 con offset, para one-shot
	Cron    string `json:"cron,omitempty"`     // expresión cron de 5 campos
}

// runScheduleAdapter procesa el marker schedule. Devuelve (artifact, summary, err).
// Para esta clase de adapter no hay artifact (no genera archivo); el summary es el
// feedback humano de qué se programó/canceló.
func (a *agent) runScheduleAdapter(ctx context.Context, payload string) (artifact, summary string, err error) {
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

func (a *agent) scheduleCreate(act scheduleAction) (string, string, error) {
	if a.scheduler == nil {
		return "", "", fmt.Errorf("scheduler no inicializado")
	}
	if strings.TrimSpace(act.Command) == "" {
		return "", "", fmt.Errorf("falta el campo command")
	}
	if act.NextRun == "" && act.Cron == "" {
		return "", "", fmt.Errorf("falta trigger: pasá 'next_run' (ISO8601) o 'cron' (5 campos)")
	}
	sched := &Schedule{
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
	if err := a.scheduler.add(sched); err != nil {
		return "", "", err
	}
	summary := fmt.Sprintf("Programada %s: %q · próximo disparo: %s",
		sched.ID,
		firstNonEmpty(sched.Label, sched.Command),
		formatScheduleTime(sched.NextRun))
	if sched.Cron != "" {
		summary += fmt.Sprintf(" (cron: %s)", sched.Cron)
	}
	a.emit("schedule_created", sched.ID, map[string]any{
		"label":    sched.Label,
		"command":  sched.Command,
		"next_run": sched.NextRun.Format(time.RFC3339),
		"cron":     sched.Cron,
	})
	return "", summary, nil
}

func (a *agent) scheduleCancel(act scheduleAction) (string, string, error) {
	if a.scheduler == nil {
		return "", "", fmt.Errorf("scheduler no inicializado")
	}
	if act.ID == "" {
		return "", "", fmt.Errorf("falta el campo id")
	}
	if err := a.scheduler.cancel(act.ID); err != nil {
		return "", "", err
	}
	a.emit("schedule_cancelled", act.ID, nil)
	return "", fmt.Sprintf("Cancelada %s.", act.ID), nil
}

func (a *agent) scheduleList() (string, string, error) {
	if a.scheduler == nil {
		return "", "", fmt.Errorf("scheduler no inicializado")
	}
	items := a.scheduler.list()
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

// runScheduled es el callback que el scheduler invoca cuando una tarea dispara.
// Crea una sesión nueva por corrida para no contaminar la conversación del usuario.
// El output queda persistido en sessions/ y los artifacts en reports/.
func (a *agent) runScheduled(ctx context.Context, sched *Schedule) {
	start := time.Now()
	sessionName := fmt.Sprintf("sched-%s-%d", strings.TrimPrefix(sched.ID, "sch-"), start.Unix())

	// Resolvemos slash commands antes (igual que en terminal/UI).
	input := sched.Command
	if strings.HasPrefix(input, "/") {
		cmd, args, _ := strings.Cut(input[1:], " ")
		args = strings.TrimSpace(args)
		if rendered, ok := a.skills.render(cmd, args); ok {
			input = rendered
		}
	}

	fmt.Printf("[sched %s] disparando: %q (sesión: %s)\n", sched.ID, sched.Command, sessionName)
	a.emit("schedule_fired", sched.ID, map[string]any{
		"command":      sched.Command,
		"label":        sched.Label,
		"session_name": sessionName,
	})

	history := []message{}
	reply, artifact, err := a.sendAndDispatch(ctx, &history, sessionName, input)
	elapsed := time.Since(start).Round(time.Second)
	if err != nil {
		fmt.Printf("[sched %s] ERROR tras %s: %v\n", sched.ID, elapsed, err)
		a.emit("schedule_error", sched.ID, map[string]any{
			"error":   err.Error(),
			"elapsed": elapsed.String(),
		})
		return
	}
	a.scheduler.markRun(sched.ID, start)
	fmt.Printf("[sched %s] ok en %s · sesión: %s · artifact: %q\n", sched.ID, elapsed, sessionName, artifact)
	a.emit("schedule_done", sched.ID, map[string]any{
		"session_name": sessionName,
		"artifact":     artifact,
		"reply":        truncateForLog(reply, 200),
		"elapsed":      elapsed.String(),
	})
}

// parseScheduleTime acepta ISO8601 con offset y también formatos relajados.
func parseScheduleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",  // sin offset → asume local
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
