package tui

import (
	"context"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"aqua/internal/events"
	"aqua/internal/llm"
)

// smoothingDelay es el sleep entre runes emitidos al smoothear el stream.
// El endpoint manda chunks en ráfagas (varios chunks en 50ms y después pausa),
// así que el TUI los re-emite carácter por carácter a velocidad constante
// para que se vea fluido tipo máquina de escribir.
//
// 15ms ≈ 66 chars/s, sensación de "typing rápido" sin parecer instantáneo.
// Si el stream es genuinamente lento (chars llegan separados >50ms), pasamos
// igual sin acelerar — el smoother solo nivela las ráfagas, no las apura.
const smoothingDelay = 15 * time.Millisecond

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

// startSend lanza SendMainStreaming en background con un smoother en el medio
// y devuelve dos canales que el Update central consume.
//
// Arquitectura:
//
//	[agent] --rawCh--> [smoother goroutine] --deltaCh--> [tea.Cmd waitForDelta]
//
// El callback del agente mete el delta crudo en rawCh (canal grande, sin
// bloquear). El smoother lo descompone en runes y los re-emite uno por uno
// con sleep de smoothingDelay entre cada uno. Esto compensa que el endpoint
// manda chunks en ráfagas.
//
// Si ctx se cancela, el smoother corta inmediatamente.
func startSend(ctx context.Context, m model, input string) (deltaCh chan llm.StreamDelta, replyCh chan chatReplyMsg) {
	rawCh := make(chan llm.StreamDelta, 256)
	deltaCh = make(chan llm.StreamDelta, 64)
	replyCh = make(chan chatReplyMsg, 1)

	// Goroutine producer: corre el agent y empuja deltas crudos al rawCh.
	go func() {
		text, artifact, err := m.agent.SendMainStreaming(ctx, m.agent.Sessions().Current(), input,
			func(d llm.StreamDelta) {
				select {
				case rawCh <- d:
				case <-ctx.Done():
				}
			})
		close(rawCh)
		replyCh <- chatReplyMsg{text: text, artifact: artifact, err: err}
	}()

	// Goroutine smoother: drena rawCh y re-emite a deltaCh char por char.
	go func() {
		defer close(deltaCh)
		for raw := range rawCh {
			emitSmoothed(ctx, raw, deltaCh)
		}
	}()

	return deltaCh, replyCh
}

// emitSmoothed descompone un delta crudo en deltas por rune individual y los
// envía a out con un sleep entre cada uno. Maneja content y reasoning_content
// por separado (un delta puede tener ambos). Si ctx se cancela, corta.
func emitSmoothed(ctx context.Context, raw llm.StreamDelta, out chan<- llm.StreamDelta) {
	// Tool-calls y role se mandan tal cual (no son texto streameable).
	if len(raw.ToolCalls) > 0 || raw.Role != "" {
		select {
		case out <- raw:
		case <-ctx.Done():
			return
		}
	}
	streamRunes(ctx, raw.Content, false, out)
	streamRunes(ctx, raw.ReasoningContent, true, out)
}

// streamRunes envía cada rune de s como un delta por separado, durmiendo
// smoothingDelay entre cada uno. reasoning=true los manda como ReasoningContent
// en vez de Content.
func streamRunes(ctx context.Context, s string, reasoning bool, out chan<- llm.StreamDelta) {
	if s == "" {
		return
	}
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		piece := string(r)
		var d llm.StreamDelta
		if reasoning {
			d.ReasoningContent = piece
		} else {
			d.Content = piece
		}
		select {
		case out <- d:
		case <-ctx.Done():
			return
		}
		// Sleep solo entre runes para no congelar el final del stream.
		if len(s) > 0 {
			select {
			case <-time.After(smoothingDelay):
			case <-ctx.Done():
				return
			}
		}
	}
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
