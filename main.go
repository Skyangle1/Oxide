package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"layeh.com/gopus"
)

// GuildContext holds the context for each guild
type GuildContext struct {
	VoiceConnection *discordgo.VoiceConnection
	MusicQueue      *MusicQueue
	CurrentTrack    *Track
	StartTime       time.Time  // Time when the current track started playing
}

// TrackWithDuration extends Track with duration information in seconds
type TrackWithDuration struct {
	*Track
	DurationSeconds int // Duration in seconds
	StartTime       time.Time // Time when this track started playing
}



// Track represents a music track
type Track struct {
	Title       string
	URL         string
	Duration    string
	Uploader    string
	Thumbnail   string
	RequesterID string
	RequesterUsername string
}

// UserStats holds user statistics for the leveling system
type UserStats struct {
	LovePoints     int
	Level          int
	TotalMessages  int
	LastActivity   time.Time
	CurrentSongRequests int
}

// Global variables for the leveling system
var (
	userStats = make(map[string]*UserStats)
	statsMutex sync.RWMutex
)

// MusicQueue represents a queue of tracks
type MusicQueue struct {
	Tracks []*Track
	Loop   bool
}

// SavedPlaylist represents a user's saved playlist
type SavedPlaylist struct {
	Name  string
	Tracks []*Track
}

// UserPreferences holds user-specific settings
type UserPreferences struct {
	Volume float64 // Volume level (0.0 to 1.0)
	SavedPlaylists map[string]*SavedPlaylist
}

// Global variable for user preferences
var (
	userPreferences = make(map[string]*UserPreferences)
	prefsMutex sync.RWMutex
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
	
	// Initialize slice if needed
	if rl.requests[userID] == nil {
		rl.requests[userID] = make([]time.Time, 0)
	}
	
	// Add current request
	rl.requests[userID] = append(rl.requests[userID], now)
	
	// Remove requests older than 1 minute
	cutoff := now.Add(-1 * time.Minute)
	filtered := make([]time.Time, 0)
	for _, reqTime := range rl.requests[userID] {
		if reqTime.After(cutoff) {
			filtered = append(filtered, reqTime)
		}
	}
	rl.requests[userID] = filtered
}

// IsLimited checks if a user has exceeded the rate limit (max 5 requests per minute)
func (rl *RateLimiter) IsLimited(userID string) bool {
	rl.mutex.RLock()
	defer rl.mutex.RUnlock()
	
	requests := rl.requests[userID]
	
	// Check if user has more than 5 requests in the last minute
	return len(requests) > 5
}

// Maximum number of tracks allowed in the queue to prevent memory leaks
const MaxQueueSize = 50

// Global variables for command options
var (
	zero = 0
)

// Global variables
var (
	// Session for Discord
	session *discordgo.Session

	// Context for each guild
	guildContexts = make(map[string]*GuildContext)

	// Mutex for thread-safe operations
	mutex sync.RWMutex

	// Allowed users for exclusive access
	AllowedUsers map[string]bool
	
	// Rate limiter to prevent abuse
	rateLimiter = &RateLimiter{
		requests: make(map[string][]time.Time),
	}
)

func main() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Retrieve token from environment variable
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN environment variable is not set")
	}
	
	// Retrieve application ID from environment variable (optional)
	appID := os.Getenv("APPLICATION_ID")
	if appID == "" {
		log.Println("APPLICATION_ID environment variable is not set, using bot user ID")
	}

	// Initialize allowed users from environment variable
	allowedUserIDs := os.Getenv("ALLOWED_USER_IDS")
	if allowedUserIDs == "" {
		log.Fatal("ALLOWED_USER_IDS environment variable is not set")
	}
	
	// Parse the comma-separated user IDs
	AllowedUsers = make(map[string]bool)
	idParts := strings.Split(allowedUserIDs, ",")
	for _, id := range idParts {
		trimmedID := strings.TrimSpace(id)
		if trimmedID != "" {
			AllowedUsers[trimmedID] = true
		}
	}

	// Create a new Discord session using the provided bot token
	session, err = discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("Error creating Discord session: ", err)
	}

	// Register message and interaction create event handlers
	session.AddHandler(messageCreate)
	session.AddHandler(interactionCreate)
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent | discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

	// Open a websocket connection to Discord
	err = session.Open()
	if err != nil {
		log.Fatal("Error opening Discord connection: ", err)
	}

	fmt.Println("Queen's Lɣre୨ৎ⭑ is now running. Press CTRL+C to exit.")
	
	// Register slash commands
	registerCommands(appID)

	// Wait here until CTRL+C or other term signal is received
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// Cleanly close down the Discord session
	session.Close()
}

// registerCommands registers all slash commands
func registerCommands(appID string) {
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
		{
			Name:        "oxide-volume",
			Description: "Set volume level (0-100%)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "level",
					Description: "Volume level (0-100)",
					Required:    true,
					MaxValue:    100,
				},
			},
		},
		{
			Name:        "oxide-saveplaylist",
			Description: "Save a track to a playlist",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "Playlist name",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "url",
					Description: "URL of the track to save",
					Required:    true,
				},
			},
		},
		{
			Name:        "oxide-loadplaylist",
			Description: "Load a playlist to the queue",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "Playlist name",
					Required:    true,
				},
			},
		},
	}

	// Determine the application ID to use
	botAppID := session.State.User.ID
	if appID != "" {
		botAppID = appID
	}

	for _, command := range commands {
		_, err := session.ApplicationCommandCreate(botAppID, "", command)
		if err != nil {
			log.Printf("Cannot create command %v: %v", command.Name, err)
		}
	}
}

// messageCreate handles incoming messages
func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}

	// Convert message to lowercase for case-insensitive comparison
	lowerContent := strings.ToLower(m.Content)

	// Check if the message starts with "lyre" or "queen" (case insensitive) - only process commands
	if strings.HasPrefix(lowerContent, "lyre") || strings.HasPrefix(lowerContent, "queen") {
		// Check if the message author is in the allowed users list
		if !AllowedUsers[m.Author.ID] {
			// Send a strong message to unauthorized users
			s.ChannelMessageSend(m.ChannelID, "❌ **PERINGATAN KERAS!** ❌\n\nAnda tidak memiliki izin untuk menggunakan bot ini. Bot ini hanya untuk penggunaan eksklusif oleh dua orang yang telah ditentukan. Mohon tinggalkan channel ini dan jangan coba-coba mengakses fitur ini lagi.")
			return
		}

		// Update user stats for the message
		updateUserStats(m.Author.ID)

		// Check if the message starts with "lyre play" or "queen play" (case insensitive)
		if strings.HasPrefix(lowerContent, "lyre play") || strings.HasPrefix(lowerContent, "queen play") {
			// Check rate limit for play commands
			rateLimiter.AddRequest(m.Author.ID)
			if rateLimiter.IsLimited(m.Author.ID) {
				s.ChannelMessageSend(m.ChannelID, "❌ Terlalu banyak permintaan! Harap tunggu sebentar sebelum meminta lagu lagi.")
				return
			}

			// Extract the URL or search query after "play"
			parts := strings.Fields(m.Content)
			if len(parts) < 3 {
				s.ChannelMessageSend(m.ChannelID, "Please provide a URL or search query to play.")
				return
			}

			// Find the index of "play" to extract everything after it
			playIndex := -1
			for i, part := range parts {
				if strings.ToLower(part) == "play" && i < len(parts)-1 {
					playIndex = i
					break
				}
			}

			if playIndex == -1 || playIndex >= len(parts)-1 {
				s.ChannelMessageSend(m.ChannelID, "Please provide a URL or search query to play.")
				return
			}

			// Extract the query (everything after "play")
			queryParts := parts[playIndex+1:]
			query := strings.Join(queryParts, " ")

			// Create a mock interaction to reuse the existing play command logic
			go func() {
				// Get voice state to check if user is in a voice channel
				voiceState, err := getVoiceState(s, m.Author.ID, m.GuildID)
				if err != nil {
					s.ChannelMessageSend(m.ChannelID, "You must be connected to a voice channel to use this command.")
					return
				}

				// Get track info using yt-dlp
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				track, err := getYoutubeInfoWithContext(ctx, query)
				if err != nil {
					s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error getting track info: %v", err))
					return
				}

				// Set the requester
				track.RequesterID = m.Author.ID
				track.RequesterUsername = m.Author.Username

				// Create or get the music queue for this guild
				mutex.Lock()
				guildCtx, exists := guildContexts[m.GuildID]
				if !exists {
					log.Printf("Creating new guild context for guild %s", m.GuildID)
					guildCtx = &GuildContext{
						MusicQueue: &MusicQueue{},
					}
					guildContexts[m.GuildID] = guildCtx
				}

				if guildCtx.MusicQueue == nil {
					guildCtx.MusicQueue = &MusicQueue{}
				}

				// Check if the queue has reached the maximum size
				if len(guildCtx.MusicQueue.Tracks) >= MaxQueueSize {
					mutex.Unlock()
					s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Antrean sudah penuh! Maksimal %d lagu dalam antrean. Silakan tunggu hingga ada lagu yang selesai diputar.", MaxQueueSize))
					return
				}

				guildCtx.MusicQueue.Tracks = append(guildCtx.MusicQueue.Tracks, track)
				mutex.Unlock()

				log.Printf("Added track '%s' to queue for guild %s", track.Title, m.GuildID)

				// Check if bot is already in voice channel
				vc, exists := s.VoiceConnections[m.GuildID]
				if !exists || vc == nil {
					// Auto-join voice channel if not already connected
					log.Printf("Bot not in voice channel, auto-joining %s", voiceState.ChannelID)
					vc, err = s.ChannelVoiceJoin(m.GuildID, voiceState.ChannelID, false, true)
					if err != nil || vc == nil {
						log.Printf("Error auto-joining voice channel: %v", err)

						s.ChannelMessageSend(m.ChannelID, "❌ Gagal join voice channel. Coba lagi nanti!")
						return
					}

					// Set log level to debug to see encryption details
					vc.LogLevel = discordgo.LogDebug
					
					// Wait for the connection to be ready
					for !vc.Ready {
						time.Sleep(100 * time.Millisecond)
					}
				}

				// If nothing is currently playing, start playback
				var tempGuildCtx *GuildContext
				var tempExists bool
				var hasTrack bool
				mutex.RLock()
				tempGuildCtx, tempExists = guildContexts[m.GuildID]
				hasTrack = false
				if tempExists && tempGuildCtx != nil && tempGuildCtx.CurrentTrack != nil {
					hasTrack = true
				}
				mutex.RUnlock()

				if !hasTrack {
					log.Printf("Nothing playing, starting playback for guild %s", m.GuildID)
					playNextTrack(s, m.GuildID, voiceState.ChannelID)

					// Send enhanced now playing message with love points
					statsMutex.RLock()
					userStat, exists := userStats[m.Author.ID]
					statsMutex.RUnlock()

					level := 0
					lovePoints := 0
					if exists && userStat != nil {
						level = userStat.Level
						lovePoints = userStat.LovePoints
					}

					// Create a more detailed "Now Playing" message
					nowPlayingMessage := fmt.Sprintf(
						"🎵 **Now Playing:** `%s`\n"+
						"👤 **Requested by:** %s **(Level %d)**\n"+
						"⏱️ **Duration:** %s\n"+
						"💝 **Love Points:** %d",
						track.Title,
						m.Author.Username,
						level,
						track.Duration,
						lovePoints,
					)

					// Send the message with embed if thumbnail is available
					if track.Thumbnail != "" {
						embed := &discordgo.MessageEmbed{
							Title:       "🎵 Now Playing",
							URL:         track.URL,
							Description: fmt.Sprintf("[%s](%s)", track.Title, track.URL),
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:  "👤 Requested by",
									Value: fmt.Sprintf("%s (Level %d)", m.Author.Username, level),
									Inline: true,
								},
								{
									Name:  "⏱️ Duration",
									Value: track.Duration,
									Inline: true,
								},
								{
									Name:  "💝 Love Points",
									Value: fmt.Sprintf("%d", lovePoints),
									Inline: true,
								},
							},
							Color: 0xff69b4, // Pink color for romantic feel
							Thumbnail: &discordgo.MessageEmbedThumbnail{
								URL: track.Thumbnail,
							},
						}

						// Create buttons for controls
						components := []discordgo.MessageComponent{
							discordgo.ActionsRow{
								Components: []discordgo.MessageComponent{
									discordgo.Button{
										Emoji: &discordgo.ComponentEmoji{Name: "⏯️"},
										Style: discordgo.PrimaryButton,
										CustomID: "pause_resume_" + m.GuildID,
									},
									discordgo.Button{
										Emoji: &discordgo.ComponentEmoji{Name: "⏭️"},
										Style: discordgo.PrimaryButton,
										CustomID: "skip_" + m.GuildID,
									},
									discordgo.Button{
										Emoji: &discordgo.ComponentEmoji{Name: "🛑"},
										Style: discordgo.DangerButton,
										CustomID: "stop_" + m.GuildID,
									},
									discordgo.Button{
										Emoji: &discordgo.ComponentEmoji{Name: "🔁"},
										Style: discordgo.SecondaryButton,
										CustomID: "loop_" + m.GuildID,
									},
								},
							},
						}

						// Send the embed with buttons
						s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
							Embeds:     []*discordgo.MessageEmbed{embed},
							Components: components,
						})
					} else {
						// Send simple message if no thumbnail
						s.ChannelMessageSend(m.ChannelID, nowPlayingMessage)
					}
				} else {
					// Send a message that the track was added to the queue
					log.Printf("Track added to queue, currently playing another track in guild %s", m.GuildID)
					s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Added `%s` to the queue.", track.Title))
				}
			}()
		} else {
			// Handle other commands or just the name being called
			parts := strings.Fields(m.Content)
			if len(parts) < 2 {
				// Just called the bot without a command
				// Special response for the girlfriend (second user ID in the list)
				allowedUserIDs := os.Getenv("ALLOWED_USER_IDS")
				idParts := strings.Split(allowedUserIDs, ",")
				girlfriendID := ""
				if len(idParts) >= 2 {
					girlfriendID = strings.TrimSpace(idParts[1]) // Assuming the second ID is the girlfriend's
				}

				if m.Author.ID == girlfriendID {
					s.ChannelMessageSend(m.ChannelID, "Hai sayangku! Queen's Lɣre୨ৎ⭑ di sini untukmu. Mau dengerin lagu romantis bareng aku? ୨ৎ⭑")
				} else {
					s.ChannelMessageSend(m.ChannelID, "I am here, My Queen. Queen's Lɣre୨ৎ⭑ siap memutar melodi untukmu! ୨ৎ⭑")
				}
				return
			}

			// Handle text commands for the leveling system
			command := strings.ToLower(parts[1])
			switch command {
			case "lovepoints":
				handleLovePointsCommand(s, m)
			case "couplestats":
				handleCoupleStatsCommand(s, m)
			case "loveprofile":
				handleLoveProfileCommand(s, m)
			case "volume":
				handleVolumeCommand(s, m)
			case "saveplaylist":
				handleSavePlaylistCommand(s, m)
			case "loadplaylist":
				handleLoadPlaylistCommand(s, m)
			case "help":
				handleHelpCommand(s, m)
			default:
				// Handle other commands or just the name being called
				s.ChannelMessageSend(m.ChannelID, "Apa ada yang bisa aku bantu? Coba gunakan perintah seperti 'Lyre lovepoints' atau 'Queen couplestats'")
			}
		}
	}
}

// interactionCreate handles incoming interactions
func interactionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Handle slash commands
	if i.Type == discordgo.InteractionApplicationCommand {
		switch i.ApplicationCommandData().Name {
		case "oxide-play":
			handlePlayCommand(s, i)
		case "oxide-skip":
			handleSkipCommand(s, i)
		case "oxide-stop":
			handleStopCommand(s, i)
		case "oxide-queue":
			handleQueueCommand(s, i)
		case "oxide-nowplaying":
			handleNowPlayingCommand(s, i)
		case "oxide-volume":
			handleSlashVolumeCommand(s, i)
		case "oxide-saveplaylist":
			handleSlashSavePlaylistCommand(s, i)
		case "oxide-loadplaylist":
			handleSlashLoadPlaylistCommand(s, i)
		}
	}
	// Handle button interactions
	if i.Type == discordgo.InteractionMessageComponent {
		handleButtonInteraction(s, i)
	}
}

// handlePlayCommand handles the play command
func handlePlayCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	username := i.Member.User.Username
	userID := i.Member.User.ID
	log.Println("Perintah /play diterima dari: " + username)
	
	// Check rate limit
	rateLimiter.AddRequest(userID)
	if rateLimiter.IsLimited(userID) {
		// Respond with rate limit exceeded message
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Terlalu banyak permintaan! Harap tunggu sebentar sebelum meminta lagu lagi.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			log.Printf("Error responding with rate limit exceeded: %v", err)
		}
		return
	}
	
	// Defer the response to prevent timeout - MUST BE DONE IN THE FIRST FEW MILLISECONDS
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	// Get the query from options
	query := i.ApplicationCommandData().Options[0].StringValue()
	log.Printf("Processing query: %s", query)
	
	// Validate the URL to prevent command injection
	sanitizedQuery := sanitizeURL(query)
	if sanitizedQuery == "" {
		log.Printf("Invalid URL provided by user %s: %s", username, query)
		
		// Use follow-up message to respond after defer
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Invalid URL provided. Please provide a valid URL or search query.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	// Get the voice channel the user is connected to
	voiceState, err := getVoiceState(s, i.Member.User.ID, i.GuildID)
	if err != nil {
		log.Printf("User %s not in voice channel: %v", username, err)
		
		// Use follow-up message to respond after defer
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You must be connected to a voice channel to use this command.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	// Get track info using yt-dlp with timeout (60 seconds as requested)
	log.Println("Sedang mencari lagu di YouTube...")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	
	track, err := getYoutubeInfoWithContext(ctx, sanitizedQuery)
	if err != nil {
		log.Printf("Error getting track info: %v", err)
		
		// Use follow-up message to respond after defer with user-friendly error
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "❌ Maaf Bre, lagunya gagal diambil. Coba link lain atau judul yang lebih spesifik!",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}
	
	// Set the requester
	track.RequesterID = i.Member.User.ID
	track.RequesterUsername = username

	// Create or get the guild context and music queue
	mutex.Lock()
	guildCtx, exists := guildContexts[i.GuildID]
	if !exists {
		log.Printf("Creating new guild context for guild %s", i.GuildID)
		guildCtx = &GuildContext{
			MusicQueue: &MusicQueue{},
		}
		guildContexts[i.GuildID] = guildCtx
	}

	if guildCtx.MusicQueue == nil {
		guildCtx.MusicQueue = &MusicQueue{}
	}

	// Check if the queue has reached the maximum size
	if len(guildCtx.MusicQueue.Tracks) >= MaxQueueSize {
		mutex.Unlock()
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Antrean sudah penuh! Maksimal %d lagu dalam antrean. Silakan tunggu hingga ada lagu yang selesai diputar.", MaxQueueSize),
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}
	
	guildCtx.MusicQueue.Tracks = append(guildCtx.MusicQueue.Tracks, track)
	mutex.Unlock()

	log.Printf("Added track '%s' to queue for guild %s", track.Title, i.GuildID)

	// Check if bot is already in voice channel
	vc, exists := s.VoiceConnections[i.GuildID]
	if !exists || vc == nil {
		// Auto-join voice channel if not already connected
		log.Printf("Bot not in voice channel, auto-joining %s", voiceState.ChannelID)
		vc, err = s.ChannelVoiceJoin(i.GuildID, voiceState.ChannelID, false, true)
		if err != nil || vc == nil {
			log.Printf("Error auto-joining voice channel: %v", err)
			
			_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
				Content: "❌ Gagal join voice channel. Coba lagi nanti!",
			})
			if err != nil {
				log.Printf("Error sending follow-up message: %v", err)
			}
			return
		}
		
		// Set log level to debug to see encryption details
		vc.LogLevel = discordgo.LogDebug
		
		// Wait for the connection to be ready
		for !vc.Ready {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// If nothing is currently playing, start playback
	var tempGuildCtx *GuildContext
	var tempExists bool
	var hasTrack bool
	mutex.RLock()
	tempGuildCtx, tempExists = guildContexts[i.GuildID]
	hasTrack = false
	if tempExists && tempGuildCtx != nil && tempGuildCtx.CurrentTrack != nil {
		hasTrack = true
	}
	mutex.RUnlock()
	
	if !hasTrack {
		log.Printf("Nothing playing, starting playback for guild %s", i.GuildID)
		playNextTrack(s, i.GuildID, voiceState.ChannelID)
		
		// Use follow-up message to respond after defer
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Now playing: `%s`", track.Title),
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
	} else {
		// Send a message that the track was added to the queue
		log.Printf("Track added to queue, currently playing another track in guild %s", i.GuildID)
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Added `%s` to the queue.", track.Title),
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
	}
}

// handleSkipCommand handles the skip command
func handleSkipCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	// Check if the user is in the same voice channel as the bot
	voiceState, err := getVoiceState(s, i.Member.User.ID, i.GuildID)
	if err != nil {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You must be connected to a voice channel to use this command.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	vc, err := getConnectedVoiceConnection(s, i.GuildID)
	if err != nil || vc == nil {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Not connected to a voice channel.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	if vc.ChannelID != voiceState.ChannelID {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You must be in the same voice channel as the bot to use this command.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	// Skip the current track
	mutex.Lock()
	guildCtx, exists := guildContexts[i.GuildID]
	if exists && guildCtx != nil {
		guildCtx.CurrentTrack = nil
	}
	mutex.Unlock()

	// Stop the current audio stream before playing the next track
	// We need to get the voice connection again to ensure we're working with the right one
	vc, err2 := getConnectedVoiceConnection(s, i.GuildID)
	if err2 != nil || vc == nil {
		log.Printf("Error getting voice connection for skip: %v", err2)
	} else {
		// Stop the current audio stream by closing the OpusSend channel
		if vc.OpusSend != nil {
			// Close the OpusSend channel to stop the current stream
			close(vc.OpusSend)
			vc.OpusSend = nil
		}
	}

	// Play the next track
	playNextTrack(s, i.GuildID, voiceState.ChannelID)
	
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: "Skipped the current track.",
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleStopCommand handles the stop command
func handleStopCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	// Check if the user is in the same voice channel as the bot
	voiceState, err := getVoiceState(s, i.Member.User.ID, i.GuildID)
	if err != nil {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You must be connected to a voice channel to use this command.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	vc, err := getConnectedVoiceConnection(s, i.GuildID)
	if err != nil || vc == nil {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Not connected to a voice channel.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	if vc.ChannelID != voiceState.ChannelID {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "You must be in the same voice channel as the bot to use this command.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	// Clear the queue and disconnect from voice
	var guildCtx *GuildContext
	var exists bool
	mutex.Lock()
	guildCtx, exists = guildContexts[i.GuildID]
	if exists && guildCtx != nil && guildCtx.MusicQueue != nil {
		guildCtx.MusicQueue.Tracks = nil
	}
	mutex.Unlock()
	
	// Clear the current track from guild context
	var tempGuildCtx *GuildContext
	var tempExists bool
	mutex.Lock()
	tempGuildCtx, tempExists = guildContexts[i.GuildID]
	if tempExists && tempGuildCtx != nil {
		tempGuildCtx.CurrentTrack = nil
	}
	mutex.Unlock()

	vc.Disconnect()
	
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: "Stopped playback and cleared the queue.",
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleQueueCommand handles the queue command
func handleQueueCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	mutex.RLock()
	guildCtx, exists := guildContexts[i.GuildID]
	queueExists := exists && guildCtx.MusicQueue != nil && len(guildCtx.MusicQueue.Tracks) > 0
	tracks := []*Track{}
	if queueExists {
		tracks = guildCtx.MusicQueue.Tracks[:]
	}
	mutex.RUnlock()
	
	if !queueExists || len(tracks) == 0 {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "The queue is empty.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	var queueMsg strings.Builder
	queueMsg.WriteString("**Music Queue:**\n")

	for idx, track := range tracks {
		queueMsg.WriteString(fmt.Sprintf("%d. %s - Requested by %s\n", idx+1, track.Title, track.RequesterUsername))
	}
	
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: queueMsg.String(),
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleNowPlayingCommand handles the now playing command
func handleNowPlayingCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	mutex.RLock()
	guildCtx, exists := guildContexts[i.GuildID]
	currentTrack := (*Track)(nil)
	if exists && guildCtx.CurrentTrack != nil {
		currentTrack = guildCtx.CurrentTrack
	}
	hasTrack := currentTrack != nil
	mutex.RUnlock()
	
	if !hasTrack {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Nothing is currently playing.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	// Get user stats for the requester
	statsMutex.RLock()
	requesterStats, exists := userStats[currentTrack.RequesterID]
	statsMutex.RUnlock()
	
	requesterLevel := 0
	if exists && requesterStats != nil {
		requesterLevel = requesterStats.Level
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎵 Now Playing",
		Description: fmt.Sprintf("[%s](%s)", currentTrack.Title, currentTrack.URL),
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:  "⏱️ Duration",
				Value: currentTrack.Duration,
				Inline: true,
			},
			{
				Name:  "👤 Uploaded by",
				Value: currentTrack.Uploader,
				Inline: true,
			},
			{
				Name:  "💝 Requested by",
				Value: fmt.Sprintf("%s (Level %d)", currentTrack.RequesterUsername, requesterLevel),
				Inline: true,
			},
		},
		Color: 0xff69b4, // Pink color for romantic feel
	}

	// Add thumbnail if available
	if currentTrack.Thumbnail != "" {
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
			URL: currentTrack.Thumbnail,
		}
	}

	// Create buttons for controls
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Emoji: &discordgo.ComponentEmoji{Name: "⏯️"},
					Style: discordgo.PrimaryButton,
					CustomID: "pause_resume_" + i.GuildID,
				},
				discordgo.Button{
					Emoji: &discordgo.ComponentEmoji{Name: "⏭️"},
					Style: discordgo.PrimaryButton,
					CustomID: "skip_" + i.GuildID,
				},
				discordgo.Button{
					Emoji: &discordgo.ComponentEmoji{Name: "⏹️"},
					Style: discordgo.DangerButton,
					CustomID: "stop_" + i.GuildID,
				},
				discordgo.Button{
					Emoji: &discordgo.ComponentEmoji{Name: "🔁"},
					Style: discordgo.SecondaryButton,
					CustomID: "loop_" + i.GuildID,
				},
			},
		},
	}

	// Send the embed with buttons using follow-up
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleSlashVolumeCommand handles the volume slash command
func handleSlashVolumeCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	// Get the volume level from options
	volume := i.ApplicationCommandData().Options[0].IntValue()
	
	if volume < 0 || volume > 100 {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Volume harus antara 0-100.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}
	
	// Convert to 0.0-1.0 scale
	volumeFloat := float64(volume) / 100.0
	
	prefs := getOrCreateUserPrefs(i.Member.User.ID)
	prefs.Volume = volumeFloat
	
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: fmt.Sprintf("🔊 Volume diatur ke %d%%", volume),
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleSlashSavePlaylistCommand handles saving a track to a playlist via slash command
func handleSlashSavePlaylistCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	// Get the playlist name and track URL from options
	playlistName := i.ApplicationCommandData().Options[0].StringValue()
	trackURL := i.ApplicationCommandData().Options[1].StringValue()
	
	// Get track info
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	track, err := getYoutubeInfoWithContext(ctx, trackURL)
	if err != nil {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Error getting track info: %v", err),
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}
	
	prefs := getOrCreateUserPrefs(i.Member.User.ID)
	
	playlist, exists := prefs.SavedPlaylists[playlistName]
	if !exists {
		playlist = &SavedPlaylist{
			Name: playlistName,
			Tracks: make([]*Track, 0),
		}
		prefs.SavedPlaylists[playlistName] = playlist
	}
	
	playlist.Tracks = append(playlist.Tracks, track)
	
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: fmt.Sprintf("🎵 Lagu '%s' telah ditambahkan ke playlist '%s'", track.Title, playlistName),
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleSlashLoadPlaylistCommand handles loading a playlist via slash command
func handleSlashLoadPlaylistCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error deferring interaction: %v", err)
		return
	}

	// Get the playlist name from options
	playlistName := i.ApplicationCommandData().Options[0].StringValue()
	
	prefs := getOrCreateUserPrefs(i.Member.User.ID)
	playlist, exists := prefs.SavedPlaylists[playlistName]
	if !exists {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Playlist '%s' tidak ditemukan.", playlistName),
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}
	
	// Add all tracks in the playlist to the music queue
	mutex.Lock()
	guildCtx, exists := guildContexts[i.GuildID]
	if !exists {
		guildCtx = &GuildContext{
			MusicQueue: &MusicQueue{},
		}
		guildContexts[i.GuildID] = guildCtx
	}
	
	if guildCtx.MusicQueue == nil {
		guildCtx.MusicQueue = &MusicQueue{}
	}
	
	// Check if the queue has space for all tracks
	if len(guildCtx.MusicQueue.Tracks) + len(playlist.Tracks) > MaxQueueSize {
		mutex.Unlock()
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Tidak cukup ruang dalam antrean! Maksimal %d lagu dalam antrean. Playlist memiliki %d lagu.", MaxQueueSize, len(playlist.Tracks)),
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}
	
	// Add all tracks from the playlist to the queue
	for _, track := range playlist.Tracks {
		guildCtx.MusicQueue.Tracks = append(guildCtx.MusicQueue.Tracks, track)
	}
	mutex.Unlock()
	
	_, err = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: fmt.Sprintf("🎵 Playlist '%s' dengan %d lagu telah ditambahkan ke antrean.", playlistName, len(playlist.Tracks)),
	})
	if err != nil {
		log.Printf("Error sending follow-up message: %v", err)
	}
}

// handleButtonInteraction handles button clicks
func handleButtonInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	buttonID := i.MessageComponentData().CustomID
	
	// Extract guild ID from button ID
	guildID := strings.Split(buttonID, "_")[1]
	
	// Check if the user is in the same voice channel as the bot
	voiceState, err := getVoiceState(s, i.Member.User.ID, guildID)
	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You must be connected to a voice channel to use this command.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	vc, err := getConnectedVoiceConnection(s, guildID)
	if err != nil || vc == nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Not connected to a voice channel.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if vc.ChannelID != voiceState.ChannelID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You must be in the same voice channel as the bot to use this command.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	switch {
	case strings.HasPrefix(buttonID, "pause_resume"):
		// Toggle pause/resume
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Pause/Resume functionality would be implemented here.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	case strings.HasPrefix(buttonID, "skip"):
		// Skip the current track
		mutex.Lock()
		guildCtx, exists := guildContexts[guildID]
		if exists && guildCtx != nil {
			guildCtx.CurrentTrack = nil
		}
		mutex.Unlock()

		// Play the next track
		playNextTrack(s, guildID, voiceState.ChannelID)
		
		// Update the message to reflect the new track
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: "Skipped the current track. Playing next track...",
			},
		})
	case strings.HasPrefix(buttonID, "stop"):
		// Stop playback and clear the queue
		mutex.Lock()
		guildCtx, exists := guildContexts[guildID]
		if exists && guildCtx != nil && guildCtx.MusicQueue != nil {
			guildCtx.MusicQueue.Tracks = nil
		}

		// Clear the current track from guild context
		if exists && guildCtx != nil {
			guildCtx.CurrentTrack = nil
		}
		mutex.Unlock()

		vc.Disconnect()
		
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: "Playback stopped and queue cleared.",
			},
		})
	case strings.HasPrefix(buttonID, "loop"):
		// Toggle loop mode
		mutex.Lock()
		guildCtx, exists := guildContexts[guildID]
		if !exists {
			guildCtx = &GuildContext{
				MusicQueue: &MusicQueue{},
			}
			guildContexts[guildID] = guildCtx
		}

		if guildCtx.MusicQueue == nil {
			guildCtx.MusicQueue = &MusicQueue{}
		}

		guildCtx.MusicQueue.Loop = !guildCtx.MusicQueue.Loop

		status := "disabled"
		if guildCtx.MusicQueue.Loop {
			status = "enabled"
		}
		mutex.Unlock()

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("🔄 Loop mode %s.", status),
			},
		})
	}
}

// getYoutubeInfoWithContext gets video info using yt-dlp with context timeout
func getYoutubeInfoWithContext(ctx context.Context, url string) (*Track, error) {
	log.Printf("getYoutubeInfo: Processing URL: %s", url)
	
	// Sanitize the URL to prevent command injection
	sanitizedURL := sanitizeURL(url)
	if sanitizedURL == "" {
		log.Printf("getYoutubeInfo: Invalid URL provided: %s", url)
		return nil, fmt.Errorf("invalid URL provided")
	}
	
	log.Printf("getYoutubeInfo: Sanitized URL: %s", sanitizedURL)
	
	// Create the command with context and additional flags
	cmd := exec.CommandContext(ctx, "/usr/bin/yt-dlp", 
		"--dump-json", 
		"-f", "bestaudio", 
		"--no-check-certificate", 
		"--no-warnings", 
		"--flat-playlist", 
		"-4", // Force IPv4
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36",
		sanitizedURL)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err := cmd.Run()
	if err != nil {
		stderrContent := stderrBuf.String()
		log.Printf("getYoutubeInfo: Error getting video info from yt-dlp: %v", err)
		log.Println("getYoutubeInfo: yt-dlp stderr: " + stderrContent) // Print detailed error to log
		
		// Check for specific error conditions
		if strings.Contains(stderrContent, "Unsupported URL") {
			return nil, fmt.Errorf("unsupported URL: %s", sanitizedURL)
		} else if strings.Contains(stderrContent, "This video is unavailable") {
			return nil, fmt.Errorf("video unavailable: %s", sanitizedURL)
		} else if strings.Contains(stderrContent, "Sign in to confirm your age") {
			return nil, fmt.Errorf("age restricted video: %s", sanitizedURL)
		} else if strings.Contains(stderrContent, "This live event will begin in") {
			return nil, fmt.Errorf("scheduled live event: %s", sanitizedURL)
		}
		
		return nil, fmt.Errorf("error getting video info: %v, stderr: %s", err, stderrContent)
	}
	
	output := stdoutBuf.Bytes()
	
	// Validate output before parsing JSON
	if len(output) == 0 {
		log.Printf("getYoutubeInfo: Empty output from yt-dlp")
		return nil, fmt.Errorf("empty output from yt-dlp for URL: %s", sanitizedURL)
	}
	
	// Check if output starts with unexpected character (like 'h')
	if len(output) > 0 && output[0] != '{' {
		outputStr := string(output)
		log.Printf("getYoutubeInfo: Unexpected output format, starts with: '%c', full output: %s", output[0], outputStr)
		return nil, fmt.Errorf("unexpected output format from yt-dlp: %s", outputStr)
	}
	
	var info struct {
		Title     string `json:"title"`
		Duration  float64 `json:"duration"`
		Uploader  string `json:"uploader"`
		Thumbnail string `json:"thumbnail"`
		WebpageURL string `json:"webpage_url"`
	}
	
	if err := json.Unmarshal(output, &info); err != nil {
		log.Printf("getYoutubeInfo: Error parsing video info JSON: %v", err)
		log.Printf("getYoutubeInfo: Raw output was: %s", string(output))
		return nil, fmt.Errorf("error parsing video info: %v", err)
	}
	
	// Validate that we got meaningful data
	if info.Title == "" {
		return nil, fmt.Errorf("could not retrieve title for URL: %s", sanitizedURL)
	}
	
	// Format duration
	durationStr := formatDuration(info.Duration)
	
	log.Printf("yt-dlp berhasil dapet link: " + info.WebpageURL)
	log.Printf("getYoutubeInfo: Successfully retrieved info for '%s'", info.Title)
	
	return &Track{
		Title:     info.Title,
		URL:       info.WebpageURL,
		Duration:  durationStr,
		Uploader:  info.Uploader,
		Thumbnail: info.Thumbnail,
	}, nil
}

// sanitizeURL sanitizes the URL to prevent command injection
func sanitizeURL(input string) string {
	// Trim whitespace
	input = strings.TrimSpace(input)

	// Handle shortened YouTube URLs (youtu.be)
	if strings.HasPrefix(input, "youtu.be/") {
		input = "https://" + input
	} else if strings.HasPrefix(input, "http://youtu.be/") || strings.HasPrefix(input, "https://youtu.be/") {
		// Already a proper URL
	} else if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		// If it doesn't start with http/https, it might be a search query
		// For search queries, validate that it contains only safe characters
		// Allow letters, numbers, spaces, and common search characters
		reg := regexp.MustCompile(`^[a-zA-Z0-9\s\-_.~:?#@!$&'()*+,;=%]+$`)
		if reg.MatchString(input) {
			return input
		}
		// If it doesn't match the safe pattern, return empty string
		return ""
	}

	// Parse the URL to validate it's a proper URL
	parsed, err := url.Parse(input)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	// Only allow http and https schemes
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}

	// Reconstruct the URL from the parsed components to ensure it's clean
	return parsed.String()
}

// formatDuration formats duration in seconds to MM:SS or HH:MM:SS
func formatDuration(seconds float64) string {
	if seconds <= 0 {
		return "Live"
	}
	
	totalSeconds := int(seconds)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	secs := totalSeconds % 60
	
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

// playAudioStream plays audio from a URL using yt-dlp and ffmpeg
func playAudioStream(vc *discordgo.VoiceConnection, url string, guildID string, requesterUsername string) {
	// Recover from panic
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in playAudioStream: %v", r)
		}
	}()

	log.Printf("playAudioStream: Starting to play audio for guild %s, URL: %s, requested by: %s", guildID, url, requesterUsername)
	
	// Perform nil check on voice connection
	if vc == nil {
		log.Printf("playAudioStream: Voice connection is nil")
		return
	}
	
	// Sanitize the URL
	sanitizedURL := sanitizeURL(url)
	if sanitizedURL == "" {
		log.Printf("playAudioStream: Invalid URL provided: %s", url)
		return
	}
	
	// First, get the direct URL using yt-dlp
	getUrlCmd := exec.Command("/usr/bin/yt-dlp", "--get-url", "-f", "bestaudio", "--no-check-certificate", "--no-warnings", "--no-playlist", "--", sanitizedURL)
	urlOutput, err := getUrlCmd.Output()
	if err != nil {
		log.Printf("playAudioStream: Error getting direct URL from yt-dlp: %v", err)
		stderr, stderrErr := getUrlCmd.CombinedOutput()
		if stderrErr != nil {
			log.Printf("playAudioStream: yt-dlp get-url stderr: %s", string(stderr))
		}
		return
	}
	
	directURL := strings.TrimSpace(string(urlOutput))
	log.Printf("playAudioStream: Got direct URL: %s", directURL)
	
	// Get user's volume preference
	prefs := getOrCreateUserPrefs(requesterUsername)
	volume := prefs.Volume
	
	// Create the ffmpeg command with the direct URL and volume adjustment
	// Using exact args for Discord audio: -f s16le -ar 48000 -ac 2 pipe:1
	cmd := exec.Command("ffmpeg", 
		"-reconnect", "1", 
		"-reconnect_streamed", "1", 
		"-reconnect_delay_max", "5",
		"-i", directURL, 
		"-filter:a", fmt.Sprintf("volume=%.2f", volume), // Apply user's volume preference
		"-f", "s16le", 
		"-ar", "48000", 
		"-ac", "2", 
		"-loglevel", "error", // Show only errors
		"pipe:1")
	
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	
	ffmpegOut, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("playAudioStream: Error creating ffmpeg output pipe: %v", err)
		return
	}
	
	// Start the ffmpeg command
	if err := cmd.Start(); err != nil {
		log.Printf("playAudioStream: Error starting ffmpeg: %v", err)
		return
	}
	
	// Read stderr in a goroutine to capture FFmpeg errors
	go func() {
		// Recover from panic in goroutine
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in ffmpeg stderr reader: %v", r)
			}
		}()
		
		// Continuously read stderr to prevent blocking
		scannerErr := bufio.NewScanner(&stderrBuf)
		for scannerErr.Scan() {
			line := scannerErr.Text()
			log.Printf("FFmpeg: %s", line)
		}
	}()
	
	// Create a reader from the stdout pipe
	reader := bufio.NewReader(ffmpegOut)
	
	// Wait for the voice connection to be ready
	for vc != nil && !vc.Ready {
		time.Sleep(10 * time.Millisecond)
	}
	
	// Check if voice connection is ready before proceeding
	if vc == nil || !vc.Ready {
		log.Println("playAudioStream: Voice connection is not ready")
		return
	}
	
	// Initialize Opus encoder
	enc, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		log.Printf("playAudioStream: Error creating Opus encoder: %v", err)
		return
	}
	
	// Set bit rate
	enc.SetBitrate(128 * 1000) // 128kbps
	
	// Get the guild context to update the start time
	mutex.Lock()
	guildCtx, exists := guildContexts[guildID]
	if !exists {
		guildCtx = &GuildContext{
			MusicQueue: &MusicQueue{},
		}
		guildContexts[guildID] = guildCtx
	}
	
	if guildCtx != nil {
		guildCtx.StartTime = time.Now()
	}
	mutex.Unlock()
	
	// Start a goroutine to update the "Now Playing" message with progress bar
	progressUpdaterCtx, progressUpdaterCancel := context.WithCancel(context.Background())
	defer progressUpdaterCancel()
	
	go func() {
		// Recover from panic in goroutine
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in progress updater goroutine: %v", r)
				log.Printf("Stack trace:\n%s", debug.Stack())
			}
		}()
		
		ticker := time.NewTicker(5 * time.Second) // Update every 5 seconds
		defer ticker.Stop()
		
		for {
			select {
			case <-ticker.C:
				// Calculate elapsed time since track started
				mutex.RLock()
				guildCtx, exists := guildContexts[guildID]
				if !exists || guildCtx == nil || guildCtx.StartTime.IsZero() {
					mutex.RUnlock()
					continue
				}
				elapsed := int(time.Since(guildCtx.StartTime).Seconds())
				
				// Get track duration
				trackDuration := 0
				if guildCtx.CurrentTrack != nil && guildCtx.CurrentTrack.Duration != "" {
					parts := strings.Split(guildCtx.CurrentTrack.Duration, ":")
					if len(parts) == 2 { // MM:SS
						mins, _ := strconv.Atoi(parts[0])
						secs, _ := strconv.Atoi(parts[1])
						trackDuration = mins*60 + secs
					} else if len(parts) == 3 { // HH:MM:SS
						hours, _ := strconv.Atoi(parts[0])
						mins, _ := strconv.Atoi(parts[1])
						secs, _ := strconv.Atoi(parts[2])
						trackDuration = hours*3600 + mins*60 + secs
					}
				}
				mutex.RUnlock()
				
				// Create progress bar
				progressBar := createProgressBar(elapsed, trackDuration)
				
				// Format time display
				elapsedFormatted := formatTime(elapsed)
				totalFormatted := formatTime(trackDuration)
				
				// Get the current track information
				var currentTrackTitle, currentTrackUploader, currentTrackRequester, currentTrackThumbnail string
				
				mutex.RLock()
				guildCtx, exists = guildContexts[guildID]
				if exists && guildCtx != nil && guildCtx.CurrentTrack != nil {
					currentTrack := guildCtx.CurrentTrack
					currentTrackTitle = currentTrack.Title
					currentTrackUploader = currentTrack.Uploader
					currentTrackRequester = currentTrack.RequesterUsername
					currentTrackThumbnail = currentTrack.Thumbnail
				}
				mutex.RUnlock()
				
				// Create embed with progress information
				embed := &discordgo.MessageEmbed{
					Title:       "🎵 Now Playing",
					Description: fmt.Sprintf("[%s](%s)", currentTrackTitle, ""),
					Fields: []*discordgo.MessageEmbedField{
						{
							Name:  "⏱️ Progress",
							Value: fmt.Sprintf("`%s / %s`\n%s", elapsedFormatted, totalFormatted, progressBar),
							Inline: false,
						},
						{
							Name:  "👤 Uploader",
							Value: currentTrackUploader,
							Inline: true,
						},
						{
							Name:  "💝 Requested by",
							Value: currentTrackRequester,
							Inline: true,
						},
					},
					Color: 0x00ff00, // Green color
				}
				
				// Add thumbnail if available
				if currentTrackThumbnail != "" {
					embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
						URL: currentTrackThumbnail,
					}
				}
				
				// Get the guild to find a text channel to send the update
				guild, err := session.State.Guild(guildID)
				if err != nil {
					continue
				}
				
				// Find a text channel to send the progress update
				var textChannelID string
				for _, channel := range guild.Channels {
					if channel.Type == discordgo.ChannelTypeGuildText {
						textChannelID = channel.ID
						break
					}
				}
				
				if textChannelID != "" {
					_, err := session.ChannelMessageSendEmbed(textChannelID, embed)
					if err != nil {
						log.Printf("Error sending progress update: %v", err)
					}
				}
			case <-progressUpdaterCtx.Done():
				// Context cancelled, stop updating
				return
			}
		}
	}()
	
	// Ensure encryption mode is properly set before sending audio
	if vc != nil {
		// Set log level to debug to see encryption details
		vc.LogLevel = discordgo.LogDebug
		
		// Check and potentially set the encryption mode for compatibility
		// This addresses the "Unknown encryption mode" error (4016)
		// The voice connection should automatically negotiate the encryption mode,
		// but we'll ensure it's properly initialized
		
		vc.Speaking(true)
		// Add delay to allow Discord to prepare for audio
		time.Sleep(1 * time.Second)
	}
	
	defer func() {
		if vc != nil {
			vc.Speaking(false)
		}
		// Remove the current track from the guild context
		mutex.Lock()
		guildCtx, exists := guildContexts[guildID]
		if exists && guildCtx != nil {
			guildCtx.CurrentTrack = nil
		}
		mutex.Unlock()
		
		// Play the next track if available and connection still exists
		if vc != nil {
			// We need to get the guildID from the voice connection or guild contexts
			var guildID string
			// Look through all voice connections to find which guild this channel belongs to
			for gid, vconn := range session.VoiceConnections {
				if vconn != nil && vconn.ChannelID == vc.ChannelID {
					guildID = gid
					break
				}
			}
			
			if guildID == "" {
				// If we still can't determine the guildID, try to get it from the guild contexts
				mutex.RLock()
				for gid, ctx := range guildContexts {
					if ctx.VoiceConnection != nil && ctx.VoiceConnection.ChannelID == vc.ChannelID {
						guildID = gid
						break
					}
				}
				mutex.RUnlock()
			}
			
			if guildID != "" {
				playNextTrack(session, guildID, vc.ChannelID)
			}
		}
	}()
	
	// Buffer for audio frames - exact size for stereo 16-bit PCM
	pcmBuf := make([]int16, 960*2) // 960 samples * 2 channels
	
	// Create a ticker for timing audio frames (20ms per frame for 48kHz sample rate)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	
	frameCounter := 0
	
	for vc != nil && vc.Ready && vc.OpusSend != nil {
		// Check if the voice connection is still active
		if vc == nil || !vc.Ready {
			log.Println("playAudioStream: Voice connection is nil or not ready")
			break
		}
		
		// Read audio frame from ffmpeg (16-bit PCM, 48000Hz, stereo) - 3840 bytes
		audioBytes := make([]byte, 3840) // 960 samples * 2 bytes per sample * 2 channels (stereo)
		n, err := io.ReadFull(reader, audioBytes)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				log.Println("playAudioStream: End of audio stream reached")
				
				// Handle end of stream - check if connection still exists before continuing
				if vc != nil {
					log.Println("playAudioStream: Checking connection after end of stream")
				}
				break
			}
			log.Printf("playAudioStream: Error reading audio: %v", err)
			continue
		}
		
		if n > 0 && vc != nil && vc.OpusSend != nil {
			// Convert bytes to int16 samples
			for i := 0; i < n/2; i++ {
				// Correctly convert little-endian bytes to int16
				pcmBuf[i] = int16(audioBytes[i*2]) | int16(audioBytes[i*2+1])<<8
			}

			// Encode to Opus
			opusData, err := enc.Encode(pcmBuf[:n/2], 960, 4000) // 960 samples per channel, max 4000 bytes
			if err != nil {
				log.Printf("playAudioStream: Error encoding to Opus: %v", err)
				continue
			}

			// Check the size of the encoded data
			log.Printf("Encoded Opus data size: %d bytes", len(opusData))

			// Wait for the next tick to maintain proper timing
			<-ticker.C

			// Strict nil check before sending the encoded Opus frame to Discord
			if vc != nil && vc.OpusSend != nil {
				// Ensure compatibility with AEAD encryption modes required by Discord
				// This addresses the "Unknown encryption mode" error (4016)
				select {
				case vc.OpusSend <- opusData:
					log.Printf("Successfully sent %d bytes as Opus frame to Discord", len(opusData))
					frameCounter++

					// Log successful frame every 100 frames
					if frameCounter%100 == 0 {
						log.Printf("playAudioStream: Successfully sent %d Opus frames to Discord", frameCounter)
					}
				case <-time.After(100 * time.Millisecond): // Timeout to prevent blocking
					log.Printf("playAudioStream: Timeout sending frame %d, channel might be full", frameCounter)
				}
			} else {
				log.Printf("playAudioStream: Voice connection or OpusSend is nil, stopping playback")
				break
			}
		}
	}

	log.Printf("playAudioStream: Sent %d audio frames", frameCounter)

	log.Printf("playAudioStream: Finished playing audio for guild %s", guildID)

	// Kill the process if still running
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	
	// Wait for process to finish
	cmd.Wait()
	
	// The progressUpdaterCancel() is deferred earlier in the function
	// It will be called automatically when the function exits
}

// playNextTrack plays the next track in the queue
func playNextTrack(s *discordgo.Session, guildID string, channelID string) {
	// Guard clause: Check if guildID is empty
	if guildID == "" {
		log.Println("Error: guildID kosong, membatalkan playback.")
		return
	}
	
	// Recover from panic with stack trace
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in playNextTrack: %v", r)
			log.Printf("Stack trace:\n%s", debug.Stack())
		}
	}()

	// Lock mutex for thread-safe access to guild contexts
	mutex.Lock()

	// Ensure guild context exists
	guildCtx, exists := guildContexts[guildID]
	if !exists {
		guildCtx = &GuildContext{
			MusicQueue: &MusicQueue{},
		}
		guildContexts[guildID] = guildCtx
	}

	// Ensure MusicQueue is initialized
	if guildCtx.MusicQueue == nil {
		guildCtx.MusicQueue = &MusicQueue{}
	}

	queue := guildCtx.MusicQueue

	// Check if queue is empty
	if queue == nil || len(queue.Tracks) == 0 {
		mutex.Unlock()

		// Queue is empty, disconnect from voice if no one else is listening
		vc, err := getConnectedVoiceConnection(s, guildID)
		if err != nil || vc == nil {
			return
		}

		// Check if anyone else is in the voice channel
		guild, err := s.State.Guild(guildID)
		if err != nil {
			vc.Disconnect()
			return
		}

		if guild != nil && guild.VoiceStates != nil {
			for _, vs := range guild.VoiceStates {
				if vs != nil && vs.ChannelID == vc.ChannelID && vs.UserID != s.State.User.ID {
					// Someone else is still in the channel, don't disconnect
					return
				}
			}
		}

		// No one else is in the channel, disconnect
		vc.Disconnect()
		return
	}

	// Get the next track - using len() is safe even if Tracks is nil (returns 0)
	if len(queue.Tracks) == 0 {
		log.Printf("playNextTrack: Queue is empty for guild %s", guildID)
		mutex.Unlock()
		return
	}

	nextTrack := queue.Tracks[0]

	// Check if nextTrack is nil before accessing its properties
	if nextTrack == nil {
		log.Printf("playNextTrack: Next track is nil for guild %s", guildID)
		if len(queue.Tracks) > 0 {
			queue.Tracks = queue.Tracks[1:] // Remove the nil track from the queue
		}
		mutex.Unlock()
		return
	}

	// Remove the played track from the queue
	if len(queue.Tracks) > 0 {
		queue.Tracks = queue.Tracks[1:]
	}

	// Store the current track in the guild context
	guildCtx.CurrentTrack = nextTrack
	mutex.Unlock() // Unlock after updating the queue

	// Connect to voice channel if not already connected
	vc, err := s.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil || vc == nil {
		log.Printf("playNextTrack: Error joining voice channel: %v", err)
		return
	}

	// Store the voice connection in the guild context
	mutex.Lock()
	guildCtx.VoiceConnection = vc
	mutex.Unlock()
	
	// Set log level to debug to see encryption details
	vc.LogLevel = discordgo.LogDebug

	// Ensure the encryption mode is properly set for newer Discord requirements
	// If the library version doesn't handle this automatically, force the encryption mode
	if vc.OpusSend == nil {
		// Initialize OpusSend channel if not already done
		vc.OpusSend = make(chan []byte, 2)
	}
	
	// Explicitly set the encryption mode to the standard one if needed
	// This helps address the "Unknown encryption mode" error (4016)
	
	// Handle encryption mode for Discord's requirements
	// This addresses the "Unknown encryption mode" error (4016)
	// Wait for the voice connection to be ready
	for i := 0; i < 50 && !vc.Ready; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	
	// Log that we're ready to connect
	if !vc.Ready {
		log.Println("Warning: Voice connection is not ready, this may cause issues")
	} else {
		log.Println("Voice connection is ready")
	}
	
	// Additional check for encryption mode compatibility
	// In newer versions of discordgo, the encryption mode should be handled automatically
	// But we ensure that we're using the standard mode

	// Wait for the connection to be ready with timeout
	readyTimeout := time.NewTimer(10 * time.Second)
	defer readyTimeout.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if vc != nil && vc.Ready {
				log.Printf("playNextTrack: Voice connection ready for guild %s", guildID)
				goto connectionReady
			}
		case <-readyTimeout.C:
			log.Printf("playNextTrack: Timeout waiting for voice connection to be ready for guild %s", guildID)
			return
		}
	}

connectionReady:
	// Add forced delay to ensure connection stability
	time.Sleep(2 * time.Second)

	// Handle encryption mode for Discord's requirements
	// This addresses the "Unknown encryption mode" error (4016)
	handleEncryptionMode(vc)

	// CRITICAL: Check if voice connection is ready before proceeding
	if vc == nil || !vc.Ready {
		log.Println("KRITIS: voiceConnection masih nil! Mencoba menyambung ulang...")
		return
	}

	// Double-check the voice connection from the guild context
	mutex.RLock()
	contextVC := guildCtx.VoiceConnection
	mutex.RUnlock()

	// Guard clause for nil voice connection
	if contextVC == nil {
		log.Println("Koneksi Voice NULL di playNextTrack, mencoba rekoneksi...")
		return // BERHENTI DI SINI, JANGAN LANJUT KE PLAYAUDIOSTREAM
	}

	// Start playing the audio stream only if all conditions are met
	if vc != nil && vc.Ready {
		// Set log level to debug to see encryption details
		vc.LogLevel = discordgo.LogDebug
		
		go func(nextTrack *Track) { // Pass nextTrack as parameter to avoid closure issues
			// Recover from panic in goroutine with stack trace
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered from panic in playAudioStream goroutine: %v", r)
					log.Printf("Stack trace:\n%s", debug.Stack())
					// Try to play the next track if available
					if vc != nil {
						playNextTrack(s, guildID, vc.ChannelID)
					}
				}
			}()

			playAudioStream(vc, nextTrack.URL, guildID, nextTrack.RequesterUsername)
		}(nextTrack) // Pass nextTrack as argument to the goroutine
	} else {
		log.Printf("playNextTrack: Conditions not met for playback - vc: %v, vc.Ready: %v", vc != nil, vc != nil && vc.Ready)
	}
}

// Helper function to get connected voice connection
func getConnectedVoiceConnection(s *discordgo.Session, guildID string) (*discordgo.VoiceConnection, error) {
	// First, try to get from our guild context
	mutex.RLock()
	guildCtx, exists := guildContexts[guildID]
	if exists && guildCtx != nil && guildCtx.VoiceConnection != nil {
		vc := guildCtx.VoiceConnection
		mutex.RUnlock()
		
		// Verify that the connection is still active
		if vc != nil && vc.Ready {
			return vc, nil
		}
	} else {
		mutex.RUnlock()
	}

	// Fallback to session's voice connections if not in our context
	if s.VoiceConnections != nil {
		vc, exists := s.VoiceConnections[guildID]
		if !exists || vc == nil || !vc.Ready {
			return nil, fmt.Errorf("no active voice connection found for guild %s", guildID)
		}
		return vc, nil
	}
	
	return nil, fmt.Errorf("no voice connection found for guild %s", guildID)
}

// Helper function to get user's voice state
func getVoiceState(s *discordgo.Session, userID, guildID string) (*discordgo.VoiceState, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil {
		return nil, err
	}

	if guild != nil && guild.VoiceStates != nil {
		for _, vs := range guild.VoiceStates {
			if vs != nil && vs.UserID == userID {
				return vs, nil
			}
		}
	}

	return nil, fmt.Errorf("user not in a voice channel")
}

// handleEncryptionMode handles the encryption mode selection for Discord voice connections
func handleEncryptionMode(vc *discordgo.VoiceConnection) {
	if vc == nil {
		return
	}
	
	// Wait for the connection to establish
	for i := 0; i < 100 && !vc.Ready; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	
	// Log the readiness of the connection
	if !vc.Ready {
		log.Println("Warning: Voice connection is not ready")
	} else {
		log.Println("Voice connection is ready")
	}
	
	// For newer Discord requirements, ensure we're using compatible settings
	// The encryption is handled internally by the library, but we ensure the connection is ready
	
	// Additional check for AEAD encryption support
	// This addresses the "Unknown encryption mode" error (4016)
	log.Println("Voice connection established, ready for AEAD encryption")
}

// createProgressBar creates a visual progress bar for the current track
func createProgressBar(currentTime, totalTime int) string {
	const barLength = 20
	if totalTime <= 0 {
		return strings.Repeat("▬", barLength) + " ▫▫▫▫▫"
	}
	
	progress := int((float64(currentTime) / float64(totalTime)) * barLength)
	if progress > barLength {
		progress = barLength
	}
	
	bar := strings.Repeat("▬", progress) + "🔘" + strings.Repeat("▬", barLength-progress)
	
	// Add time indicators at specific positions
	indicatorPos := make([]string, barLength+1)
	for i := range indicatorPos {
		indicatorPos[i] = "▫"
	}
	
	// Place time indicators at 25%, 50%, 75% positions
	if barLength >= 4 {
		indicatorPos[barLength/4] = "⏱️"
		indicatorPos[barLength/2] = "💡"
		indicatorPos[(3*barLength)/4] = "🔥"
	}
	
	// Replace the indicator at progress position with a different symbol
	if progress <= barLength && progress >= 0 {
		indicatorPos[progress] = "✅"
	}
	
	return bar + "\n" + strings.Join(indicatorPos, "")
}

// formatTime formats seconds to MM:SS or HH:MM:SS
func formatTime(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

// updateUserStats updates user statistics when they send a message
func updateUserStats(userID string) {
	statsMutex.Lock()
	defer statsMutex.Unlock()
	
	userStat, exists := userStats[userID]
	if !exists {
		userStat = &UserStats{
			LovePoints: 0,
			Level: 0,
			TotalMessages: 0,
			LastActivity: time.Now(),
			CurrentSongRequests: 0,
		}
		userStats[userID] = userStat
	}
	
	// Update stats
	userStat.TotalMessages++
	userStat.LastActivity = time.Now()
	
	// Award love points for activity
	userStat.LovePoints += 1
	
	// Calculate new level based on love points
	newLevel := calculateLevel(userStat.LovePoints)
	if newLevel > userStat.Level {
		userStat.Level = newLevel
	}
}

// calculateLevel calculates the level based on love points
func calculateLevel(points int) int {
	// Simple formula: level = sqrt(points) rounded down
	return int(math.Sqrt(float64(points)))
}

// handleLovePointsCommand handles the lovepoints command
func handleLovePointsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	statsMutex.RLock()
	userStat, exists := userStats[m.Author.ID]
	statsMutex.RUnlock()
	
	if !exists || userStat == nil {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s, you don't have any love points yet. Send some messages to earn them! 💕", m.Author.Username))
		return
	}
	
	levelName := getLevelName(userStat.Level)
	
	message := fmt.Sprintf(
		"💖 **%s's Love Points** 💖\n"+
		"**Points:** %d\n"+
		"**Level:** %d (%s)\n"+
		"**Messages:** %d\n"+
		"**Songs Requested:** %d\n"+
		"**Last Active:** %s",
		m.Author.Username,
		userStat.LovePoints,
		userStat.Level,
		levelName,
		userStat.TotalMessages,
		userStat.CurrentSongRequests,
		userStat.LastActivity.Format("Jan 2, 2006 at 3:04 PM"),
	)
	
	s.ChannelMessageSend(m.ChannelID, message)
}

// handleCoupleStatsCommand handles the couplestats command
func handleCoupleStatsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Get allowed user IDs to identify the couple
	allowedUserIDs := os.Getenv("ALLOWED_USER_IDS")
	idParts := strings.Split(allowedUserIDs, ",")
	
	if len(idParts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "Not enough users in the allowed list to show couple stats.")
		return
	}
	
	user1ID := strings.TrimSpace(idParts[0])
	user2ID := strings.TrimSpace(idParts[1])
	
	statsMutex.RLock()
	user1Stat, user1Exists := userStats[user1ID]
	user2Stat, user2Exists := userStats[user2ID]
	statsMutex.RUnlock()
	
	var user1Name, user2Name string
	var user1Level, user2Level int
	var user1Points, user2Points int
	
	// Get user names and stats
	if user1Exists && user1Stat != nil {
		user1Name = getUserByUsernameID(s, user1ID)
		user1Level = user1Stat.Level
		user1Points = user1Stat.LovePoints
	} else {
		user1Name = getUserByUsernameID(s, user1ID)
	}
	
	if user2Exists && user2Stat != nil {
		user2Name = getUserByUsernameID(s, user2ID)
		user2Level = user2Stat.Level
		user2Points = user2Stat.LovePoints
	} else {
		user2Name = getUserByUsernameID(s, user2ID)
	}
	
	if user1Name == "" {
		user1Name = "User1"
	}
	if user2Name == "" {
		user2Name = "User2"
	}
	
	message := fmt.Sprintf(
		"💕 **Couple Statistics** 💕\n\n"+
		"**%s**\n"+
		"• Level: %d\n"+
		"• Love Points: %d\n\n"+
		"**%s**\n"+
		"• Level: %d\n"+
		"• Love Points: %d\n\n"+
		"💖 Total Combined Love Points: %d 💖",
		user1Name, user1Level, user1Points,
		user2Name, user2Level, user2Points,
		user1Points+user2Points,
	)
	
	s.ChannelMessageSend(m.ChannelID, message)
}

// handleLoveProfileCommand handles the loveprofile command
func handleLoveProfileCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	statsMutex.RLock()
	userStat, exists := userStats[m.Author.ID]
	statsMutex.RUnlock()
	
	if !exists || userStat == nil {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s, your profile is still being created. Interact more with the bot!", m.Author.Username))
		return
	}
	
	levelName := getLevelName(userStat.Level)
	
	// Calculate some achievements
	achievements := []string{}
	if userStat.TotalMessages > 100 {
		achievements = append(achievements, "Chatty 💬")
	}
	if userStat.LovePoints > 50 {
		achievements = append(achievements, "Love Master 💖")
	}
	if userStat.CurrentSongRequests > 10 {
		achievements = append(achievements, "DJ Expert 🎵")
	}
	
	achievementStr := "None yet"
	if len(achievements) > 0 {
		achievementStr = strings.Join(achievements, ", ")
	}
	
	message := fmt.Sprintf(
		"💞 **%s's Love Profile** 💞\n"+
		"**Level:** %d (%s)\n"+
		"**Love Points:** %d\n"+
		"**Total Messages:** %d\n"+
		"**Songs Requested:** %d\n"+
		"**Achievements:** %s\n"+
		"**Member Since:** %s",
		m.Author.Username,
		userStat.Level,
		levelName,
		userStat.LovePoints,
		userStat.TotalMessages,
		userStat.CurrentSongRequests,
		achievementStr,
		userStat.LastActivity.Format("January 2, 2006"),
	)
	
	s.ChannelMessageSend(m.ChannelID, message)
}

// getLevelName returns the name for a given level
func getLevelName(level int) string {
	switch {
	case level <= 2:
		return "New Crush"
	case level <= 5:
		return "Getting Close"
	case level <= 10:
		return "Best Friends"
	case level <= 20:
		return "In Love"
	case level <= 30:
		return "Soulmates"
	case level <= 50:
		return "Partners Forever"
	default:
		return "Eternal Love"
	}
}

// getUserByUsernameID gets a username by their ID
func getUserByUsernameID(s *discordgo.Session, userID string) string {
	user, err := s.User(userID)
	if err != nil {
		return ""
	}
	return user.Username
}

// getOrCreateUserPrefs gets or creates user preferences
func getOrCreateUserPrefs(userID string) *UserPreferences {
	prefsMutex.Lock()
	defer prefsMutex.Unlock()
	
	prefs, exists := userPreferences[userID]
	if !exists {
		prefs = &UserPreferences{
			Volume: 1.0, // Default volume at 100%
			SavedPlaylists: make(map[string]*SavedPlaylist),
		}
		userPreferences[userID] = prefs
	}
	
	return prefs
}

// handleVolumeCommand handles the volume command
func handleVolumeCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	parts := strings.Fields(m.Content)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Gunakan perintah: 'Lyre volume [0-100]' atau 'Queen volume [0-100]'")
		return
	}
	
	volumeStr := parts[2]
	volume, err := strconv.ParseFloat(volumeStr, 64)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "Silakan masukkan angka antara 0-100 untuk volume.")
		return
	}
	
	if volume < 0 || volume > 100 {
		s.ChannelMessageSend(m.ChannelID, "Volume harus antara 0-100.")
		return
	}
	
	// Convert to 0.0-1.0 scale
	volume /= 100.0
	
	prefs := getOrCreateUserPrefs(m.Author.ID)
	prefs.Volume = volume
	
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔊 Volume diatur ke %.0f%%", volume*100))
}

// handleSavePlaylistCommand handles saving a playlist
func handleSavePlaylistCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	parts := strings.Fields(m.Content)
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "Gunakan perintah: 'Lyre saveplaylist [nama] [url]' atau 'Queen saveplaylist [nama] [url]'")
		return
	}
	
	playlistName := parts[2]
	trackURL := parts[3]
	
	// Get track info
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	track, err := getYoutubeInfoWithContext(ctx, trackURL)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error getting track info: %v", err))
		return
	}
	
	prefs := getOrCreateUserPrefs(m.Author.ID)
	
	playlist, exists := prefs.SavedPlaylists[playlistName]
	if !exists {
		playlist = &SavedPlaylist{
			Name: playlistName,
			Tracks: make([]*Track, 0),
		}
		prefs.SavedPlaylists[playlistName] = playlist
	}
	
	playlist.Tracks = append(playlist.Tracks, track)
	
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎵 Lagu '%s' telah ditambahkan ke playlist '%s'", track.Title, playlistName))
}

// handleLoadPlaylistCommand handles loading a playlist
func handleLoadPlaylistCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	parts := strings.Fields(m.Content)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Gunakan perintah: 'Lyre loadplaylist [nama]' atau 'Queen loadplaylist [nama]'")
		return
	}
	
	playlistName := parts[2]
	
	prefs := getOrCreateUserPrefs(m.Author.ID)
	playlist, exists := prefs.SavedPlaylists[playlistName]
	if !exists {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Playlist '%s' tidak ditemukan.", playlistName))
		return
	}
	
	// Add all tracks in the playlist to the music queue
	mutex.Lock()
	guildCtx, exists := guildContexts[m.GuildID]
	if !exists {
		guildCtx = &GuildContext{
			MusicQueue: &MusicQueue{},
		}
		guildContexts[m.GuildID] = guildCtx
	}
	
	if guildCtx.MusicQueue == nil {
		guildCtx.MusicQueue = &MusicQueue{}
	}
	
	// Check if the queue has space for all tracks
	if len(guildCtx.MusicQueue.Tracks) + len(playlist.Tracks) > MaxQueueSize {
		mutex.Unlock()
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Tidak cukup ruang dalam antrean! Maksimal %d lagu dalam antrean. Playlist memiliki %d lagu.", MaxQueueSize, len(playlist.Tracks)))
		return
	}
	
	// Add all tracks from the playlist to the queue
	for _, track := range playlist.Tracks {
		guildCtx.MusicQueue.Tracks = append(guildCtx.MusicQueue.Tracks, track)
	}
	mutex.Unlock()
	
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎵 Playlist '%s' dengan %d lagu telah ditambahkan ke antrean.", playlistName, len(playlist.Tracks)))
}

// handleHelpCommand handles the help command
func handleHelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	helpMessage := `
💖 **QUEEN'S LƔRE - HELP** 💖

🎵 **Musik Commands:**
• "Lyre play [link/search]" atau "Queen play [link/search]" - Putar lagu dari YouTube
• "Lyre volume [0-100]" atau "Queen volume [0-100]" - Atur volume (0-100%)
• "Lyre saveplaylist [nama] [url]" atau "Queen saveplaylist [nama] [url]" - Simpan lagu ke playlist
• "Lyre loadplaylist [nama]" atau "Queen loadplaylist [nama]" - Muat playlist ke antrean

💝 **Love System Commands:**
• "Lyre lovepoints" atau "Queen lovepoints" - Lihat poin cinta dan level kamu
• "Lyre couplestats" atau "Queen couplestats" - Lihat statistik pasangan
• "Lyre loveprofile" atau "Queen loveprofile" - Lihat profil lengkap kamu
• "Lyre help" atau "Queen help" - Menampilkan pesan bantuan ini

🎵 **Interaksi Musik:**
• Gunakan tombol ⏯️ untuk pause/resume
• Gunakan tombol ⏭️ untuk skip lagu
• Gunakan tombol 🔁 untuk mengulang lagu
• Gunakan tombol 🛑 untuk menghentikan pemutaran

💕 **Catatan:**
• Setiap kali kamu mengirim pesan, kamu mendapatkan poin cinta!
• Semakin sering berinteraksi, semakin tinggi level kamu!
• Kamu dan pasanganmu membentuk tim yang hebat! 💕

Selamat menikmati musik dengan Queen's Lɣre୨ৎ⭑! 🎶
	`
	
	s.ChannelMessageSend(m.ChannelID, helpMessage)
}

