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
			Name:        "oxide-play",
			Description: "Play a song from YouTube or other supported platforms",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "URL or search query for the song",
					Required:    true,
				},
			},
		},
		{
			Name:        "oxide-skip",
			Description: "Skip the current song",
		},
		{
			Name:        "oxide-stop",
			Description: "Stop playback and clear the queue",
		},
		{
			Name:        "oxide-queue",
			Description: "Show the current music queue",
		},
		{
			Name:        "oxide-nowplaying",
			Description: "Show the currently playing track",
		},
	}

	botAppID := b.Session.State.User.ID
	if b.Config.ApplicationID != "" {
		botAppID = b.Config.ApplicationID
	}

	for _, command := range commands {
		_, err := b.Session.ApplicationCommandCreate(botAppID, "", command)
		if err != nil {
			log.Printf("Cannot create command %v: %v", command.Name, err)
		}
	}
}

func (b *Bot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	lowerContent := strings.ToLower(m.Content)

	if strings.HasPrefix(lowerContent, "lyre") || strings.HasPrefix(lowerContent, "queen") {
		if !b.Config.AllowedUserIDs[m.Author.ID] {
			s.ChannelMessageSend(m.ChannelID, "🎵 **Melodi Ini Adalah Warisan** 🎵\n\n\"Melodi ini adalah warisan dari waktu yang dicuri dari tidur. Jangan merusak harmoni yang tak kau pahami prosesnya. Akses ditolak secara elegan.\"")
			return
		}

		b.updateUserStats(m.Author.ID)

		if strings.HasPrefix(lowerContent, "lyre play") || strings.HasPrefix(lowerContent, "queen play") {
			RateLimiterInstance.AddRequest(m.Author.ID)
			if RateLimiterInstance.IsLimited(m.Author.ID) {
				s.ChannelMessageSend(m.ChannelID, "❌ Terlalu banyak permintaan! Harap tunggu sebentar sebelum meminta lagu lagi.")
				return
			}

			// Process play command (simplified for brevity, main logic in interaction)
			parts := strings.Fields(m.Content)
			if len(parts) < 3 {
				s.ChannelMessageSend(m.ChannelID, "Please provide a URL or search query using /oxide-play command for better experience.")
				return
			}
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
		case "oxide-play":
			b.handlePlayCommand(s, i)
		case "oxide-skip":
			b.handleSkipCommand(s, i)
		case "oxide-stop":
			b.handleStopCommand(s, i)
		case "oxide-queue":
			b.handleQueueCommand(s, i)
		case "oxide-nowplaying":
			b.handleNowPlayingCommand(s, i)
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
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "⏸️ Pause/Resume functionality is currently being enhanced and will be available soon!",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (b *Bot) handlePlayCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Rate limit check
	RateLimiterInstance.AddRequest(i.Member.User.ID)
	if RateLimiterInstance.IsLimited(i.Member.User.ID) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Terlalu banyak permintaan!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	query := i.ApplicationCommandData().Options[0].StringValue()

	// Get Voice State
	voiceState, err := audio.GetVoiceState(s, i.Member.User.ID, i.GuildID)
	if err != nil {
		s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You must be connected to a voice channel.",
		})
		return
	}

	// Get info
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	track, err := audio.GetYoutubeInfoWithContext(ctx, query)
	if err != nil {
		s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Error getting track info: %v", err),
		})
		return
	}

	track.RequesterID = i.Member.User.ID
	track.RequesterUsername = i.Member.User.Username

	// Add to queue
	audio.Mutex.Lock()
	guildCtx, exists := audio.GuildContexts[i.GuildID]
	if !exists {
		guildCtx = &models.GuildContext{
			MusicQueue: &models.MusicQueue{},
		}
		audio.GuildContexts[i.GuildID] = guildCtx
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
		audio.PlayNextTrack(s, i.GuildID, voiceState.ChannelID)
		s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Now playing: `%s`", track.Title),
		})
	} else {
		s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Added `%s` to queue.", track.Title),
		})
	}
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

func (b *Bot) handleNowPlayingCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	audio.Mutex.RLock()
	guildCtx, exists := audio.GuildContexts[i.GuildID]
	var track *models.Track
	if exists && guildCtx != nil {
		track = guildCtx.CurrentTrack
	}
	audio.Mutex.RUnlock()

	if track == nil {
		s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Nothing is playing.",
		})
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Now Playing",
		Description: fmt.Sprintf("[%s](%s)", track.Title, track.URL),
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
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
			URL: track.Thumbnail,
		}
	}

	s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}
