// Package tui implementa la interfaz interactiva full-screen de aqua sobre
// Bubble Tea. Reusa los métodos públicos del Agent (SendMain, History,
// Sessions, etc.) y no toca el core.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"aqua/internal/agent"
)

// Run levanta el TUI bloqueante. Retorna cuando el usuario sale o ctx se cancela.
func Run(ctx context.Context, a *agent.Agent) error {
	m := newModel(a)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}
