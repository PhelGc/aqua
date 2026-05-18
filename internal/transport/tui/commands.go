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

// streamDoneMsg avisa que el canal de deltas se cerró y no van a llegar
// más chunks. El Update lo usa para coordinar el cierre del turn cuando
// también llegó chatReplyMsg.
type streamDoneMsg struct{}

// eventMsg envuelve un events.Event para que el Update central lo procese.
type eventMsg events.Event

// startSend lanza SendMainStreaming en background. Los deltas del LLM van
// directo de la API al TUI sin smoothing intermedio: lo que llega del
// endpoint se renderiza apenas Bubble Tea lo procesa.
//
// Devuelve dos canales:
//   - deltaCh: cada chunk crudo del LLM (puede tener content y/o reasoning)
//   - replyCh: resultado consolidado cuando el agent terminó
//
// Cerramos deltaCh apenas el agent retorna para que waitForDelta emita
// streamDoneMsg y el TUI pueda coordinar el cierre del turn.
func startSend(ctx context.Context, m model, input string) (deltaCh chan llm.StreamDelta, replyCh chan chatReplyMsg) {
	deltaCh = make(chan llm.StreamDelta, 256)
	replyCh = make(chan chatReplyMsg, 1)
	go func() {
		text, artifact, err := m.agent.SendMainStreaming(ctx, m.agent.Sessions().Current(), input,
			func(d llm.StreamDelta) {
				// Non-blocking: si el buffer está lleno, el chunk se cae al
				// piso. El reply final llega entero por replyCh igual, así
				// que no perdemos texto definitivo, solo "frames" del stream.
				select {
				case deltaCh <- d:
				case <-ctx.Done():
				}
			})
		close(deltaCh)
		replyCh <- chatReplyMsg{text: text, artifact: artifact, err: err}
	}()
	return deltaCh, replyCh
}

// waitForDelta toma el próximo delta del canal y lo entrega como tea.Msg.
// Cuando el canal se cierra emite streamDoneMsg para que el Update pueda
// coordinar el cierre del turn (en vez de devolver nil, que no genera msg
// y dejaría el state colgado si el reply llega antes).
func waitForDelta(ch <-chan llm.StreamDelta) tea.Cmd {
	return func() tea.Msg {
		d, ok := <-ch
		if !ok {
			return streamDoneMsg{}
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
