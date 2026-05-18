package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"aqua/internal/agent"
	"aqua/internal/transport/discord"
	"aqua/internal/transport/tui"
	"aqua/internal/transport/web"
)

func main() {
	mode := flag.String("mode", "tui", "interfaz: tui | discord | web")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	a, err := agent.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	// Orden importa: schedulers en vuelo pueden estar usando MCP, así que
	// esperamos al scheduler antes de cerrar las sesiones MCP.
	defer a.MCP().Close()
	defer a.Scheduler().Shutdown()

	switch *mode {
	case "tui":
		if err := tui.Run(ctx, a); err != nil {
			fmt.Fprintln(os.Stderr, "tui:", err)
			os.Exit(1)
		}
	case "discord":
		if err := discord.Run(ctx, a); err != nil {
			fmt.Fprintln(os.Stderr, "discord:", err)
			os.Exit(1)
		}
	case "ui", "web":
		if err := web.Run(ctx, a); err != nil {
			fmt.Fprintln(os.Stderr, "ui:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "modo desconocido: %q (usar: tui | discord | web)\n", *mode)
		os.Exit(1)
	}
}
