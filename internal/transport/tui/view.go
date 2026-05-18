package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"aqua/internal/events"
)

// minViewportRows y minViewportCols son los pisos para considerar la
// terminal "usable". Por debajo, mostramos un mensaje de "muy chica".
const (
	minTermRows = 8
	minTermCols = 20
)

func (m model) View() string {
	if m.width == 0 {
		return "" // todavía no recibimos WindowSizeMsg
	}
	if m.height < minTermRows || m.width < minTermCols {
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			mutedStyle.Render("terminal muy chica · agrandá la ventana"))
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	input := inputStyle.Render(m.input.View())

	// El popup de completion va arriba del input. Recortamos el viewport
	// para hacerle lugar (sino se solaparían en la última línea visible).
	var bodyParts []string
	bodyParts = append(bodyParts, header)
	if m.completion.visible {
		popup := m.completion.view(m.width, m.height)
		popupHeight := lipgloss.Height(popup)
		vpView := m.viewportTrimmed(popupHeight)
		bodyParts = append(bodyParts, vpView, popup)
	} else {
		bodyParts = append(bodyParts, m.viewport.View())
	}
	bodyParts = append(bodyParts, input, footer)
	base := lipgloss.JoinVertical(lipgloss.Left, bodyParts...)

	if m.state == stateSessionsModal {
		// Overlay del modal centrado sobre el chat. lipgloss.Place compone
		// el modal sobre un fondo del tamaño de la pantalla, pero no preserva
		// el contenido de abajo. Para no perder el chat, lo dejamos vacío
		// detrás (efecto "tomar foco").
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			m.sessionsModal.view(m.width, m.height))
	}

	return base
}

// viewportTrimmed renderiza el viewport pero recortando las últimas N filas
// para hacerle lugar al popup. No modifica el viewport real — solo trunca
// el string de salida.
func (m model) viewportTrimmed(rows int) string {
	full := m.viewport.View()
	lines := strings.Split(full, "\n")
	keep := len(lines) - rows
	if keep < 1 {
		keep = 1
	}
	return strings.Join(lines[:keep], "\n")
}

func (m model) renderHeader() string {
	// Versión completa para pantallas anchas, compacta para angostas.
	// El padding de headerStyle (0,1) suma 2 chars de cada lado: descontamos.
	avail := m.width - 4
	full := fmt.Sprintf("aqua · %s · %d msgs · %d tools · %d skills",
		m.agent.Sessions().Current(),
		m.agent.HistoryLen(),
		len(m.agent.MCP().Tools()),
		len(m.agent.Skills().List()),
	)
	short := fmt.Sprintf("aqua · %s · %d msgs",
		m.agent.Sessions().Current(),
		m.agent.HistoryLen(),
	)
	title := full
	if lipgloss.Width(full) > avail {
		title = short
	}
	if lipgloss.Width(title) > avail {
		title = truncateANSI(title, avail)
	}
	return headerStyle.Width(m.width).Render(title)
}

func (m model) renderFooter() string {
	var hint string
	if m.state == stateSending {
		hint = m.spinner.View() + " enviando… · esc cancelar"
	} else if m.completion.visible {
		hint = "↑↓ navegar · tab/enter completar · esc cerrar"
	} else if m.width < 60 {
		hint = "ctrl+c salir · ctrl+s sesiones"
	} else {
		hint = "ctrl+c salir · enter enviar · ctrl+j línea nueva · ctrl+s sesiones"
	}
	avail := m.width - 4
	if lipgloss.Width(hint) > avail {
		hint = truncateANSI(hint, avail)
	}
	return footerStyle.Width(m.width).Render(hint)
}

// truncateANSI corta un string visible a max columnas. Usa rune-count que es
// suficiente para nuestros casos (no tenemos ANSI codes dentro del texto, los
// estilos los agrega lipgloss después).
func truncateANSI(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
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
	case lineThinkingInProgress:
		// Reasoning visible en gris cursiva mientras llega. Si la línea
		// está vacía (recién creada antes del primer chunk), mostramos
		// solo el header.
		header := "· pensando"
		if l.text != "" {
			return thinkingStyle.Render(header + "\n" + wrap.Render(l.text))
		}
		return thinkingStyle.Render(header + "…")
	case lineThinkingClosed:
		// Wrap respetando el ancho del viewport.
		return mutedStyle.Render(wrap.Render("» thinking: " + l.text))
	}
	return l.text
}
