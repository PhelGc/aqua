package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	discordMaxMessageLen = 1900
	discordSessionPrefix = "discord-"
	discordRequestTimeout = 5 * time.Minute
)

type discordBot struct {
	agent      *agent
	allowedIDs map[string]bool

	convoMu sync.Mutex
	convos  map[string]*[]message
	busy    map[string]bool
}

func runDiscord(ctx context.Context, a *agent) error {
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

	bot := &discordBot{
		agent:      a,
		allowedIDs: allowedIDs,
		convos:     map[string]*[]message{},
		busy:       map[string]bool{},
	}
	dg.AddHandler(bot.onMessage)

	if err := dg.Open(); err != nil {
		return fmt.Errorf("conectando a Discord: %w", err)
	}
	defer dg.Close()

	toolN := len(a.mcp.tools())
	skillN := len(a.skills.list())
	fmt.Printf("aqua · modo: discord · usuarios autorizados: %d · %d tools · %d skills\n",
		len(allowedIDs), toolN, skillN)
	fmt.Println("ctrl+c para salir")

	<-ctx.Done()
	return nil
}

func (b *discordBot) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
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
		loaded, err := b.agent.sessions.load(sessionName)
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

	fmt.Printf("discord: %s → aqua: %s\n", m.Author.Username, truncateForLog(text, 80))

	stopTyping := keepTyping(ctx, s, m.ChannelID)
	defer stopTyping()

	reply, err := b.agent.send(ctx, history, sessionName, text)
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

func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
