package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"aqua/internal/agent"
)

// completionItem es una entrada en el popup de autocompletado de slash commands.
type completionItem struct {
	name        string // sin "/"
	description string // para mostrar al lado
}

// completion es el estado del popup. Está "abierto" cuando el input empieza
// con "/" y hay matches. El cursor recorre los matches.
type completion struct {
	visible bool
	items   []completionItem // filtrados por el query actual
	cursor  int
}

// builtinCommands son los slash-commands que el TUI maneja localmente.
// Aparecen en el autocomplete junto con las skills cargadas.
var builtinCommands = []completionItem{
	{name: "exit", description: "salir del TUI"},
	{name: "quit", description: "salir del TUI"},
	{name: "reset", description: "limpiar historial de la sesión actual"},
	{name: "clear", description: "limpiar solo la vista del chat"},
}

// refresh recalcula items y visibilidad según el input actual. Lo llama Update
// cada vez que el contenido del textarea cambia.
//
// Reglas:
//   - input vacío o sin "/" como primer carácter → invisible.
//   - "/" solo → muestra todo.
//   - "/algo" → fuzzy-prefix match contra los nombres.
//   - sin matches → invisible.
func (c *completion) refresh(input string, a *agent.Agent) {
	input = strings.TrimLeft(input, " ")
	if !strings.HasPrefix(input, "/") {
		c.visible = false
		c.items = nil
		c.cursor = 0
		return
	}
	// El query es lo que está después del slash y antes del primer espacio
	// (después del espacio ya son args del comando, no parte del nombre).
	rest := input[1:]
	if idx := strings.Index(rest, " "); idx >= 0 {
		// Ya hay un espacio → el usuario está tipeando argumentos, no
		// el nombre del comando. No mostramos popup.
		c.visible = false
		c.items = nil
		return
	}
	query := strings.ToLower(rest)

	all := append([]completionItem(nil), builtinCommands...)
	for _, s := range a.Skills().List() {
		all = append(all, completionItem{name: s.Name, description: s.Description})
	}

	matches := make([]completionItem, 0, len(all))
	for _, item := range all {
		if strings.HasPrefix(strings.ToLower(item.name), query) {
			matches = append(matches, item)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].name < matches[j].name })

	c.items = matches
	c.visible = len(matches) > 0
	if c.cursor >= len(matches) {
		c.cursor = 0
	}
}

// move ajusta el cursor en delta posiciones, con wraparound.
func (c *completion) move(delta int) {
	if !c.visible || len(c.items) == 0 {
		return
	}
	c.cursor = (c.cursor + delta + len(c.items)) % len(c.items)
}

// selected devuelve el item bajo el cursor. Llamarlo solo cuando visible.
func (c *completion) selected() completionItem {
	return c.items[c.cursor]
}

// view renderiza el popup en una caja. width/height son los del modelo;
// el popup se queda con ~width-2 y como mucho 1/3 de la altura disponible
// (con piso de 3 entradas para que sea usable en terminales chicas).
// Si no es visible devuelve "".
func (c *completion) view(width, height int) string {
	if !c.visible {
		return ""
	}
	maxVisible := height / 3
	if maxVisible < 3 {
		maxVisible = 3
	}
	if maxVisible > 8 {
		maxVisible = 8
	}
	start := 0
	if len(c.items) > maxVisible {
		// Ventana centrada en el cursor.
		start = c.cursor - maxVisible/2
		if start < 0 {
			start = 0
		}
		if start+maxVisible > len(c.items) {
			start = len(c.items) - maxVisible
		}
	}
	end := start + maxVisible
	if end > len(c.items) {
		end = len(c.items)
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		it := c.items[i]
		line := "/" + it.name
		if it.description != "" {
			line += "  " + truncate(it.description, 60)
		}
		if i == c.cursor {
			sb.WriteString(completionSelectedStyle.Render(line))
		} else {
			sb.WriteString(completionItemStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	body := strings.TrimRight(sb.String(), "\n")
	w := width - 2 // restamos el borde
	if w < 20 {
		w = 20
	}
	return completionFrameStyle.Width(w).Render(body)
}

var (
	completionFrameStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Padding(0, 1)

	completionItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E2E8F0"))

	completionSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#0F172A")).
				Background(colorAccent).
				Bold(true)
)
