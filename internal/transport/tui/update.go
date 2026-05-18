package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"aqua/internal/events"
)

// initEventLoopMsg se manda una sola vez al arrancar para que el Update
// pueda suscribirse al sink (necesita modificar el modelo, cosa que Init no
// puede hacer porque devuelve solo un Cmd).
type initEventLoopMsg struct{}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.input.Focus(),
		func() tea.Msg { return initEventLoopMsg{} },
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		// statePaused: cualquier tecla vuelve al alt-screen. Ctrl+C sigue
		// saliendo del TUI por las dudas.
		if m.state == statePaused {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			m.state = stateNormal
			return m, tea.EnterAltScreen
		}

		// Ctrl+P entra a statePaused: sale del alt-screen y dumpea el chat
		// como texto plano a stdout para que el usuario pueda seleccionar
		// con mouse y copiar normalmente.
		if msg.String() == "ctrl+p" {
			m.state = statePaused
			return m, tea.Sequence(
				tea.ExitAltScreen,
				func() tea.Msg {
					m.dumpChatPlain()
					return nil
				},
			)
		}

		// Modal abierto: las teclas van al modal, salvo ctrl+c global.
		if m.state == stateSessionsModal {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var (
				action modalAction
				cmd    tea.Cmd
			)
			m.sessionsModal, action, cmd = m.sessionsModal.update(msg, m.agent)
			switch action {
			case modalActionClose:
				m.state = stateNormal
			case modalActionSwitched:
				m.state = stateNormal
				// Limpiamos la vista y mostramos cuántos mensajes trae la nueva.
				m.chatView = nil
				m.appendLine(lineSystem, fmt.Sprintf("sesión: %s · %d mensajes en disco",
					m.agent.Sessions().Current(), m.agent.HistoryLen()))
			}
			return m, cmd
		}

		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit
		case "ctrl+s":
			if m.state == stateSending {
				return m, nil
			}
			m.state = stateSessionsModal
			m.sessionsModal = newSessionsModal(m.agent)
			return m, nil
		}

		// Si el popup de completion está abierto, interceptamos las teclas
		// de navegación antes de que lleguen al textarea o al submit.
		if m.completion.visible {
			switch msg.String() {
			case "up":
				m.completion.move(-1)
				return m, nil
			case "down":
				m.completion.move(1)
				return m, nil
			case "tab", "enter":
				sel := m.completion.selected()
				m.input.SetValue("/" + sel.name + " ")
				m.input.CursorEnd()
				m.completion.refresh(m.input.Value(), m.agent)
				m.layout()
				return m, nil
			case "esc":
				m.completion.visible = false
				return m, nil
			}
		}

		// Esc o Ctrl+X durante un send cancela el request en vuelo.
		if m.state == stateSending && (msg.String() == "esc" || msg.String() == "ctrl+x") {
			if m.cancelReq != nil {
				m.cancelReq()
			}
			return m, nil
		}

		// Enter sin popup → submit normal.
		if msg.String() == "enter" {
			if m.state == stateSending {
				// No bloqueamos el typing pero sí avisamos que está ocupado.
				// El texto del input queda intacto para que el usuario lo
				// mande cuando termine el request actual.
				m.appendLine(lineSystem, "esperá a que termine el request actual (esc cancela)")
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			return m.submit(text)
		}

	case initEventLoopMsg:
		ch, _, _ := m.sink.Subscribe()
		m.eventCh = ch
		return m, waitForEvent(ch)

	case eventMsg:
		// Re-encolar la espera del próximo evento. Si el canal se cerró
		// (m.eventCh == nil), no reagendar.
		var next tea.Cmd
		if m.eventCh != nil {
			next = waitForEvent(m.eventCh)
		}
		if line, ok := renderEvent(events.Event(msg)); ok {
			m.appendLine(lineTool, line)
		}
		return m, next

	case streamDeltaMsg:
		// reasoning_content se muestra en vivo en una línea lineThinkingInProgress.
		// content va a la línea assistant. Antes del primer content del turn,
		// colapsamos el thinking si existía (switch visual "pensando" → "respondiendo").
		if msg.ReasoningContent != "" {
			m.addThinkingChunk(msg.ReasoningContent)
		}
		if msg.Content != "" {
			if !hasAssistantLine(m.chatView) {
				m.thinkingTransition()
			}
			m.appendOrExtend(lineAssistant, msg.Content)
		}
		return m, waitForDelta(m.deltaCh)

	case streamDoneMsg:
		// El canal de deltas terminó de drenarse. Si ya teníamos el reply
		// pendiente, ahora sí cerramos el turn. Si no, cuando llegue.
		m.deltaCh = nil
		if m.pendingReply != nil {
			rep := *m.pendingReply
			m.pendingReply = nil
			return m, m.finalizeReply(rep)
		}
		return m, nil

	case chatReplyMsg:
		// Si todavía hay deltas pendientes (canal abierto), esperamos a que
		// drene antes de cerrar. Guardamos el reply y volvemos.
		if m.deltaCh != nil {
			rep := msg
			m.pendingReply = &rep
			return m, nil
		}
		return m, m.finalizeReply(msg)
	}

	// Input SIEMPRE recibe teclas (incluso durante stateSending) para que
	// el usuario pueda ir escribiendo su próximo mensaje mientras la TUI
	// renderiza la respuesta del LLM. El spinner del footer también sigue
	// haciendo tick.
	if m.state == stateSending {
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)
	}
	prevLines := m.input.LineCount()
	var inCmd tea.Cmd
	m.input, inCmd = m.input.Update(msg)
	cmds = append(cmds, inCmd)
	if m.input.LineCount() != prevLines {
		m.layout()
	}
	m.completion.refresh(m.input.Value(), m.agent)

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

// submit procesa la entrada del usuario:
//   - slash commands builtin (/exit, /reset, /clear): se ejecutan localmente.
//   - skills (/<nombre> [args]): se renderean y se mandan al agente.
//   - texto plano: va al agente tal cual.
//
// En todos los casos limpia el input y muestra la línea del usuario.
func (m model) submit(text string) (model, tea.Cmd) {
	m.input.Reset()
	m.layout() // input vuelve a su altura mínima tras vaciar

	if strings.HasPrefix(text, "/") {
		cmd, args, _ := strings.Cut(text[1:], " ")
		args = strings.TrimSpace(args)
		switch cmd {
		case "exit", "quit":
			return m, tea.Quit
		case "reset":
			if err := m.agent.Reset(); err != nil {
				m.appendLine(lineError, "reset: "+err.Error())
			} else {
				m.appendLine(lineSystem, "historial limpio")
			}
			return m, nil
		case "clear":
			// /clear limpia solo la vista, no toca el historial real.
			m.chatView = nil
			m.viewport.SetContent(renderChat(m.chatView, m.viewport.Width))
			return m, nil
		default:
			rendered, ok := m.agent.Skills().Render(cmd, args)
			if !ok {
				m.appendLine(lineError, "comando desconocido: /"+cmd)
				return m, nil
			}
			m.appendLine(lineUser, text)
			return m.startStreaming(rendered)
		}
	}

	m.appendLine(lineUser, text)
	return m.startStreaming(text)
}

// startStreaming arma el ctx cancelable, lanza el send en background y
// devuelve los Cmds que escuchan el stream de deltas y la respuesta final.
func (m model) startStreaming(prompt string) (model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelReq = cancel
	m.state = stateSending
	m.pendingReply = nil
	deltaCh, replyCh := startSend(ctx, m, prompt)
	m.deltaCh = deltaCh
	return m, tea.Batch(
		waitForDelta(deltaCh),
		waitForReply(replyCh),
	)
}

// finalizeReply cierra el turn: colapsa el thinking, renderiza errores o
// pega el artifact, y devuelve el state a normal. Se llama cuando AMBOS
// canales (deltas y reply) terminaron, así garantizamos que ningún delta
// tardío aparezca después del assistant final.
func (m *model) finalizeReply(msg chatReplyMsg) tea.Cmd {
	m.state = stateNormal
	m.cancelReq = nil
	m.closeThinking()
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			m.appendLine(lineSystem, "request cancelado")
		} else {
			m.appendLine(lineError, msg.err.Error())
		}
		return nil
	}
	// Si veníamos streameando content, la última línea assistant ya tiene
	// el texto completo. Solo agregamos el artifact si hay, o creamos la
	// línea de cero cuando no hubo stream (fallback no-SSE).
	if msg.artifact != "" {
		m.appendOrExtend(lineAssistant, "\n(archivo: "+msg.artifact+")")
	}
	if !hasAssistantLine(m.chatView) {
		m.appendLine(lineAssistant, msg.text)
	}
	return nil
}

// layout calcula tamaños de viewport e input según width/height actuales.
// El input crece con el contenido (entre inputMinHeight e inputMaxHeight);
// el viewport se queda con lo que sobre arriba.
func (m *model) layout() {
	// Altura interna del textarea según líneas actuales (cuenta wrap).
	contentLines := m.input.LineCount()
	if contentLines < inputMinHeight {
		contentLines = inputMinHeight
	}
	if contentLines > inputMaxHeight {
		contentLines = inputMaxHeight
	}
	m.input.SetHeight(contentLines)

	inputBlockHeight := contentLines + inputChromeRows
	vpHeight := m.height - headerHeight - footerHeight - inputBlockHeight
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpHeight
	// Restamos el chrome (bordes) del ancho disponible para el textarea.
	m.input.SetWidth(m.width - inputChromeRows)
	// Re-render del chat ahora que sabemos el ancho.
	m.viewport.SetContent(renderChat(m.chatView, m.width))
	m.viewport.GotoBottom()
}
