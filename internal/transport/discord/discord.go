// Package discord implementa el bot de aqua sobre discordgo, con DM directo
// + slash commands. Una sesión por usuario, persistida en sessions/.
package discord

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"aqua/internal/agent"
	"aqua/internal/llm"
)

const (
	discordMaxMessageLen  = 1900
	discordSessionPrefix  = "discord-"
	discordRequestTimeout = 30 * time.Minute
	discordClearPageSize  = 100
	discordClearMaxPages  = 100
)

type bot struct {
	agent      *agent.Agent
	allowedIDs map[string]bool

	convoMu sync.Mutex
	convos  map[string]*[]llm.Message
	busy    map[string]bool
}

// Run levanta el bot Discord y bloquea hasta que se cancela el ctx.
func Run(ctx context.Context, a *agent.Agent) error {
	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	if token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN no está definida")
	}

	rawAllowed := strings.TrimSpace(os.Getenv("DISCORD_ALLOWED_USERS"))
	allowedIDs := map[string]bool{}
	for _, id := range strings.Split(rawAllowed, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			allowedIDs[id] = true
		}
	}
	if len(allowedIDs) == 0 {
		return fmt.Errorf("DISCORD_ALLOWED_USERS está vacía: nadie podría DM al bot")
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("creando cliente Discord: %w", err)
	}
	dg.Identify.Intents = discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	b := &bot{
		agent:      a,
		allowedIDs: allowedIDs,
		convos:     map[string]*[]llm.Message{},
		busy:       map[string]bool{},
	}
	dg.AddHandler(b.onMessage)
	dg.AddHandler(b.onInteraction)
	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		if err := b.registerCommands(s); err != nil {
			fmt.Fprintln(os.Stderr, "discord: error registrando comandos:", err)
			return
		}
		fmt.Printf("discord: comandos registrados (%d skills + 3 builtins)\n", len(a.Skills().List()))
	})

	if err := dg.Open(); err != nil {
		return fmt.Errorf("conectando a Discord: %w", err)
	}
	defer dg.Close()

	toolN := len(a.MCP().Tools())
	skillN := len(a.Skills().List())
	fmt.Printf("aqua · modo: discord · usuarios autorizados: %d · %d tools · %d skills\n",
		len(allowedIDs), toolN, skillN)
	fmt.Println("ctrl+c para salir")

	<-ctx.Done()
	return nil
}

func (b *bot) registerCommands(s *discordgo.Session) error {
	appID := s.State.User.ID
	guildID := strings.TrimSpace(os.Getenv("DISCORD_REGISTER_GUILD"))
	cmds := buildDiscordCommands(b.agent)
	empty := []*discordgo.ApplicationCommand{}

	if guildID != "" {
		if _, err := s.ApplicationCommandBulkOverwrite(appID, "", empty); err != nil {
			fmt.Fprintln(os.Stderr, "discord: warning limpiando comandos globales:", err)
		} else {
			fmt.Println("discord: comandos globales previos eliminados")
		}
	} else {
		guilds, err := s.UserGuilds(200, "", "", false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "discord: warning listando guilds:", err)
		} else {
			cleared := 0
			for _, g := range guilds {
				if _, err := s.ApplicationCommandBulkOverwrite(appID, g.ID, empty); err != nil {
					fmt.Fprintf(os.Stderr, "discord: warning limpiando guild %s: %v\n", g.ID, err)
					continue
				}
				cleared++
			}
			if cleared > 0 {
				fmt.Printf("discord: comandos guild-specific previos eliminados en %d server(s)\n", cleared)
			}
		}
	}

	if _, err := s.ApplicationCommandBulkOverwrite(appID, guildID, cmds); err != nil {
		return err
	}

	target := "global"
	if guildID != "" {
		target = "guild " + guildID
	}
	fmt.Printf("discord: %d comandos registrados en %s\n", len(cmds), target)
	return nil
}

func buildDiscordCommands(a *agent.Agent) []*discordgo.ApplicationCommand {
	allContexts := []discordgo.InteractionContextType{
		discordgo.InteractionContextGuild,
		discordgo.InteractionContextBotDM,
		discordgo.InteractionContextPrivateChannel,
	}
	integrationTypes := []discordgo.ApplicationIntegrationType{
		discordgo.ApplicationIntegrationGuildInstall,
	}

	cmds := []*discordgo.ApplicationCommand{
		{
			Name:             "reset",
			Description:      "Limpia el historial de tu conversación con aqua",
			Contexts:         &allContexts,
			IntegrationTypes: &integrationTypes,
		},
		{
			Name:             "clear",
			Description:      "Borra los mensajes del bot en este DM y limpia el historial",
			Contexts:         &allContexts,
			IntegrationTypes: &integrationTypes,
		},
		{
			Name:             "tools",
			Description:      "Lista las MCP tools disponibles",
			Contexts:         &allContexts,
			IntegrationTypes: &integrationTypes,
		},
		{
			Name:             "skills",
			Description:      "Lista las skills disponibles",
			Contexts:         &allContexts,
			IntegrationTypes: &integrationTypes,
		},
	}

	for _, sk := range a.Skills().List() {
		desc := sk.Description
		if desc == "" {
			desc = "Ejecuta la skill " + sk.Name
		}
		if len(desc) > 100 {
			desc = desc[:97] + "..."
		}
		cmds = append(cmds, &discordgo.ApplicationCommand{
			Name:             sk.Name,
			Description:      desc,
			Contexts:         &allContexts,
			IntegrationTypes: &integrationTypes,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "input",
					Description: "Argumentos para la skill",
					Required:    false,
				},
			},
		})
	}

	return cmds
}

func (b *bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	var userID, username string
	if i.User != nil {
		userID = i.User.ID
		username = i.User.Username
	} else if i.Member != nil && i.Member.User != nil {
		userID = i.Member.User.ID
		username = i.Member.User.Username
	} else {
		return
	}

	if !b.allowedIDs[userID] {
		fmt.Fprintf(os.Stderr, "discord: slash rechazado de %s (%s)\n", username, userID)
		respondEphemeral(s, i, "No autorizado.")
		return
	}

	data := i.ApplicationCommandData()
	name := data.Name
	var input string
	for _, opt := range data.Options {
		if opt.Name == "input" {
			input = opt.StringValue()
		}
	}

	switch name {
	case "reset":
		b.convoMu.Lock()
		sessionName := discordSessionPrefix + userID
		empty := []llm.Message{}
		b.convos[userID] = &empty
		b.convoMu.Unlock()
		_ = b.agent.Sessions().Save(sessionName, empty)
		respondEphemeral(s, i, "(historial limpio)")
		return
	case "clear":
		b.handleClear(s, i, userID)
		return
	case "tools":
		tools := b.agent.MCP().Tools()
		if len(tools) == 0 {
			respondEphemeral(s, i, "(sin tools cargadas)")
			return
		}
		var sb strings.Builder
		for _, t := range tools {
			fmt.Fprintf(&sb, "- **%s** — %s\n", t.Function.Name, t.Function.Description)
		}
		respondEphemeral(s, i, truncateForDiscord(sb.String()))
		return
	case "skills":
		list := b.agent.Skills().List()
		if len(list) == 0 {
			respondEphemeral(s, i, "(sin skills cargadas)")
			return
		}
		var sb strings.Builder
		for _, sk := range list {
			d := sk.Description
			if d == "" {
				d = "(sin descripción)"
			}
			fmt.Fprintf(&sb, "- **/%s** — %s\n", sk.Name, d)
		}
		respondEphemeral(s, i, truncateForDiscord(sb.String()))
		return
	}

	rendered, ok := b.agent.Skills().Render(name, input)
	if !ok {
		respondEphemeral(s, i, "Comando desconocido: /"+name)
		return
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "discord: error defer:", err)
		return
	}

	b.convoMu.Lock()
	if b.busy[userID] {
		b.convoMu.Unlock()
		editInteractionResponse(s, i, "Tu mensaje anterior aún está procesándose. Aguardá.")
		return
	}
	history, found := b.convos[userID]
	if !found {
		sessionName := discordSessionPrefix + userID
		loaded, err := b.agent.Sessions().Load(sessionName)
		if err != nil {
			b.convoMu.Unlock()
			editInteractionResponse(s, i, "Error cargando sesión: "+err.Error())
			return
		}
		history = &loaded
		b.convos[userID] = history
	}
	b.busy[userID] = true
	b.convoMu.Unlock()

	defer func() {
		b.convoMu.Lock()
		delete(b.busy, userID)
		b.convoMu.Unlock()
	}()

	sessionName := discordSessionPrefix + userID
	reqCtx, cancel := context.WithTimeout(context.Background(), discordRequestTimeout)
	defer cancel()

	fmt.Printf("discord slash: %s /%s %s\n", username, name, agent.TruncateForLog(input, 60))

	reply, artifact, err := b.agent.SendAndDispatch(reqCtx, history, sessionName, rendered)
	if err != nil {
		editInteractionResponse(s, i, "Error: "+err.Error())
		return
	}

	chunks := splitForDiscord(reply, discordMaxMessageLen)
	if len(chunks) == 0 {
		chunks = []string{"(sin contenido)"}
	}
	editInteractionResponse(s, i, chunks[0])
	for _, chunk := range chunks[1:] {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{Content: chunk})
		if err != nil {
			fmt.Fprintln(os.Stderr, "discord: error followup:", err)
		}
	}
	if artifact != "" {
		if err := sendInteractionFile(s, i, artifact); err != nil {
			fmt.Fprintln(os.Stderr, "discord: error adjuntando artifact:", err)
		}
	}
}

func (b *bot) handleClear(s *discordgo.Session, i *discordgo.InteractionCreate, userID string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "discord: error defer clear:", err)
		return
	}

	b.convoMu.Lock()
	sessionName := discordSessionPrefix + userID
	empty := []llm.Message{}
	b.convos[userID] = &empty
	b.convoMu.Unlock()
	_ = b.agent.Sessions().Save(sessionName, empty)

	deleted := 0
	scanned := 0
	if i.ChannelID != "" {
		before := ""
		for page := 0; page < discordClearMaxPages; page++ {
			msgs, err := s.ChannelMessages(i.ChannelID, discordClearPageSize, before, "", "")
			if err != nil {
				fmt.Fprintln(os.Stderr, "discord: error paginando historial:", err)
				break
			}
			if len(msgs) == 0 {
				break
			}
			scanned += len(msgs)
			before = msgs[len(msgs)-1].ID
			for _, msg := range msgs {
				if msg.Author == nil || msg.Author.ID != s.State.User.ID {
					continue
				}
				if err := s.ChannelMessageDelete(i.ChannelID, msg.ID); err == nil {
					deleted++
				}
			}
		}
	}

	editInteractionResponse(s, i, fmt.Sprintf("Limpieza: %d mensaje(s) del bot borrados (de %d revisados) + historial reseteado", deleted, scanned))
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func editInteractionResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
	if err != nil {
		fmt.Fprintln(os.Stderr, "discord: error edit:", err)
	}
}

func truncateForDiscord(s string) string {
	if len(s) <= discordMaxMessageLen {
		return s
	}
	return s[:discordMaxMessageLen-3] + "..."
}

// sendInteractionFile adjunta el archivo del path como followup de una interaction.
func sendInteractionFile(s *discordgo.Session, i *discordgo.InteractionCreate, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("abriendo %s: %w", path, err)
	}
	defer f.Close()
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Files: []*discordgo.File{
			{Name: filepath.Base(path), Reader: f},
		},
	})
	return err
}

// sendChannelFile adjunta el archivo del path a un canal (uso desde DMs).
func sendChannelFile(s *discordgo.Session, channelID, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("abriendo %s: %w", path, err)
	}
	defer f.Close()
	_, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Files: []*discordgo.File{
			{Name: filepath.Base(path), Reader: f},
		},
	})
	return err
}

func (b *bot) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.ID == s.State.User.ID || m.Author.Bot {
		return
	}
	if m.GuildID != "" {
		return
	}
	if !b.allowedIDs[m.Author.ID] {
		fmt.Fprintf(os.Stderr, "discord: DM rechazado de %s (%s)\n", m.Author.Username, m.Author.ID)
		return
	}
	text := strings.TrimSpace(m.Content)
	if text == "" {
		return
	}

	b.convoMu.Lock()
	if b.busy[m.Author.ID] {
		b.convoMu.Unlock()
		_, _ = s.ChannelMessageSend(m.ChannelID, "Tu mensaje anterior aún está procesándose. Aguardá un momento.")
		return
	}
	history, ok := b.convos[m.Author.ID]
	if !ok {
		sessionName := discordSessionPrefix + m.Author.ID
		loaded, err := b.agent.Sessions().Load(sessionName)
		if err != nil {
			b.convoMu.Unlock()
			fmt.Fprintln(os.Stderr, "discord: error cargando sesión:", err)
			_, _ = s.ChannelMessageSend(m.ChannelID, "Error cargando sesión: "+err.Error())
			return
		}
		history = &loaded
		b.convos[m.Author.ID] = history
	}
	b.busy[m.Author.ID] = true
	b.convoMu.Unlock()

	defer func() {
		b.convoMu.Lock()
		delete(b.busy, m.Author.ID)
		b.convoMu.Unlock()
	}()

	sessionName := discordSessionPrefix + m.Author.ID
	ctx, cancel := context.WithTimeout(context.Background(), discordRequestTimeout)
	defer cancel()

	fmt.Printf("discord: %s → aqua: %s\n", m.Author.Username, agent.TruncateForLog(text, 80))

	stopTyping := keepTyping(ctx, s, m.ChannelID)
	defer stopTyping()

	reply, artifact, err := b.agent.SendAndDispatch(ctx, history, sessionName, text)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discord: error en send:", err)
		_, _ = s.ChannelMessageSend(m.ChannelID, "Error: "+err.Error())
		return
	}

	for _, chunk := range splitForDiscord(reply, discordMaxMessageLen) {
		if _, err := s.ChannelMessageSend(m.ChannelID, chunk); err != nil {
			fmt.Fprintln(os.Stderr, "discord: error enviando:", err)
			return
		}
	}
	if artifact != "" {
		if err := sendChannelFile(s, m.ChannelID, artifact); err != nil {
			fmt.Fprintln(os.Stderr, "discord: error adjuntando artifact:", err)
		}
	}
}

func keepTyping(ctx context.Context, s *discordgo.Session, channelID string) func() {
	_ = s.ChannelTyping(channelID)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(7 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.ChannelTyping(channelID)
			}
		}
	}()
	return func() {
		close(done)
	}
}

func splitForDiscord(text string, maxLen int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"(sin contenido)"}
	}
	var out []string
	for len(text) > maxLen {
		cut := strings.LastIndex(text[:maxLen], "\n")
		if cut <= maxLen/4 {
			cut = strings.LastIndex(text[:maxLen], " ")
		}
		if cut <= 0 {
			cut = maxLen
		}
		out = append(out, strings.TrimRight(text[:cut], " \n"))
		text = strings.TrimLeft(text[cut:], " \n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

