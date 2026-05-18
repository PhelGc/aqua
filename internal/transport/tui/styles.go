package tui

import "github.com/charmbracelet/lipgloss"

// Paleta. Tema oscuro fijo para v1.
var (
	colorAccent   = lipgloss.Color("#7DD3FC") // celeste (aqua-like)
	colorMuted    = lipgloss.Color("#94A3B8") // gris
	colorYou      = lipgloss.Color("#A78BFA") // violeta para mensajes del user
	colorAqua     = lipgloss.Color("#7DD3FC") // celeste para respuestas del agente
	colorTool     = lipgloss.Color("#FBBF24") // ámbar para tool-calls
	colorError    = lipgloss.Color("#F87171") // rojo suave
	colorBorder   = lipgloss.Color("#334155") // gris-azul para bordes
)

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent).
			Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(colorBorder)

	footerStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(colorBorder)

	inputStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 0)

	userPrefix = lipgloss.NewStyle().Bold(true).Foreground(colorYou).Render("you ")
	aquaPrefix = lipgloss.NewStyle().Bold(true).Foreground(colorAqua).Render("aqua ")
	toolStyle  = lipgloss.NewStyle().Foreground(colorTool)
	errorStyle = lipgloss.NewStyle().Foreground(colorError)
	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)
)
