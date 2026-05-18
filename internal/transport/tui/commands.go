package tui

import (
	"context"
	"time"

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

	// Goroutine smoother: arquitectura productor/consumidor interno.
	//
	// Mantiene DOS buffers (reasoning y content) y un ticker que cada
	// smoothingDelay saca el siguiente rune del buffer apropiado y lo manda
	// a deltaCh. Esto evita el bug de bloquear el drain de rawCh mientras
	// se hace el sleep entre runes.
	//
	// Orden de emisión:
	//   1. Mientras haya runes pendientes en reasoningBuf, emitir esos.
	//   2. Solo cuando reasoningBuf está vacío Y pasaron reasoningSettleTime
	//      sin recibir más reasoning, empezar a emitir contentBuf.
	//   3. Tool-calls y role: pass-through inmediato.
	//
	// Termina cuando rawCh está cerrado y ambos buffers vacíos.
	go func() {
		defer close(deltaCh)

		var reasoningBuf, contentBuf []rune
		var lastReasoningAt time.Time
		const reasoningSettleTime = 200 * time.Millisecond
		rawDone := false

		tick := time.NewTicker(smoothingDelay)
		defer tick.Stop()

		send := func(d llm.StreamDelta) bool {
			select {
			case deltaCh <- d:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			// Si todo terminó y no queda nada que emitir, salimos.
			if rawDone && len(reasoningBuf) == 0 && len(contentBuf) == 0 {
				return
			}

			select {
			case <-ctx.Done():
				return

			case raw, ok := <-rawCh:
				if !ok {
					rawDone = true
					rawCh = nil // evita re-seleccionar el case cerrado
					continue
				}
				if len(raw.ToolCalls) > 0 || raw.Role != "" {
					if !send(raw) {
						return
					}
				}
				if raw.ReasoningContent != "" {
					lastReasoningAt = time.Now()
					reasoningBuf = append(reasoningBuf, []rune(raw.ReasoningContent)...)
				}
				if raw.Content != "" {
					contentBuf = append(contentBuf, []rune(raw.Content)...)
				}

			case <-tick.C:
				// Prioridad 1: si hay reasoning pendiente, emitir un rune.
				if len(reasoningBuf) > 0 {
					r := reasoningBuf[0]
					reasoningBuf = reasoningBuf[1:]
					if !send(llm.StreamDelta{ReasoningContent: string(r)}) {
						return
					}
					continue
				}
				// Prioridad 2: emitir content solo si pasó settleTime sin
				// nuevo reasoning (o el rawCh ya cerró).
				canEmitContent := rawDone ||
					lastReasoningAt.IsZero() ||
					time.Since(lastReasoningAt) >= reasoningSettleTime
				if len(contentBuf) > 0 && canEmitContent {
					r := contentBuf[0]
					contentBuf = contentBuf[1:]
					if !send(llm.StreamDelta{Content: string(r)}) {
						return
					}
				}
			}
		}
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
