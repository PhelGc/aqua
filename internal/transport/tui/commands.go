package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// chatReplyMsg es el resultado de SendMain. La goroutine que envía lo
// devuelve via tea.Cmd al Update central.
type chatReplyMsg struct {
	text     string
	artifact string
	err      error
}

// sendChat dispara SendMain en background. Devuelve un tea.Cmd que cuando
// completa envía chatReplyMsg al loop principal.
func sendChat(ctx context.Context, m model, input string) tea.Cmd {
	return func() tea.Msg {
		text, artifact, err := m.agent.SendMain(ctx, m.agent.Sessions().Current(), input)
		return chatReplyMsg{text: text, artifact: artifact, err: err}
	}
}
