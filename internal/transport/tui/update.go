package tui

import (
	"context"
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
		case "enter":
			if m.state == stateSending {
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

	case chatReplyMsg:
		m.state = stateNormal
		if msg.err != nil {
			m.appendLine(lineError, msg.err.Error())
		} else {
			body := msg.text
			if msg.artifact != "" {
				body = body + "\n(archivo: " + msg.artifact + ")"
			}
			m.appendLine(lineAssistant, body)
		}
		return m, nil
	}

	// Si está enviando, NO propagamos teclas al input (queda "congelado"
	// visualmente, pero podríamos mostrar el spinner haciendo tick).
	if m.state == stateSending {
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)
	} else {
		var inCmd tea.Cmd
		m.input, inCmd = m.input.Update(msg)
		cmds = append(cmds, inCmd)
	}

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
			m.state = stateSending
			return m, sendChat(context.Background(), m, rendered)
		}
	}

	m.appendLine(lineUser, text)
	m.state = stateSending
	// Sin timeout duro: el agent ya tiene timeout interno por tool-call.
	return m, sendChat(context.Background(), m, text)
}

// layout calcula tamaños de viewport e input según width/height actuales.
func (m *model) layout() {
	vpHeight := m.height - headerHeight - footerHeight - inputHeight
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpHeight
	m.input.SetWidth(m.width)
	// Re-render del chat ahora que sabemos el ancho.
	m.viewport.SetContent(renderChat(m.chatView, m.width))
	m.viewport.GotoBottom()
}
