package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"aqua/internal/events"
)

// chatReplyMsg es el resultado de SendMain. La goroutine que envía lo
// devuelve via tea.Cmd al Update central.
type chatReplyMsg struct {
	text     string
	artifact string
	err      error
}

// eventMsg envuelve un events.Event para que el Update central lo procese.
type eventMsg events.Event

// sendChat dispara SendMain en background. Devuelve un tea.Cmd que cuando
// completa envía chatReplyMsg al loop principal.
func sendChat(ctx context.Context, m model, input string) tea.Cmd {
	return func() tea.Msg {
		text, artifact, err := m.agent.SendMain(ctx, m.agent.Sessions().Current(), input)
		return chatReplyMsg{text: text, artifact: artifact, err: err}
	}
}

// waitForEvent toma el próximo Event del canal del subscriber y lo entrega
// como tea.Msg. Bubble Tea garantiza que solo un tea.Cmd está corriendo a la
// vez para esta cadena, así que después de procesar el msg el Update vuelve
// a llamar a esta función — patrón estándar para streams en Bubble Tea.
func waitForEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil // canal cerrado, no reagendar
		}
		return eventMsg(evt)
	}
}
