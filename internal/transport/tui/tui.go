// Package tui implementa la interfaz interactiva full-screen de aqua sobre
// Bubble Tea. Reusa los métodos públicos del Agent (SendMain, History,
// Sessions, etc.) y no toca el core.
package tui

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"aqua/internal/agent"
	"aqua/internal/events"
)

// Run levanta el TUI bloqueante. Retorna cuando el usuario sale o ctx se cancela.
//
// Silencia stdout y stderr durante la ejecución porque el agente y MCP
// imprimen logs crudos (por ejemplo `[tool] xxx` desde chat.go) que romperían
// el rendering de Bubble Tea. La info equivalente llega al viewport vía el
// event sink. Trade-off: si algo paniquea no vamos a ver el stack — si esto
// se vuelve un problema, redirigir stderr a un archivo log opcional.
func Run(ctx context.Context, a *agent.Agent) error {
	origStdout, origStderr := os.Stdout, os.Stderr
	silenced, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stdout = silenced
		os.Stderr = silenced
		defer func() {
			os.Stdout = origStdout
			os.Stderr = origStderr
			silenced.Close()
		}()
	}

	// El TUI consume el mismo FanoutSink que la web. Buffer chico (la TUI
	// renderiza directo) e historial corto (Bubble Tea no necesita replay).
	sink := events.NewFanout(64, 100)
	a.SetEvents(sink)

	m := newModel(a, sink)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}
