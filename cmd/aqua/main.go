package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"aqua/internal/agent"
	"aqua/internal/transport/discord"
	"aqua/internal/transport/terminal"
	"aqua/internal/transport/web"
)

func main() {
	mode := flag.String("mode", "terminal", "interfaz: terminal | discord | web")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	a, err := agent.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer a.MCP().Close()

	switch *mode {
	case "terminal", "console":
		terminal.Run(ctx, a)
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
		fmt.Fprintf(os.Stderr, "modo desconocido: %q (usar: terminal | discord | web)\n", *mode)
		os.Exit(1)
	}
}
