package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"

	"aqua/internal/agent"
	"aqua/internal/events"
)

// lineKind clasifica una entrada del chat para renderizar con prefix/color.
type lineKind int

const (
	lineUser lineKind = iota
	lineAssistant
	lineTool
	lineError
	lineSystem
)

type chatLine struct {
	kind lineKind
	text string
	time time.Time
}

// state representa el estado de UI principal. Los modales (sesiones) los
// agregamos en el paso 4.
type state int

const (
	stateNormal        state = iota
	stateSending             // request en vuelo, input deshabilitado
	stateSessionsModal       // modal abierto, input ignora teclas
)

type model struct {
	agent   *agent.Agent
	sink    *events.FanoutSink
	eventCh <-chan events.Event // canal del subscriber, set en Init
	width   int
	height  int
	state   state

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	// chatView es source-of-truth para lo que se muestra. Se mantiene
	// separado del history del agente para no leerlo mientras SendMain
	// tiene el lock.
	chatView []chatLine

	// sessionsModal está poblado solo mientras state == stateSessionsModal.
	sessionsModal sessionsModal

	// completion es el popup de autocomplete de slash commands. Vive sobre
	// el input cuando el contenido empieza con "/".
	completion completion
}

const (
	headerHeight    = 2  // título + borde
	footerHeight    = 2  // hint + borde
	inputMinHeight  = 1  // 1 línea visible cuando está vacío
	inputMaxHeight  = 10 // tope antes de que aparezca scroll interno del textarea
	inputChromeRows = 2  // bordes del marco del input (top + bottom)
)

func newModel(a *agent.Agent, sink *events.FanoutSink) model {
	ta := textarea.New()
	ta.Placeholder = "Escribí un mensaje o /skill ..."
	ta.Focus()
	ta.Prompt = "│ "
	ta.CharLimit = 0 // sin límite
	ta.SetHeight(inputMinHeight)
	ta.ShowLineNumbers = false
	// Shift+Enter no es distinguible de Enter en la mayoría de terminales
	// (no manda código separado). Usamos ctrl+j (LF universal) y alt+enter
	// como alias para línea nueva.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")

	vp := viewport.New(0, 0) // dimensiones se setean al primer WindowSizeMsg

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return model{
		agent:    a,
		sink:     sink,
		state:    stateNormal,
		viewport: vp,
		input:    ta,
		spinner:  sp,
	}
}

// appendLine agrega una entrada al chatView y reflota el viewport hacia abajo.
// Si todavía no recibimos WindowSizeMsg (Width == 0), guardamos la línea y
// el primer layout() la renderizará con el ancho correcto.
func (m *model) appendLine(kind lineKind, text string) {
	m.chatView = append(m.chatView, chatLine{kind: kind, text: text, time: time.Now()})
	if m.viewport.Width == 0 {
		return
	}
	m.viewport.SetContent(renderChat(m.chatView, m.viewport.Width))
	m.viewport.GotoBottom()
}
