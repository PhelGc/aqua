package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"aqua/internal/agent"
)

// sessionsMode es el sub-estado del modal: navegando, creando nueva,
// o confirmando borrado.
type sessionsMode int

const (
	sessModeBrowse sessionsMode = iota
	sessModeCreating
	sessModeConfirmDelete
)

// sessionsModal encapsula toda la UI del modal /sessions.
type sessionsModal struct {
	mode     sessionsMode
	items    []sessionItem
	cursor   int
	current  string
	input    textinput.Model
	notice   string // mensaje al pie (error o info)
}

type sessionItem struct {
	name     string
	messages int
}

// newSessionsModal arma el modal leyendo el estado actual del agente.
func newSessionsModal(a *agent.Agent) sessionsModal {
	items := loadSessionItems(a)
	current := a.Sessions().Current()
	cursor := 0
	for i, it := range items {
		if it.name == current {
			cursor = i
			break
		}
	}

	ti := textinput.New()
	ti.Placeholder = "nombre (letras, números, . _ -)"
	ti.CharLimit = 64

	return sessionsModal{
		mode:    sessModeBrowse,
		items:   items,
		cursor:  cursor,
		current: current,
		input:   ti,
	}
}

// loadSessionItems lista sesiones del disco y agrega counts de mensajes.
// Si una sesión falla al cargar, va con count -1 (se muestra como "?").
func loadSessionItems(a *agent.Agent) []sessionItem {
	names, err := a.Sessions().List()
	if err != nil {
		return nil
	}
	// La sesión actual puede no estar persistida todavía (recién creada),
	// aseguramos que aparezca.
	cur := a.Sessions().Current()
	hasCur := false
	for _, n := range names {
		if n == cur {
			hasCur = true
			break
		}
	}
	if !hasCur && cur != "" {
		names = append(names, cur)
	}
	sort.Strings(names)

	out := make([]sessionItem, 0, len(names))
	for _, n := range names {
		count := -1
		if h, err := a.Sessions().Load(n); err == nil {
			count = len(h)
		}
		out = append(out, sessionItem{name: n, messages: count})
	}
	return out
}

// update procesa una tecla cuando el modal está abierto. Devuelve el modal
// modificado, una posible acción para el caller (load/closed) y cmds.
type modalAction int

const (
	modalActionNone modalAction = iota
	modalActionClose
	modalActionSwitched // el caller debe refrescar chatView desde el agente
)

func (sm sessionsModal) update(msg tea.KeyMsg, a *agent.Agent) (sessionsModal, modalAction, tea.Cmd) {
	switch sm.mode {
	case sessModeBrowse:
		return sm.updateBrowse(msg, a)
	case sessModeCreating:
		return sm.updateCreating(msg, a)
	case sessModeConfirmDelete:
		return sm.updateConfirm(msg, a)
	}
	return sm, modalActionNone, nil
}

func (sm sessionsModal) updateBrowse(msg tea.KeyMsg, a *agent.Agent) (sessionsModal, modalAction, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+s", "q":
		return sm, modalActionClose, nil
	case "up", "k":
		if sm.cursor > 0 {
			sm.cursor--
		}
		sm.notice = ""
	case "down", "j":
		if sm.cursor < len(sm.items)-1 {
			sm.cursor++
		}
		sm.notice = ""
	case "enter":
		if len(sm.items) == 0 {
			return sm, modalActionNone, nil
		}
		target := sm.items[sm.cursor].name
		if target == sm.current {
			return sm, modalActionClose, nil // ya estás en esa
		}
		if err := switchSession(a, target); err != nil {
			sm.notice = "error: " + err.Error()
			return sm, modalActionNone, nil
		}
		return sm, modalActionSwitched, nil
	case "n":
		sm.mode = sessModeCreating
		sm.input.Reset()
		sm.input.Focus()
		sm.notice = ""
		return sm, modalActionNone, textinput.Blink
	case "d":
		if len(sm.items) == 0 {
			return sm, modalActionNone, nil
		}
		target := sm.items[sm.cursor].name
		if target == sm.current {
			sm.notice = "no se puede borrar la sesión actual"
			return sm, modalActionNone, nil
		}
		sm.mode = sessModeConfirmDelete
		sm.notice = ""
		return sm, modalActionNone, nil
	}
	return sm, modalActionNone, nil
}

func (sm sessionsModal) updateCreating(msg tea.KeyMsg, a *agent.Agent) (sessionsModal, modalAction, tea.Cmd) {
	switch msg.String() {
	case "esc":
		sm.mode = sessModeBrowse
		sm.input.Blur()
		sm.notice = ""
		return sm, modalActionNone, nil
	case "enter":
		name := strings.TrimSpace(sm.input.Value())
		if name == "" {
			sm.notice = "nombre vacío"
			return sm, modalActionNone, nil
		}
		if err := createSession(a, name); err != nil {
			sm.notice = "error: " + err.Error()
			return sm, modalActionNone, nil
		}
		return sm, modalActionSwitched, nil
	}
	var cmd tea.Cmd
	sm.input, cmd = sm.input.Update(msg)
	return sm, modalActionNone, cmd
}

func (sm sessionsModal) updateConfirm(msg tea.KeyMsg, a *agent.Agent) (sessionsModal, modalAction, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		target := sm.items[sm.cursor].name
		if err := a.Sessions().Delete(target); err != nil {
			sm.notice = "error: " + err.Error()
			sm.mode = sessModeBrowse
			return sm, modalActionNone, nil
		}
		// Refrescamos la lista; el cursor puede haber quedado fuera.
		sm.items = loadSessionItems(a)
		if sm.cursor >= len(sm.items) {
			sm.cursor = len(sm.items) - 1
		}
		if sm.cursor < 0 {
			sm.cursor = 0
		}
		sm.mode = sessModeBrowse
		sm.notice = "borrada: " + target
	default:
		// cualquier otra tecla cancela
		sm.mode = sessModeBrowse
		sm.notice = "cancelado"
	}
	return sm, modalActionNone, nil
}

// switchSession persiste la sesión actual y carga la nueva. Devuelve error
// si algo falla; en éxito, el agent queda con history y current actualizados.
func switchSession(a *agent.Agent, target string) error {
	// Persistimos la actual con su history vigente (que la TUI ya tiene en
	// el agente vía SendMain).
	if err := a.Sessions().Save(a.Sessions().Current(), a.History()); err != nil {
		return fmt.Errorf("guardando actual: %w", err)
	}
	hist, err := a.Sessions().Load(target)
	if err != nil {
		return fmt.Errorf("cargando %s: %w", target, err)
	}
	if err := a.Sessions().SwitchTo(target); err != nil {
		return fmt.Errorf("switch: %w", err)
	}
	a.SetHistory(hist)
	return nil
}

// createSession persiste la actual, cambia a una nueva sesión vacía, y la
// guarda con history nil para que aparezca en el listado.
func createSession(a *agent.Agent, name string) error {
	if err := a.Sessions().Save(a.Sessions().Current(), a.History()); err != nil {
		return fmt.Errorf("guardando actual: %w", err)
	}
	if err := a.Sessions().SwitchTo(name); err != nil {
		return fmt.Errorf("switch: %w", err)
	}
	a.SetHistory(nil)
	if err := a.Sessions().Save(name, nil); err != nil {
		return fmt.Errorf("guardando nueva: %w", err)
	}
	return nil
}

// view renderiza el modal como un bloque centrado. width/height son las
// dimensiones de la pantalla; el modal se acota a ~60 cols pero más chico
// si la terminal no llega, y scrollea verticalmente cuando hay más sesiones
// que filas disponibles.
func (sm sessionsModal) view(width, height int) string {
	w := 60
	if width < w+4 {
		w = width - 4
	}
	if w < 30 {
		w = 30
	}

	// Reservamos espacio para título, separadores, hint y borde:
	// título (1) + blank (1) + blank-before-hint (1) + hint (1-3 líneas) + borde (2) + padding (2) ≈ 10.
	maxListRows := height - 10
	if maxListRows < 3 {
		maxListRows = 3
	}

	var body strings.Builder
	body.WriteString(modalTitleStyle.Render("Sesiones"))
	body.WriteString("\n\n")

	if len(sm.items) == 0 {
		body.WriteString(mutedStyle.Render("(sin sesiones guardadas)"))
	} else {
		// Ventana visible alrededor del cursor cuando la lista no entra entera.
		start, end := 0, len(sm.items)
		if len(sm.items) > maxListRows {
			start = sm.cursor - maxListRows/2
			if start < 0 {
				start = 0
			}
			if start+maxListRows > len(sm.items) {
				start = len(sm.items) - maxListRows
			}
			end = start + maxListRows
		}
		// Ancho útil del item (descontando padding del modal + borde).
		itemNameW := w - 14
		if itemNameW < 10 {
			itemNameW = 10
		}
		for i := start; i < end; i++ {
			it := sm.items[i]
			marker := "  "
			if it.name == sm.current {
				marker = "* "
			}
			line := fmt.Sprintf("%s%-*s %s",
				marker, itemNameW, truncate(it.name, itemNameW), countLabel(it.messages))
			if i == sm.cursor && sm.mode == sessModeBrowse {
				line = modalCursorStyle.Render(line)
			} else {
				line = modalItemStyle.Render(line)
			}
			body.WriteString(line)
			body.WriteString("\n")
		}
		// Indicador "hay más arriba/abajo" si la lista está scrolleada.
		if start > 0 || end < len(sm.items) {
			body.WriteString(mutedStyle.Render(fmt.Sprintf("(mostrando %d-%d de %d)", start+1, end, len(sm.items))))
			body.WriteString("\n")
		}
	}

	body.WriteString("\n")

	switch sm.mode {
	case sessModeBrowse:
		body.WriteString(mutedStyle.Render("↑↓/jk navegar · enter cargar · n nueva · d borrar · esc cerrar"))
	case sessModeCreating:
		body.WriteString(mutedStyle.Render("nueva sesión: "))
		body.WriteString("\n")
		body.WriteString(sm.input.View())
		body.WriteString("\n")
		body.WriteString(mutedStyle.Render("enter crear · esc cancelar"))
	case sessModeConfirmDelete:
		if len(sm.items) > 0 {
			target := sm.items[sm.cursor].name
			body.WriteString(errorStyle.Render(fmt.Sprintf("¿Borrar %q? ", target)))
			body.WriteString(mutedStyle.Render("[y/N]"))
		}
	}

	if sm.notice != "" {
		body.WriteString("\n")
		body.WriteString(mutedStyle.Render(sm.notice))
	}

	return modalFrameStyle.Width(w).Render(body.String())
}

func countLabel(n int) string {
	if n < 0 {
		return mutedStyle.Render("(? msgs)")
	}
	return mutedStyle.Render(fmt.Sprintf("(%d msgs)", n))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// Estilos del modal (lipgloss). Lo dejo acá adentro porque solo se usa para
// este modal; si en el futuro hay más modales, se mueve a styles.go.
var (
	modalFrameStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(1, 2)

	modalTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	modalItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E2E8F0"))

	modalCursorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#0F172A")).
				Background(colorAccent).
				Bold(true)
)
