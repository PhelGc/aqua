// Package tui implementa la interfaz interactiva full-screen de aqua sobre
// Bubble Tea. Por ahora es un esqueleto: muestra una pantalla placeholder y
// responde a Ctrl+C / Ctrl+Q. Las siguientes iteraciones agregan chat,
// eventos en vivo y modal de sesiones.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"aqua/internal/agent"
)

// Run levanta el TUI bloqueante. Retorna cuando el usuario sale o ctx se cancela.
func Run(ctx context.Context, a *agent.Agent) error {
	m := newModel(a)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// model es el estado de Bubble Tea. En el bootstrap solo trackeamos tamaño;
// los próximos pasos sumarán viewport, input, etc.
type model struct {
	agent  *agent.Agent
	width  int
	height int
}

func newModel(a *agent.Agent) model {
	return model{agent: a}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit
		}
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7DD3FC")) // celeste

	hintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#94A3B8")) // gris

	centerStyle = lipgloss.NewStyle().Align(lipgloss.Center, lipgloss.Center)
)

func (m model) View() string {
	if m.width == 0 {
		return "" // todavía no recibimos WindowSizeMsg
	}
	title := titleStyle.Render("aqua · TUI")
	subtitle := hintStyle.Render(fmt.Sprintf("modelo: %s · sesión: %s",
		m.agent.Model(), m.agent.Sessions().Current()))
	hint := hintStyle.Render("ctrl+c salir · (chat en construcción)")

	body := lipgloss.JoinVertical(lipgloss.Center, title, "", subtitle, "", hint)
	return centerStyle.Width(m.width).Height(m.height).Render(body)
}
