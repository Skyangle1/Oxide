package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"kingmyralune/oxide-music-bot/internal/config"
	"kingmyralune/oxide-music-bot/internal/lavalink"
	"kingmyralune/oxide-music-bot/internal/models"
	"kingmyralune/oxide-music-bot/internal/utils"
)

type Bot struct {
	Session  *discordgo.Session
	Config   *config.Config
	Lavalink *lavalink.Client
}

// Global variables for the leveling system (moved from main)
var (
	UserStats  = make(map[string]*models.UserStats)
	StatsMutex sync.RWMutex

	// Rate limiter
	RateLimiterInstance = &RateLimiter{
		requests: make(map[string][]time.Time),
	}

	// Audio State (Moved from audio package)
	GuildContexts  = make(map[string]*models.GuildContext)
	AudioMutex     sync.RWMutex
	RoomGuardMode  = make(map[string]bool)
	GuardModeMutex sync.RWMutex
)

// RateLimiter tracks user requests to prevent abuse
type RateLimiter struct {
	requests map[string][]time.Time
	mutex    sync.RWMutex
}

// AddRequest adds a request timestamp for a user
func (rl *RateLimiter) AddRequest(userID string) {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()

	if rl.requests[userID] == nil {
		rl.requests[userID] = make([]time.Time, 0)
	}

	rl.requests[userID] = append(rl.requests[userID], now)

	cutoff := now.Add(-1 * time.Minute)
	filtered := make([]time.Time, 0)
	for _, reqTime := range rl.requests[userID] {
		if reqTime.After(cutoff) {
			filtered = append(filtered, reqTime)
		}
	}
	rl.requests[userID] = filtered
}

// IsLimited checks if a user has exceeded the rate limit
func (rl *RateLimiter) IsLimited(userID string) bool {
	rl.mutex.RLock()
	defer rl.mutex.RUnlock()

	requests := rl.requests[userID]
	return len(requests) > 5
}

// NewBot creates a new Bot instance
func NewBot(cfg *config.Config) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, err
	}

	return &Bot{
		Session:  session,
		Config:   cfg,
		Lavalink: lavalink.NewClient(session, cfg),
	}, nil
}

// Start starts the bot
func (b *Bot) Start() error {
	b.Session.AddHandler(b.messageCreate)
	b.Session.AddHandler(b.interactionCreate)
	b.Session.AddHandler(b.voiceStateUpdate)
	b.Session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent | discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

	err := b.Session.Open()
	if err != nil {
		return err
	}

	// Initialize Lavalink
	b.Lavalink.RegisterHandlers()
	if err := b.Lavalink.Connect(context.TODO()); err != nil {
		log.Printf("Failed to connect to Lavalink: %v", err)
	}

	log.Println("Queen's Lɣre୨ৎ⭑ is now running. Press CTRL+C to exit.")

	b.registerCommands()

	// Start background routines
	go b.cleanupInactiveUsers()

	return nil
}

// cleanupInactiveUsers periodically removes inactive users from userStats
func (b *Bot) cleanupInactiveUsers() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		log.Println("Starting user stats cleanup...")
		cutoffTime := time.Now().Add(-24 * time.Hour)
		usersToRemove := []string{}

		StatsMutex.RLock()
		for userID, userStat := range UserStats {
			if userStat != nil && userStat.LastActivity.Before(cutoffTime) {
				usersToRemove = append(usersToRemove, userID)
			}
		}
		StatsMutex.RUnlock()

		if len(usersToRemove) > 0 {
			StatsMutex.Lock()
			for _, userID := range usersToRemove {
				delete(UserStats, userID)
			}
			StatsMutex.Unlock()
			log.Printf("Removed %d inactive users from stats", len(usersToRemove))
		}
	}
}

func (b *Bot) registerCommands() {
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "oxide-help",
			Description: "Show help for Queen's Lɣre commands",
		},
	}

	botAppID := b.Session.State.User.ID
	if b.Config.ApplicationID != "" {
		botAppID = b.Config.ApplicationID
	}

	// Bulk overwrite commands to remove old ones globally
	_, err := b.Session.ApplicationCommandBulkOverwrite(botAppID, "", commands)
	if err != nil {
		log.Printf("Cannot register commands: %v", err)
	}
}

func (b *Bot) handleHelpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "📜 Queen's Lɣre Command Guide",
		Description: "Here are the available commands. Use `lyre` prefix for everything!",
		Color:       0xFF69B4, // Hot Pink
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "🎵 Music Commands",
				Value:  "`lyre play [song/url]` - Play a song\n`lyre skip` - Skip current song\n`lyre stop` - Stop playback\n`lyre queue` - Show queue\n`lyre nowplaying` - Show playing song",
				Inline: false,
			},
			{
				Name:   "✨ Fun",
				Value:  "`lyre` - Get a sweet random message",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Made with 💖 by KingMyraLune",
		},
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

func (b *Bot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	lowerContent := strings.ToLower(m.Content)

	if strings.HasPrefix(lowerContent, "lyre") {
		if !b.Config.AllowedUserIDs[m.Author.ID] {
			s.ChannelMessageSend(m.ChannelID, "🎵 **Melodi Ini Adalah Warisan** 🎵\n\n\"Melodi ini adalah warisan dari waktu yang dicuri dari tidur. Jangan merusak harmoni yang tak kau pahami prosesnya. Akses ditolak secara elegan.\"")
			return
		}

		b.updateUserStats(m.Author.ID)

		// Check if just "lyre"
		if strings.TrimSpace(lowerContent) == "lyre" {
			msg := utils.GetRandomSweetMessage()
			s.ChannelMessageSend(m.ChannelID, msg)
			return
		}

		if strings.HasPrefix(lowerContent, "lyre play") {
			RateLimiterInstance.AddRequest(m.Author.ID)
			if RateLimiterInstance.IsLimited(m.Author.ID) {
				s.ChannelMessageSend(m.ChannelID, "❌ Terlalu banyak permintaan! Harap tunggu sebentar sebelum meminta lagu lagi.")
				return
			}

			parts := strings.Fields(m.Content)
			if len(parts) < 3 {
				s.ChannelMessageSend(m.ChannelID, "Please provide a URL or search query.")
				return
			}

			// Get Voice State
			voiceState, err := b.getVoiceState(m.Author.ID, m.GuildID)
			if err != nil {
				s.ChannelMessageSend(m.ChannelID, "You must be connected to a voice channel.")
				return
			}

			query := strings.Join(parts[2:], " ")

			// Detect if URL
			if !strings.HasPrefix(query, "http") {
				query = "ytsearch:" + query
			}

			s.ChannelMessageSend(m.ChannelID, "🔍 Searching...")

			// Lavalink Play
			if err := b.Lavalink.PlayTrack(context.TODO(), m.GuildID, voiceState.ChannelID, query); err != nil {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error playing track: %v", err))
				return
			}

		} else if strings.HasPrefix(lowerContent, "lyre skip") {
			if err := b.Lavalink.Stop(context.TODO(), m.GuildID); err != nil {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error skipping track: %v", err))
			} else {
				s.ChannelMessageSend(m.ChannelID, "Skipped current track.")
			}

		} else if strings.HasPrefix(lowerContent, "lyre stop") {
			if err := b.Lavalink.Stop(context.TODO(), m.GuildID); err != nil {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error stopping playback: %v", err))
			} else {
				s.ChannelMessageSend(m.ChannelID, "Stopped playback and cleared queue.")
			}

			// Queue command implementation
			// TODO: Implement Queue display with Lavalink
			s.ChannelMessageSend(m.ChannelID, "Queue display implementation pending with Lavalink upgrade.")

		} else if strings.HasPrefix(lowerContent, "lyre nowplaying") || strings.HasPrefix(lowerContent, "lyre np") {
			// TODO: Implement Now Playing with Lavalink
			s.ChannelMessageSend(m.ChannelID, "Now Playing display pending with Lavalink upgrade.")

		} else if strings.HasPrefix(lowerContent, "lyre hug") {
			// Parse mentioned user
			target := m.Author
			if len(m.Mentions) > 0 {
				target = m.Mentions[0]
			}

			if len(m.Mentions) == 0 {
				s.ChannelMessageSend(m.ChannelID, "Siapa yang mau dipeluk? Mention orangnya dong! 🤗\nContoh: `lyre hug @sayang`")
				return
			}

			gifURL := utils.GetHugGifURL()

			embed := &discordgo.MessageEmbed{
				Title:       fmt.Sprintf("💕 %s memeluk %s!", m.Author.Username, target.Username),
				Description: "*pelukan hangat terkirim~* 🤗✨",
				Color:       0xFF69B4,
				Image: &discordgo.MessageEmbedImage{
					URL: gifURL,
				},
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Queen's Lɣre୨ৎ⭑ | Spread love 💖",
				},
			}
			s.ChannelMessageSendEmbed(m.ChannelID, embed)

		} else if strings.HasPrefix(lowerContent, "lyre quote") {
			quote := utils.GetRandomQuote()
			embed := &discordgo.MessageEmbed{
				Title:       "✨ Quote of the Moment",
				Description: quote,
				Color:       0x9B59B6,
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Queen's Lɣre୨ৎ⭑ | Words of love 💫",
				},
			}
			s.ChannelMessageSendEmbed(m.ChannelID, embed)

		} else if strings.HasPrefix(lowerContent, "lyre autoplay") {
			AudioMutex.Lock()
			guildCtx, exists := GuildContexts[m.GuildID]
			if !exists {
				guildCtx = &models.GuildContext{
					MusicQueue: &models.MusicQueue{},
				}
				GuildContexts[m.GuildID] = guildCtx
			}
			guildCtx.AutoPlay = !guildCtx.AutoPlay
			autoPlayOn := guildCtx.AutoPlay
			AudioMutex.Unlock()

			status := "OFF"
			if autoPlayOn {
				status = "ON"
			}
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔄 **AutoPlay**: %s\nBot akan otomatis cari lagu mirip saat antrian habis.", status))

		} else if strings.HasPrefix(lowerContent, "lyre stay") {
			GuardModeMutex.Lock()
			currentMode := RoomGuardMode[m.GuildID]
			newMode := !currentMode
			RoomGuardMode[m.GuildID] = newMode
			GuardModeMutex.Unlock()

			status := "OFF"
			if newMode {
				status = "ON"
			}
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🛡️ **Room Guard / Stay Mode**: %s\nBot tidak akan disconnect otomatis saat antrian habis.", status))

		} else if strings.HasPrefix(lowerContent, "lyre help") {
			embed := &discordgo.MessageEmbed{
				Title:       "📜 Queen's Lɣre Command Guide",
				Description: "Here are the available commands. Use `lyre` prefix for everything!",
				Color:       0xFF69B4,
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:   "🎵 Music Commands",
						Value:  "`lyre play [song/url]` - Play a song\n`lyre skip` - Skip current song\n`lyre stop` - Stop playback\n`lyre queue` - Show queue\n`lyre nowplaying` - Show playing song",
						Inline: false,
					},
					{
						Name:   "⚙️ Settings",
						Value:  "`lyre stay` - Toggle 24/7 Stay Mode\n`lyre autoplay` - Toggle auto-play related songs",
						Inline: false,
					},
					{
						Name:   "✨ Fun & Social",
						Value:  "`lyre` - Sweet random message\n`lyre hug @user` - Send a hug\n`lyre quote` - Random quote",
						Inline: false,
					},
				},
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Made with 💖 by KingMyraLune",
				},
			}
			s.ChannelMessageSendEmbed(m.ChannelID, embed)
		}
	}
}

func (b *Bot) updateUserStats(userID string) {
	StatsMutex.Lock()
	defer StatsMutex.Unlock()

	userStat, exists := UserStats[userID]
	if !exists {
		userStat = &models.UserStats{
			LovePoints:    0,
			Level:         0,
			TotalMessages: 0,
			LastActivity:  time.Now(),
		}
		UserStats[userID] = userStat
	}

	userStat.TotalMessages++
	userStat.LastActivity = time.Now()
	userStat.LovePoints += 1

	newLevel := models.CalculateLevel(userStat.LovePoints)
	if newLevel > userStat.Level {
		userStat.Level = newLevel
	}
}

func (b *Bot) interactionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		switch i.ApplicationCommandData().Name {
		case "oxide-help":
			b.handleHelpCommand(s, i)
		}
	case discordgo.InteractionMessageComponent:
		switch i.MessageComponentData().CustomID {
		case "oxide_pause":
			b.handlePauseResume(s, i)
		case "oxide_skip":
			b.handleSkipCommand(s, i)
		case "oxide_stop":
			b.handleStopCommand(s, i)
		case "oxide_queue":
			b.handleQueueCommand(s, i)
		}
	}
}

func (b *Bot) handlePauseResume(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})

	// Toggle pause state
	AudioMutex.Lock()
	guildCtx, exists := GuildContexts[i.GuildID]
	isPaused := false
	if exists && guildCtx != nil {
		guildCtx.IsPaused = !guildCtx.IsPaused
		isPaused = guildCtx.IsPaused
	}
	AudioMutex.Unlock()

	status := "Resumed"
	if isPaused {
		status = "Paused"
	}

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: fmt.Sprintf("Playback %s ⏯️", status),
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}

func (b *Bot) handleSkipCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	AudioMutex.Lock()
	guildCtx, exists := GuildContexts[i.GuildID]
	if exists && guildCtx != nil {
		guildCtx.CurrentTrack = nil
	}
	AudioMutex.Unlock()

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: "Skipped current track.",
	})
}

func (b *Bot) handleStopCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	AudioMutex.Lock()
	guildCtx, exists := GuildContexts[i.GuildID]
	if exists && guildCtx != nil {
		guildCtx.MusicQueue.Tracks = nil
		guildCtx.CurrentTrack = nil
	}
	AudioMutex.Unlock()

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: "Stopped playback and cleared queue.",
	})
}

func (b *Bot) handleQueueCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	// Use Lavalink Queue
	queue := b.Lavalink.GetQueue(i.GuildID)

	tracks := []*models.Track{}
	if queue != nil && len(queue.Tracks) > 0 {
		tracks = queue.Tracks
	}

	if len(tracks) == 0 {
		s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Queue is empty.",
		})
		return
	}

	var msg strings.Builder
	msg.WriteString("**Music Queue:**\n")
	for idx, track := range tracks {
		msg.WriteString(fmt.Sprintf("%d. %s\n", idx+1, track.Title))
	}

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: msg.String(),
	})
}

// voiceStateUpdate handles voice channel events (welcome message)
func (b *Bot) voiceStateUpdate(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	// Only trigger when a user JOINS a channel (BeforeUpdate was nil or different channel)
	if v.ChannelID == "" {
		return // User left a channel, ignore
	}

	// Don't greet the bot itself
	if v.UserID == s.State.User.ID {
		return
	}

	// Check if user just joined (wasn't in a channel before, or changed channels)
	if v.BeforeUpdate != nil && v.BeforeUpdate.ChannelID == v.ChannelID {
		return // Same channel, not a new join
	}

	// Check if the bot is in the same channel
	AudioMutex.RLock()
	guildCtx, exists := GuildContexts[v.GuildID]
	botInChannel := exists && guildCtx != nil && guildCtx.VoiceConnection != nil && guildCtx.VoiceConnection.ChannelID == v.ChannelID
	AudioMutex.RUnlock()

	if !botInChannel {
		return
	}

	// Get user info
	user, err := s.User(v.UserID)
	if err != nil {
		return
	}

	// Find a text channel to send the welcome message
	guild, err := s.State.Guild(v.GuildID)
	if err != nil {
		return
	}

	greetings := []string{
		fmt.Sprintf("Hai %s! Selamat datang di voice~ 🎶💖", user.Username),
		fmt.Sprintf("Yay! %s masuk! Mau dengerin lagu bareng? 🎵✨", user.Username),
		fmt.Sprintf("Welcome %s~ Aku sudah menunggumu! 🌸", user.Username),
		fmt.Sprintf("%s! Akhirnya kamu datang juga~ 💕", user.Username),
	}
	greeting := greetings[time.Now().UnixNano()%int64(len(greetings))]

	// Send to the last known text channel, or find one
	channelID := ""
	AudioMutex.RLock()
	if guildCtx != nil && guildCtx.LastChannelID != "" {
		channelID = guildCtx.LastChannelID
	}
	AudioMutex.RUnlock()

	if channelID == "" {
		for _, ch := range guild.Channels {
			if ch.Type == discordgo.ChannelTypeGuildText {
				channelID = ch.ID
				break
			}
		}
	}

	if channelID != "" {
		s.ChannelMessageSend(channelID, greeting)
	}
}

// Helper to get voice state
func (b *Bot) getVoiceState(userID, guildID string) (*discordgo.VoiceState, error) {
	guild, err := b.Session.State.Guild(guildID)
	if err != nil {
		return nil, err
	}

	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			return vs, nil
		}
	}
	return nil, fmt.Errorf("user not in voice channel")
}
