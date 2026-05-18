package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
