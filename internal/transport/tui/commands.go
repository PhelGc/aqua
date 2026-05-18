package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"aqua/internal/events"
	"aqua/internal/llm"
)

// chatReplyMsg es el resultado de SendMain. La goroutine que envía lo
// devuelve via tea.Cmd al Update central.
type chatReplyMsg struct {
	text     string
	artifact string
	err      error
}

// streamDeltaMsg envuelve un delta del stream del LLM para el Update central.
type streamDeltaMsg llm.StreamDelta

// eventMsg envuelve un events.Event para que el Update central lo procese.
type eventMsg events.Event

// startSend lanza SendMainStreaming en background y devuelve dos Cmds:
//   - el listener del canal de deltas (waitForDelta)
//   - el "esperá la respuesta final" (waitForReply)
//
// Bubble Tea ejecuta ambos en paralelo. Cuando llega un delta el listener
// se reagenda; cuando llega el chatReplyMsg, el caller deja de re-encolar
// waitForDelta y cierra el ciclo.
//
// El canal deltaCh tiene buffer porque el agent corre dentro de un Lock y no
// queremos que el callback se bloquee si la TUI tarda un frame en consumir.
func startSend(ctx context.Context, m model, input string) (deltaCh chan llm.StreamDelta, replyCh chan chatReplyMsg) {
	deltaCh = make(chan llm.StreamDelta, 64)
	replyCh = make(chan chatReplyMsg, 1)
	go func() {
		text, artifact, err := m.agent.SendMainStreaming(ctx, m.agent.Sessions().Current(), input,
			func(d llm.StreamDelta) {
				// Non-blocking: si el buffer está lleno (TUI lenta), dropeamos
				// el delta. El mensaje final llega entero por replyCh igual.
				select {
				case deltaCh <- d:
				default:
				}
			})
		close(deltaCh)
		replyCh <- chatReplyMsg{text: text, artifact: artifact, err: err}
	}()
	return deltaCh, replyCh
}

// waitForDelta toma el próximo delta del canal y lo entrega como tea.Msg.
// Devuelve nil cuando el canal se cierra (= la goroutine de send terminó);
// el Update central usa eso para dejar de re-encolar.
func waitForDelta(ch <-chan llm.StreamDelta) tea.Cmd {
	return func() tea.Msg {
		d, ok := <-ch
		if !ok {
			return nil
		}
		return streamDeltaMsg(d)
	}
}

// waitForReply espera la respuesta final consolidada de SendMainStreaming.
func waitForReply(ch <-chan chatReplyMsg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
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
