package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"

	"aqua/internal/agent"
	"aqua/internal/events"
	"aqua/internal/llm"
)

// lineKind clasifica una entrada del chat para renderizar con prefix/color.
type lineKind int

const (
	lineUser lineKind = iota
	lineAssistant
	lineTool
	lineError
	lineSystem
	lineThinking        // reasoning_content del LLM en gris cursiva
	lineThinkingClosed  // bloque colapsado "» thinking: ..." tras terminar
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
	agent     *agent.Agent
	sink      *events.FanoutSink
	eventCh   <-chan events.Event // canal del subscriber, set en Init
	deltaCh   <-chan llm.StreamDelta // deltas del send en vuelo, nil entre sends
	cancelReq context.CancelFunc  // cancela el request en vuelo (esc/ctrl+x)
	width     int
	height    int
	state     state

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
	m.refreshViewport()
}

// appendOrExtend busca la última línea del mismo kind y le concatena text.
// Si no existe (o la última no es de ese kind), agrega una nueva. Pensado
// para acumular deltas del stream sin spamear líneas separadas.
func (m *model) appendOrExtend(kind lineKind, text string) {
	if n := len(m.chatView); n > 0 && m.chatView[n-1].kind == kind {
		m.chatView[n-1].text += text
	} else {
		m.chatView = append(m.chatView, chatLine{kind: kind, text: text, time: time.Now()})
	}
	m.refreshViewport()
}

// collapseThinking convierte la última línea lineThinking en lineThinkingClosed
// con el texto truncado. Se llama al terminar el stream para no dejar el
// razonamiento expandido para siempre.
func (m *model) collapseThinking() {
	for i := len(m.chatView) - 1; i >= 0; i-- {
		if m.chatView[i].kind == lineThinking {
			m.chatView[i].kind = lineThinkingClosed
			m.chatView[i].text = collapseSnippet(m.chatView[i].text)
			break
		}
		// Si ya pasamos por una línea assistant, el thinking ya quedó cerrado
		// antes (turno anterior); no buscamos más atrás.
		if m.chatView[i].kind == lineAssistant {
			break
		}
	}
	m.refreshViewport()
}

// hasAssistantLine devuelve true si las últimas líneas tras el último user
// incluyen ya una línea assistant. Sirve para decidir si necesitamos crear
// la respuesta de cero (fallback no-stream) o ya viene del stream.
func hasAssistantLine(lines []chatLine) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		switch lines[i].kind {
		case lineAssistant:
			return true
		case lineUser:
			return false
		}
	}
	return false
}

// collapseSnippet toma el thinking completo y devuelve una primera línea
// representativa: hasta el primer "\n" o 80 chars, lo que llegue antes.
func collapseSnippet(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	const maxLen = 80
	if len([]rune(s)) > maxLen {
		s = string([]rune(s)[:maxLen]) + "…"
	}
	return s
}

// refreshViewport re-renderiza el chatView en el viewport. Es no-op si el
// ancho todavía no llegó (lo dispara el primer WindowSizeMsg).
func (m *model) refreshViewport() {
	if m.viewport.Width == 0 {
		return
	}
	m.viewport.SetContent(renderChat(m.chatView, m.viewport.Width))
	m.viewport.GotoBottom()
}
