package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"kingmyralune/oxide-music-bot/internal/audio"
	"kingmyralune/oxide-music-bot/internal/config"
	"kingmyralune/oxide-music-bot/internal/models"
	"kingmyralune/oxide-music-bot/internal/utils"
)

type Bot struct {
	Session *discordgo.Session
	Config  *config.Config
}

// Global variables for the leveling system (moved from main)
var (
	UserStats  = make(map[string]*models.UserStats)
	StatsMutex sync.RWMutex

	// Rate limiter
	RateLimiterInstance = &RateLimiter{
		requests: make(map[string][]time.Time),
	}
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
		Session: session,
		Config:  cfg,
	}, nil
}

// Start starts the bot
func (b *Bot) Start() error {
	b.Session.AddHandler(b.messageCreate)
	b.Session.AddHandler(b.interactionCreate)
	b.Session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent | discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

	err := b.Session.Open()
	if err != nil {
		return err
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

			query := strings.Join(parts[2:], " ")

			// Get Voice State
			voiceState, err := audio.GetVoiceState(s, m.Author.ID, m.GuildID)
			if err != nil {
				s.ChannelMessageSend(m.ChannelID, "You must be connected to a voice channel.")
				return
			}

			// Get info
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			s.ChannelMessageSend(m.ChannelID, "🔍 Searching...")

			track, err := audio.GetTrackInfoWithContext(ctx, query)
			if err != nil {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error finding track: %v", err))
				return
			}

			track.RequesterID = m.Author.ID
			track.RequesterUsername = m.Author.Username

			// Add to queue
			audio.Mutex.Lock()
			guildCtx, exists := audio.GuildContexts[m.GuildID]
			if !exists {
				guildCtx = &models.GuildContext{
					MusicQueue: &models.MusicQueue{},
				}
				audio.GuildContexts[m.GuildID] = guildCtx
			}
			if guildCtx.MusicQueue == nil {
				guildCtx.MusicQueue = &models.MusicQueue{}
			}

			guildCtx.MusicQueue.Tracks = append(guildCtx.MusicQueue.Tracks, track)
			audio.Mutex.Unlock()

			// Check if playing
			shouldPlay := false
			audio.Mutex.RLock()
			if guildCtx.CurrentTrack == nil && (guildCtx.MusicQueue == nil || len(guildCtx.MusicQueue.Tracks) <= 1) {
				shouldPlay = true
			}
			audio.Mutex.RUnlock()

			if shouldPlay {
				audio.PlayNextTrack(s, m.GuildID, voiceState.ChannelID)
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Now playing: `%s`", track.Title))
			} else {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Added `%s` to queue.", track.Title))
			}
		} else if strings.HasPrefix(lowerContent, "lyre skip") {
			// Basic skip implementation for text command
			audio.Mutex.Lock()
			guildCtx, exists := audio.GuildContexts[m.GuildID]
			if exists && guildCtx != nil {
				guildCtx.CurrentTrack = nil
			}
			audio.Mutex.Unlock()

			vc, err := audio.GetConnectedVoiceConnection(s, m.GuildID)
			if err == nil && vc != nil && vc.OpusSend != nil {
				close(vc.OpusSend)
				vc.OpusSend = nil
			}

			if vc != nil {
				audio.PlayNextTrack(s, m.GuildID, vc.ChannelID)
			}
			s.ChannelMessageSend(m.ChannelID, "Skipped current track.")
		} else if strings.HasPrefix(lowerContent, "lyre stop") {
			audio.Mutex.Lock()
			guildCtx, exists := audio.GuildContexts[m.GuildID]
			if exists && guildCtx != nil {
				guildCtx.MusicQueue.Tracks = nil
				guildCtx.CurrentTrack = nil
			}
			audio.Mutex.Unlock()

			vc, err := audio.GetConnectedVoiceConnection(s, m.GuildID)
			if err == nil && vc != nil {
				if vc.OpusSend != nil {
					close(vc.OpusSend)
					vc.OpusSend = nil
				}
				vc.Disconnect()
			}
			s.ChannelMessageSend(m.ChannelID, "Stopped playback and cleared queue.")
		} else if strings.HasPrefix(lowerContent, "lyre queue") {
			audio.Mutex.RLock()
			guildCtx, exists := audio.GuildContexts[m.GuildID]
			queueExists := exists && guildCtx.MusicQueue != nil && len(guildCtx.MusicQueue.Tracks) > 0
			tracks := []*models.Track{}
			if queueExists {
				tracks = guildCtx.MusicQueue.Tracks[:]
			}
			audio.Mutex.RUnlock()

			if len(tracks) == 0 {
				s.ChannelMessageSend(m.ChannelID, "Queue is empty.")
				return
			}

			var msg strings.Builder
			msg.WriteString("**Music Queue:**\n")
			for idx, track := range tracks {
				msg.WriteString(fmt.Sprintf("%d. %s\n", idx+1, track.Title))
			}
			s.ChannelMessageSend(m.ChannelID, msg.String())

		} else if strings.HasPrefix(lowerContent, "lyre nowplaying") || strings.HasPrefix(lowerContent, "lyre np") {
			audio.Mutex.RLock()
			guildCtx, exists := audio.GuildContexts[m.GuildID]
			var track *models.Track
			if exists && guildCtx != nil {
				track = guildCtx.CurrentTrack
			}
			audio.Mutex.RUnlock()

			if track == nil {
				s.ChannelMessageSend(m.ChannelID, "Nothing is playing.")
				return
			}

			embed := &discordgo.MessageEmbed{
				Title:       "Now Playing",
				Description: fmt.Sprintf("[%s](%s)", track.Title, track.URL),
				Color:       0xFF69B4,
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:   "Duration",
						Value:  track.Duration,
						Inline: true,
					},
					{
						Name:   "Requested by",
						Value:  track.RequesterUsername,
						Inline: true,
					},
				},
			}
			if track.Thumbnail != "" {
				embed.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail}
			}
			s.ChannelMessageSendEmbed(m.ChannelID, embed)

		} else if strings.HasPrefix(lowerContent, "lyre help") {
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
	audio.Mutex.Lock()
	guildCtx, exists := audio.GuildContexts[i.GuildID]
	isPaused := false
	if exists && guildCtx != nil {
		guildCtx.IsPaused = !guildCtx.IsPaused
		isPaused = guildCtx.IsPaused
	}
	audio.Mutex.Unlock()

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

	// Validation logic (voice state check etc) omitted for brevity

	audio.Mutex.Lock()
	guildCtx, exists := audio.GuildContexts[i.GuildID]
	if exists && guildCtx != nil {
		guildCtx.CurrentTrack = nil
	}
	audio.Mutex.Unlock()

	vc, err := audio.GetConnectedVoiceConnection(s, i.GuildID)
	if err == nil && vc != nil && vc.OpusSend != nil {
		// Stop current stream
		close(vc.OpusSend)
		vc.OpusSend = nil
	}

	// Play next
	// We need channel ID, could get from VC
	if vc != nil {
		audio.PlayNextTrack(s, i.GuildID, vc.ChannelID)
	}

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: "Skipped current track.",
	})
}

func (b *Bot) handleStopCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	audio.Mutex.Lock()
	guildCtx, exists := audio.GuildContexts[i.GuildID]
	if exists && guildCtx != nil {
		guildCtx.MusicQueue.Tracks = nil
		guildCtx.CurrentTrack = nil
	}
	audio.Mutex.Unlock()

	vc, err := audio.GetConnectedVoiceConnection(s, i.GuildID)
	if err == nil && vc != nil {
		if vc.OpusSend != nil {
			close(vc.OpusSend)
			vc.OpusSend = nil
		}
		vc.Disconnect()
	}

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: "Stopped playback and cleared queue.",
	})
}

func (b *Bot) handleQueueCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	audio.Mutex.RLock()
	guildCtx, exists := audio.GuildContexts[i.GuildID]
	queueExists := exists && guildCtx.MusicQueue != nil && len(guildCtx.MusicQueue.Tracks) > 0
	tracks := []*models.Track{}
	if queueExists {
		tracks = guildCtx.MusicQueue.Tracks[:]
	}
	audio.Mutex.RUnlock()

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
