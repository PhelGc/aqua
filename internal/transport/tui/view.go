package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"aqua/internal/events"
)

func (m model) View() string {
	if m.width == 0 {
		return "" // todavía no recibimos WindowSizeMsg
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	body := m.viewport.View()
	input := inputStyle.Render(m.input.View())
	base := lipgloss.JoinVertical(lipgloss.Left, header, body, input, footer)

	if m.state == stateSessionsModal {
		// Overlay del modal centrado sobre el chat. lipgloss.Place compone
		// el modal sobre un fondo del tamaño de la pantalla, pero no preserva
		// el contenido de abajo. Para no perder el chat, lo dejamos vacío
		// detrás (efecto "tomar foco").
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			m.sessionsModal.view(m.width))
	}

	return base
}

func (m model) renderHeader() string {
	title := fmt.Sprintf("aqua · %s · %d msgs · %d tools · %d skills",
		m.agent.Sessions().Current(),
		m.agent.HistoryLen(),
		len(m.agent.MCP().Tools()),
		len(m.agent.Skills().List()),
	)
	return headerStyle.Width(m.width).Render(title)
}

func (m model) renderFooter() string {
	var hint string
	if m.state == stateSending {
		hint = m.spinner.View() + " enviando…"
	} else {
		hint = "ctrl+c salir · enter enviar · ctrl+j línea nueva"
	}
	return footerStyle.Width(m.width).Render(hint)
}

// renderChat compila las chatLines en un solo string ya estilizado, listo
// para meter en el viewport. width permite envolver párrafos respetando el
// ancho actual (con un margen interno chico).
func renderChat(lines []chatLine, width int) string {
	if len(lines) == 0 {
		return mutedStyle.Render("(sin mensajes aún · escribí algo abajo)")
	}
	inner := width - 4
	if inner < 20 {
		inner = 20
	}
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(renderLine(l, inner))
	}
	return sb.String()
}

// renderEvent traduce un Event a una línea para el viewport. Devuelve
// (text, false) cuando el evento no aporta nada visual (ej. job_start
// silencioso). El llamador decide qué hacer si !ok.
//
// Mapeo:
//   tool_call          → "<jobId> tool_name" (jobId vacío = agente principal)
//   schedule_fired     → "schedule X disparado"
//   schedule_done      → "schedule X terminado en Xs"
//   schedule_error     → "schedule X falló: ..."
//   schedule_created   → "schedule X programado"
//   schedule_cancelled → "schedule X cancelado"
//   orch_start         → "orch: N jobs · pool=K"
//   orch_done          → "orch: M/N ok · artifact"
//   job_done           → "<jobId> ok/fail (Ns)"
//   job_start          → omitido (ruidoso)
//   chat_*             → omitidos (la TUI ya los muestra como user/assistant)
func renderEvent(evt events.Event) (string, bool) {
	switch evt.Type {
	case "tool_call":
		tool, _ := evt.Payload["tool"].(string)
		if evt.JobID != "" {
			return fmt.Sprintf("[%s] %s", evt.JobID, tool), true
		}
		return tool, true

	case "schedule_fired":
		label, _ := evt.Payload["label"].(string)
		if label == "" {
			label, _ = evt.Payload["command"].(string)
		}
		return fmt.Sprintf("schedule %s disparado: %s", evt.JobID, label), true

	case "schedule_done":
		elapsed, _ := evt.Payload["elapsed"].(string)
		return fmt.Sprintf("schedule %s ok (%s)", evt.JobID, elapsed), true

	case "schedule_error":
		errMsg, _ := evt.Payload["error"].(string)
		return fmt.Sprintf("schedule %s falló: %s", evt.JobID, errMsg), true

	case "schedule_created":
		label, _ := evt.Payload["label"].(string)
		return fmt.Sprintf("schedule %s programado: %s", evt.JobID, label), true

	case "schedule_cancelled":
		return fmt.Sprintf("schedule %s cancelado", evt.JobID), true

	case "orch_start":
		total, _ := evt.Payload["total"].(int)
		poolSize, _ := evt.Payload["pool_size"].(int)
		// total/poolSize pueden venir como float64 si vinieron de JSON;
		// los eventos en este código son map[string]any directo, así que
		// el cast a int debería andar — fallback igual.
		if total == 0 {
			if f, ok := evt.Payload["total"].(float64); ok {
				total = int(f)
			}
		}
		if poolSize == 0 {
			if f, ok := evt.Payload["pool_size"].(float64); ok {
				poolSize = int(f)
			}
		}
		return fmt.Sprintf("orch start · %d jobs · pool=%d", total, poolSize), true

	case "orch_done":
		ok, _ := evt.Payload["ok"].(int)
		fail, _ := evt.Payload["fail"].(int)
		artifact, _ := evt.Payload["artifact"].(string)
		base := fmt.Sprintf("orch done · %d ok · %d fail", ok, fail)
		if artifact != "" {
			base += " · " + artifact
		}
		return base, true

	case "job_done":
		elapsedMs, _ := evt.Payload["elapsed_ms"].(int64)
		success, _ := evt.Payload["success"].(bool)
		status := "ok"
		if !success {
			status = "fail"
		}
		return fmt.Sprintf("[%s] %s (%dms)", evt.JobID, status, elapsedMs), true
	}

	return "", false
}

func renderLine(l chatLine, width int) string {
	wrap := lipgloss.NewStyle().Width(width)
	switch l.kind {
	case lineUser:
		return userPrefix + wrap.Render(l.text)
	case lineAssistant:
		return aquaPrefix + wrap.Render(l.text)
	case lineTool:
		return toolStyle.Render("⚡ " + l.text)
	case lineError:
		return errorStyle.Render("✗ " + l.text)
	case lineSystem:
		return mutedStyle.Render("· " + l.text)
	}
	return l.text
}
