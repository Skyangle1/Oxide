package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"layeh.com/gopus"
)

// Global variables
var (
	// Session for Discord
	session *discordgo.Session
	
	// Music queues for each guild
	musicQueues = make(map[string]*MusicQueue)
	
	// Currently playing tracks
	currentTracks = make(map[string]*Track)
	
	// Voice connections map
	voiceConnections = make(map[string]*discordgo.VoiceConnection)
	
	// Mutex for thread-safe operations
	mutex sync.RWMutex
	
	// Allowed users for exclusive access
	AllowedUsers map[string]bool
)

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

// MusicQueue represents a queue of tracks
type MusicQueue struct {
	Tracks []*Track
	Loop   bool
}

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

	fmt.Println("Oxide Music Bot is now running. Press CTRL+C to exit.")
	
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

	// Check if the message author is in the allowed users list
	if !AllowedUsers[m.Author.ID] {
		return
	}

	// Convert message to lowercase for case-insensitive comparison
	lowerContent := strings.ToLower(m.Content)

	// Check if the message starts with "lyre play" or "queen play" (case insensitive)
	if strings.HasPrefix(lowerContent, "lyre play") || strings.HasPrefix(lowerContent, "queen play") {
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
			queue, exists := musicQueues[m.GuildID]
			if !exists {
				log.Printf("Creating new queue for guild %s", m.GuildID)
				queue = &MusicQueue{}
				musicQueues[m.GuildID] = queue
			}

			queue.Tracks = append(queue.Tracks, track)
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
				
				// Wait for the connection to be ready
				for !vc.Ready {
					time.Sleep(100 * time.Millisecond)
				}
			}

			// If nothing is currently playing, start playback
			if _, playing := currentTracks[m.GuildID]; !playing {
				log.Printf("Nothing playing, starting playback for guild %s", m.GuildID)
				playNextTrack(s, &discordgo.InteractionCreate{}, voiceState.ChannelID)
				
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Now playing: `%s`", track.Title))
			} else {
				// Send a message that the track was added to the queue
				log.Printf("Track added to queue, currently playing another track in guild %s", m.GuildID)
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Added `%s` to the queue.", track.Title))
			}
		}()
	} else if strings.HasPrefix(lowerContent, "lyre") || strings.HasPrefix(lowerContent, "queen") {
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
				s.ChannelMessageSend(m.ChannelID, "Hai sayangku! Aku di sini untukmu. Mau dengerin lagu romantis bareng aku? ୨ৎ⭑")
			} else {
				s.ChannelMessageSend(m.ChannelID, "I am here, My Queen. Apa ada melodi yang ingin diputar? ୨ৎ⭑")
			}
			return
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
	log.Println("Perintah /play diterima dari: " + username)
	
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

	// Create or get the music queue for this guild
	mutex.Lock()
	queue, exists := musicQueues[i.GuildID]
	if !exists {
		log.Printf("Creating new queue for guild %s", i.GuildID)
		queue = &MusicQueue{}
		musicQueues[i.GuildID] = queue
	}

	queue.Tracks = append(queue.Tracks, track)
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
		
		// Wait for the connection to be ready
		for !vc.Ready {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// If nothing is currently playing, start playback
	if _, playing := currentTracks[i.GuildID]; !playing {
		log.Printf("Nothing playing, starting playback for guild %s", i.GuildID)
		playNextTrack(s, i, voiceState.ChannelID)
		
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
	delete(currentTracks, i.GuildID)
	
	// Play the next track
	playNextTrack(s, i, voiceState.ChannelID)
	
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
	queue, exists := musicQueues[i.GuildID]
	if exists {
		queue.Tracks = nil
	}
	
	delete(currentTracks, i.GuildID)
	
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

	queue, exists := musicQueues[i.GuildID]
	if !exists || len(queue.Tracks) == 0 {
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
	
	for idx, track := range queue.Tracks {
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

	currentTrack, exists := currentTracks[i.GuildID]
	if !exists {
		_, err := s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Content: "Nothing is currently playing.",
		})
		if err != nil {
			log.Printf("Error sending follow-up message: %v", err)
		}
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Now Playing",
		Description: fmt.Sprintf("[%s](%s)", currentTrack.Title, currentTrack.URL),
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:  "Duration",
				Value: currentTrack.Duration,
				Inline: true,
			},
			{
				Name:  "Uploader",
				Value: currentTrack.Uploader,
				Inline: true,
			},
			{
				Name:  "Requested by",
				Value: currentTrack.RequesterUsername,
				Inline: true,
			},
		},
		Color: 0x00ff00, // Green color
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
					Emoji: discordgo.ComponentEmoji{Name: "⏯"},
					Style: discordgo.PrimaryButton,
					CustomID: "pause_resume_" + i.GuildID,
				},
				discordgo.Button{
					Emoji: discordgo.ComponentEmoji{Name: "⏭"},
					Style: discordgo.PrimaryButton,
					CustomID: "skip_" + i.GuildID,
				},
				discordgo.Button{
					Emoji: discordgo.ComponentEmoji{Name: "⏹"},
					Style: discordgo.DangerButton,
					CustomID: "stop_" + i.GuildID,
				},
				discordgo.Button{
					Emoji: discordgo.ComponentEmoji{Name: "🔁"},
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
		delete(currentTracks, guildID)
		
		// Play the next track
		playNextTrack(s, &discordgo.InteractionCreate{
			Interaction: i.Interaction,
		}, voiceState.ChannelID)
		
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: "Skipped the current track.",
			},
		})
	case strings.HasPrefix(buttonID, "stop"):
		// Stop playback and clear the queue
		queue, exists := musicQueues[guildID]
		if exists {
			queue.Tracks = nil
		}
		
		delete(currentTracks, guildID)
		
		vc.Disconnect()
		
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: "Stopped playback and cleared the queue.",
			},
		})
	case strings.HasPrefix(buttonID, "loop"):
		// Toggle loop mode
		queue, exists := musicQueues[guildID]
		if !exists {
			queue = &MusicQueue{}
			musicQueues[guildID] = queue
		}
		
		queue.Loop = !queue.Loop
		
		status := "disabled"
		if queue.Loop {
			status = "enabled"
		}
		
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("Loop mode %s.", status),
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
		// For search queries, we'll return as-is to be handled by yt-dlp
		return input
	}
	
	// Only allow alphanumeric characters, hyphens, underscores, periods, slashes, colons, and question marks
	reg := regexp.MustCompile(`[^a-zA-Z0-9\-_.~:/?#\[\]@!$&'()*+,;=%]+`)
	sanitized := reg.ReplaceAllString(input, "")
	
	return sanitized
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
	
	// Create the ffmpeg command with the direct URL
	// Using exact args for Discord audio: -f s16le -ar 48000 -ac 2 pipe:1
	cmd := exec.Command("ffmpeg", 
		"-reconnect", "1", 
		"-reconnect_streamed", "1", 
		"-reconnect_delay_max", "5",
		"-i", directURL, 
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
	
	// Send audio to Discord
	if vc != nil {
		vc.Speaking(true)
		// Add delay to allow Discord to prepare for audio
		time.Sleep(250 * time.Millisecond)
	}
	
	defer func() {
		if vc != nil {
			vc.Speaking(false)
		}
		// Remove the current track from the map
		mutex.Lock()
		delete(currentTracks, guildID)
		mutex.Unlock()
		
		// Play the next track if available and connection still exists
		if vc != nil {
			playNextTrack(session, &discordgo.InteractionCreate{}, vc.ChannelID)
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

			// Send the encoded Opus frame to Discord
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
}

// playNextTrack plays the next track in the queue
func playNextTrack(s *discordgo.Session, i *discordgo.InteractionCreate, channelID string) {
	// Recover from panic
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in playNextTrack: %v", r)
		}
	}()

	var guildID string
	if i != nil {
		guildID = i.GuildID
	} else {
		// If called from inside playAudioStream, we need to determine the guildID differently
		// This is a simplified approach - in practice, you'd need to track this differently
		return
	}
	
	// Validate guildID
	if guildID == "" {
		log.Printf("playNextTrack: Invalid guildID provided")
		return
	}
	
	mutex.Lock()
	queue, exists := musicQueues[guildID]
	if !exists || len(queue.Tracks) == 0 {
		mutex.Unlock()
		// Queue is empty, disconnect from voice if no one else is listening
		if vc, err := getConnectedVoiceConnection(s, guildID); err == nil && vc != nil {
			// Check if anyone else is in the voice channel
			guild, err := s.State.Guild(guildID)
			if err != nil {
				vc.Disconnect()
				return
			}
			
			for _, vs := range guild.VoiceStates {
				if vs != nil && vs.ChannelID == vc.ChannelID && vs.UserID != s.State.User.ID {
					// Someone else is still in the channel, don't disconnect
					return
				}
			}
			
			// No one else is in the channel, disconnect
			vc.Disconnect()
		}
		return
	}

	// Ensure queue is not empty before accessing tracks
	if len(queue.Tracks) == 0 {
		log.Printf("playNextTrack: Queue is empty for guild %s", guildID)
		mutex.Unlock()
		return
	}

	// Get the next track
	nextTrack := queue.Tracks[0]
	queue.Tracks = queue.Tracks[1:] // Remove the played track from the queue

	// Store the current track
	currentTracks[guildID] = nextTrack
	mutex.Unlock()

	// Connect to voice channel if not already connected
	vc, err := s.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil || vc == nil {
		log.Printf("playNextTrack: Error joining voice channel: %v", err)
		return
	}

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
	// Strict nil check before proceeding with audio streaming
	if vc == nil {
		log.Printf("playNextTrack: Voice connection is nil for guild %s", guildID)
		return
	}
	
	// Double-check that the connection is ready
	if !vc.Ready {
		log.Printf("playNextTrack: Voice connection is not ready for guild %s", guildID)
		return
	}

	// Verify that we have a valid track to play
	if nextTrack == nil {
		log.Printf("playNextTrack: Next track is nil for guild %s", guildID)
		return
	}

	// Start playing the audio stream only if all conditions are met
	if vc != nil && vc.Ready && nextTrack != nil {
		go func() {
			// Recover from panic in goroutine
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered from panic in playAudioStream goroutine: %v", r)
					// Try to play the next track if available
					if vc != nil {
						playNextTrack(s, &discordgo.InteractionCreate{}, vc.ChannelID)
					}
				}
			}()
			
			playAudioStream(vc, nextTrack.URL, guildID, nextTrack.RequesterUsername)
		}()
	} else {
		log.Printf("playNextTrack: Conditions not met for playback - vc: %v, vc.Ready: %v, nextTrack: %v", vc != nil, vc != nil && vc.Ready, nextTrack != nil)
	}
}

// Helper function to get user's voice state
func getVoiceState(s *discordgo.Session, userID, guildID string) (*discordgo.VoiceState, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil {
		return nil, err
	}

	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			return vs, nil
		}
	}

	return nil, fmt.Errorf("user not in a voice channel")
}

// Helper function to get connected voice connection
func getConnectedVoiceConnection(s *discordgo.Session, guildID string) (*discordgo.VoiceConnection, error) {
	vc, exists := s.VoiceConnections[guildID]
	if !exists || vc == nil {
		return nil, fmt.Errorf("no voice connection found for guild %s", guildID)
	}
	return vc, nil
}