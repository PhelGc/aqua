// Package terminal implementa el REPL interactivo de aqua sobre stdin/stdout.
package terminal

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"aqua/internal/agent"
	"aqua/internal/skills"
)

// Run levanta el REPL hasta que el usuario pide /exit o cierra stdin.
func Run(ctx context.Context, a *agent.Agent) {
	personalityStatus := "sin personalidad"
	if p := a.Personality(); p != "" {
		personalityStatus = fmt.Sprintf("personalidad: %d chars", len(p))
	}
	toolStatus := "sin tools"
	if n := len(a.MCP().Tools()); n > 0 {
		toolStatus = fmt.Sprintf("%d tools de %d servidores MCP", n, a.MCP().Sessions())
	}
	skillStatus := "sin skills"
	if n := len(a.Skills().List()); n > 0 {
		skillStatus = fmt.Sprintf("%d skills", n)
	}
	fmt.Printf("aqua · modelo: %s · %s · %s · %s · sesión: %s (%d mensajes)\n",
		a.Model(), personalityStatus, toolStatus, skillStatus, a.Sessions().Current(), len(a.History()))
	fmt.Println("comandos: /exit, /reset, /tools, /skills [reload], /sessions, /<skill> [args]")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			cmd, args, _ := strings.Cut(input[1:], " ")
			args = strings.TrimSpace(args)
			switch cmd {
			case "exit":
				return
			case "reset":
				if err := a.Reset(); err != nil {
					fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión:", err)
				}
				fmt.Println("(historial limpio)")
				continue
			case "sessions":
				handleSessions(a, args)
				continue
			case "tools":
				tools := a.MCP().Tools()
				if len(tools) == 0 {
					fmt.Println("(sin tools cargadas)")
				} else {
					for _, t := range tools {
						fmt.Printf("- %s: %s\n", t.Function.Name, t.Function.Description)
					}
				}
				continue
			case "skills":
				if args == "reload" {
					reloaded, err := skills.Load()
					if err != nil {
						fmt.Fprintln(os.Stderr, "error recargando skills:", err)
						continue
					}
					a.SetSkills(reloaded)
					fmt.Printf("(recargadas %d skills)\n", len(reloaded.List()))
					continue
				}
				list := a.Skills().List()
				if len(list) == 0 {
					fmt.Println("(sin skills cargadas)")
				} else {
					for _, s := range list {
						desc := s.Description
						if desc == "" {
							desc = "(sin descripción)"
						}
						fmt.Printf("- /%s: %s\n", s.Name, desc)
					}
				}
				continue
			default:
				rendered, ok := a.Skills().Render(cmd, args)
				if !ok {
					fmt.Fprintf(os.Stderr, "comando desconocido: /%s\n", cmd)
					continue
				}
				input = rendered
			}
		}

		reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Minute)
		reply, artifact, err := a.SendAndDispatch(reqCtx, a.HistoryPtr(), a.Sessions().Current(), input)
		reqCancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Println(reply)
		if artifact != "" {
			fmt.Printf("(archivo: %s)\n", artifact)
		}
		fmt.Println()
	}
}

func handleSessions(a *agent.Agent, args string) {
	sub, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch sub {
	case "":
		names, err := a.Sessions().List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error listando sesiones:", err)
			return
		}
		if len(names) == 0 {
			fmt.Printf("(sin sesiones guardadas; actual: %s)\n", a.Sessions().Current())
			return
		}
		for _, n := range names {
			marker := "  "
			if n == a.Sessions().Current() {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, n)
		}
	case "new":
		if rest == "" {
			fmt.Fprintln(os.Stderr, "uso: /sessions new <nombre>")
			return
		}
		if err := a.Sessions().Save(a.Sessions().Current(), a.History()); err != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión actual:", err)
		}
		if err := a.Sessions().SwitchTo(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		a.SetHistory(nil)
		if err := a.Sessions().Save(rest, a.History()); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		fmt.Printf("(sesión actual: %s)\n", rest)
	case "load":
		if rest == "" {
			fmt.Fprintln(os.Stderr, "uso: /sessions load <nombre>")
			return
		}
		if err := a.Sessions().Save(a.Sessions().Current(), a.History()); err != nil {
			fmt.Fprintln(os.Stderr, "warning: no se pudo guardar sesión actual:", err)
		}
		history, err := a.Sessions().Load(rest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		if err := a.Sessions().SwitchTo(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		a.SetHistory(history)
		fmt.Printf("(sesión actual: %s · %d mensajes)\n", rest, len(history))
	case "delete":
		if rest == "" {
			fmt.Fprintln(os.Stderr, "uso: /sessions delete <nombre>")
			return
		}
		if err := a.Sessions().Delete(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return
		}
		fmt.Printf("(borrada: %s)\n", rest)
	default:
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %s\nuso: /sessions [new|load|delete] <nombre>\n", sub)
	}
}
