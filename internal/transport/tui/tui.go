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
// Silencia stdout para que los logs crudos del agente/MCP no rompan el
// rendering de Bubble Tea. Stderr lo redirige a aqua-tui.log para que los
// panics y warnings queden recuperables (sino se pierden y debugar es
// imposible).
func Run(ctx context.Context, a *agent.Agent) error {
	// Guardamos el TTY original para pasárselo explícitamente a Bubble Tea.
	// Si redirigíamos os.Stdout sin esto, Bubble Tea heredaba el /dev/null y
	// renderizaba a la nada → pantalla negra.
	origStdout, origStderr := os.Stdout, os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stdout = devNull
		defer func() {
			os.Stdout = origStdout
			devNull.Close()
		}()
	}
	logFile, err := os.OpenFile("aqua-tui.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		os.Stderr = logFile
		defer func() {
			os.Stderr = origStderr
			logFile.Close()
		}()
	}

	// El TUI consume el mismo FanoutSink que la web. Buffer chico (la TUI
	// renderiza directo) e historial corto (Bubble Tea no necesita replay).
	sink := events.NewFanout(64, 100)
	a.SetEvents(sink)

	m := newModel(a, sink, origStdout)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
		tea.WithOutput(origStdout), // forzar render al TTY real, no al NUL
	)
	_, err = p.Run()
	return err
}
