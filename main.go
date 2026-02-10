package main

import (
	"bufio"
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
)

// Track represents a music track
type Track struct {
	Title       string
	URL         string
	Duration    string
	Uploader    string
	Thumbnail   string
	RequesterID string
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

	// Respond to ping
	if m.Content == "!ping" {
		s.ChannelMessageSend(m.ChannelID, "Pong!")
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
	log.Printf("handlePlayCommand: Received play command from user %s in guild %s", i.Member.User.Username, i.GuildID)
	
	// Defer the response to prevent timeout
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("handlePlayCommand: Error deferring interaction: %v", err)
		return
	}

	// Get the query from options
	query := i.ApplicationCommandData().Options[0].StringValue()
	log.Printf("handlePlayCommand: Processing query: %s", query)
	
	// Validate the URL to prevent command injection
	sanitizedQuery := sanitizeURL(query)
	if sanitizedQuery == "" {
		log.Printf("handlePlayCommand: Invalid URL provided by user %s: %s", i.Member.User.Username, query)
		editOriginalResponse(s, i, "Invalid URL provided. Please provide a valid URL or search query.")
		return
	}

	// Get the voice channel the user is connected to
	voiceState, err := getVoiceState(s, i.Member.User.ID, i.GuildID)
	if err != nil {
		log.Printf("handlePlayCommand: User %s not in voice channel: %v", i.Member.User.Username, err)
		editOriginalResponse(s, i, "You must be connected to a voice channel to use this command.")
		return
	}

	// Get track info using yt-dlp
	log.Printf("handlePlayCommand: Getting track info for: %s", sanitizedQuery)
	track, err := getYoutubeInfo(sanitizedQuery)
	if err != nil {
		log.Printf("handlePlayCommand: Error getting track info: %v", err)
		editOriginalResponse(s, i, fmt.Sprintf("Error getting track info: %v", err))
		return
	}
	
	// Set the requester
	track.RequesterID = i.Member.User.ID

	// Create or get the music queue for this guild
	mutex.Lock()
	queue, exists := musicQueues[i.GuildID]
	if !exists {
		log.Printf("handlePlayCommand: Creating new queue for guild %s", i.GuildID)
		queue = &MusicQueue{}
		musicQueues[i.GuildID] = queue
	}

	queue.Tracks = append(queue.Tracks, track)
	mutex.Unlock()

	log.Printf("handlePlayCommand: Added track '%s' to queue for guild %s", track.Title, i.GuildID)

	// If nothing is currently playing, start playback
	if _, playing := currentTracks[i.GuildID]; !playing {
		log.Printf("handlePlayCommand: Nothing playing, starting playback for guild %s", i.GuildID)
		playNextTrack(s, i, voiceState.ChannelID)
	} else {
		// Send a message that the track was added to the queue
		log.Printf("handlePlayCommand: Track added to queue, currently playing another track in guild %s", i.GuildID)
		editOriginalResponse(s, i, fmt.Sprintf("Added `%s` to the queue.", track.Title))
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
		editOriginalResponse(s, i, "You must be connected to a voice channel to use this command.")
		return
	}

	vc, err := getConnectedVoiceConnection(s, i.GuildID)
	if err != nil || vc == nil {
		editOriginalResponse(s, i, "Not connected to a voice channel.")
		return
	}

	if vc.ChannelID != voiceState.ChannelID {
		editOriginalResponse(s, i, "You must be in the same voice channel as the bot to use this command.")
		return
	}

	// Skip the current track
	delete(currentTracks, i.GuildID)
	
	// Play the next track
	playNextTrack(s, i, voiceState.ChannelID)
	
	editOriginalResponse(s, i, "Skipped the current track.")
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
		editOriginalResponse(s, i, "You must be connected to a voice channel to use this command.")
		return
	}

	vc, err := getConnectedVoiceConnection(s, i.GuildID)
	if err != nil || vc == nil {
		editOriginalResponse(s, i, "Not connected to a voice channel.")
		return
	}

	if vc.ChannelID != voiceState.ChannelID {
		editOriginalResponse(s, i, "You must be in the same voice channel as the bot to use this command.")
		return
	}

	// Clear the queue and disconnect from voice
	queue, exists := musicQueues[i.GuildID]
	if exists {
		queue.Tracks = nil
	}
	
	delete(currentTracks, i.GuildID)
	
	vc.Disconnect()
	
	editOriginalResponse(s, i, "Stopped playback and cleared the queue.")
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
		editOriginalResponse(s, i, "The queue is empty.")
		return
	}

	var queueMsg strings.Builder
	queueMsg.WriteString("**Music Queue:**\n")
	
	for idx, track := range queue.Tracks {
		queueMsg.WriteString(fmt.Sprintf("%d. %s - Requested by <@%s>\n", idx+1, track.Title, track.RequesterID))
	}
	
	editOriginalResponse(s, i, queueMsg.String())
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
		editOriginalResponse(s, i, "Nothing is currently playing.")
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
				Value: fmt.Sprintf("<@%s>", currentTrack.RequesterID),
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

	// Send the embed with buttons
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
		Components: &components,
	})
	if err != nil {
		log.Printf("Error editing interaction response: %v", err)
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
		// In a real implementation, this would pause/resume the audio stream
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

// Helper function to edit the original response
func editOriginalResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
	if err != nil {
		log.Printf("Error editing interaction response: %v", err)
	}
}

// getYoutubeInfo gets video info using yt-dlp
func getYoutubeInfo(url string) (*Track, error) {
	log.Printf("getYoutubeInfo: Processing URL: %s", url)
	
	// Sanitize the URL to prevent command injection
	sanitizedURL := sanitizeURL(url)
	if sanitizedURL == "" {
		log.Printf("getYoutubeInfo: Invalid URL provided: %s", url)
		return nil, fmt.Errorf("invalid URL provided")
	}
	
	cmd := exec.Command("/usr/bin/yt-dlp", "--dump-json", "-f", "bestaudio", "--no-check-certificate", "--no-warnings", "--extract-audio", "--audio-format", "mp3", sanitizedURL)
	
	output, err := cmd.Output()
	if err != nil {
		log.Printf("getYoutubeInfo: Error getting video info from yt-dlp: %v", err)
		// Try to get stderr for more details
		stderr, stderrErr := cmd.CombinedOutput()
		if stderrErr != nil {
			log.Printf("getYoutubeInfo: yt-dlp stderr: %s", string(stderr))
		}
		return nil, fmt.Errorf("error getting video info: %v, stderr: %s", err, string(stderr))
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
		return nil, fmt.Errorf("error parsing video info: %v", err)
	}
	
	// Format duration
	durationStr := formatDuration(info.Duration)
	
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
	// Only allow alphanumeric characters, hyphens, underscores, periods, slashes, colons, and question marks
	reg := regexp.MustCompile(`[^a-zA-Z0-9\-_.~:/?#\[\]@!$&'()*+,;=%]+`)
	sanitized := reg.ReplaceAllString(input, "")
	
	// Ensure it starts with http:// or https://
	if !strings.HasPrefix(sanitized, "http://") && !strings.HasPrefix(sanitized, "https://") {
		// If it doesn't start with http/https, it's likely not a valid URL
		// In a real implementation, you might want to handle search queries differently
		return ""
	}
	
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
func playAudioStream(vc *discordgo.VoiceConnection, url string, guildID string) {
	// Sanitize the URL
	sanitizedURL := sanitizeURL(url)
	if sanitizedURL == "" {
		log.Printf("Invalid URL provided: %s", url)
		return
	}
	
	// Create the yt-dlp and ffmpeg pipeline
	// yt-dlp gets the audio stream, ffmpeg converts it to the format Discord expects
	cmd := exec.Command("ffmpeg", "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5", 
		"-i", "-", "-f", "s16le", "-ar", "48000", "-ac", "2", "-loglevel", "quiet", "pipe:1")
	
	// Create a pipe to feed yt-dlp output to ffmpeg
	ytCmd := exec.Command("/usr/bin/yt-dlp", "-f", "bestaudio", "-o", "-", "--no-check-certificate", "--no-warnings", "--extract-audio", "--audio-format", "mp3", "--", sanitizedURL)
	
	stdout, err := ytCmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating stdout pipe: %v", err)
		return
	}
	
	cmd.Stdin = stdout
	
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("Error creating stderr pipe: %v", err)
		return
	}
	
	ffmpegOut, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating ffmpeg output pipe: %v", err)
		return
	}
	
	// Start the yt-dlp command
	if err := ytCmd.Start(); err != nil {
		log.Printf("Error starting yt-dlp: %v", err)
		return
	}
	
	// Start the ffmpeg command
	if err := cmd.Start(); err != nil {
		log.Printf("Error starting ffmpeg: %v", err)
		ytCmd.Process.Kill()
		return
	}
	
	// Create a scanner to read from ffmpeg output
	scanner := bufio.NewScanner(ffmpegOut)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	
	// Read stderr in a goroutine to prevent blocking
	go func() {
		scannerErr := bufio.NewScanner(stderr)
		for scannerErr.Scan() {
			log.Printf("FFmpeg: %s", scannerErr.Text())
		}
	}()
	
	// Send audio to Discord
	vc.Speaking(true)
	defer func() {
		vc.Speaking(false)
		// Remove the current track from the map
		mutex.Lock()
		delete(currentTracks, guildID)
		mutex.Unlock()
		
		// Play the next track if available
		playNextTrack(session, &discordgo.InteractionCreate{}, vc.ChannelID)
	}()
	
	// Buffer for audio frames
	audioBuf := make([]byte, 960*2) // 20ms at 48kHz stereo 16-bit
	
	// Create a reader from the stdout pipe
	reader := bufio.NewReader(ffmpegOut)
	
	for vc.Ready && vc.OpusSend != nil {
		// Check if the voice connection is still active
		if !vc.Ready {
			log.Println("Voice connection is not ready")
			break
		}
		
		// Read audio frame from ffmpeg
		n, err := io.ReadFull(reader, audioBuf)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				log.Println("End of audio stream reached")
				break
			}
			log.Printf("Error reading audio: %v", err)
			continue
		}
		
		if n > 0 {
			// Send the audio frame to Discord
			vc.OpusSend <- audioBuf[:n]
		}
	}
	
	// Close the pipes and kill the processes
	stdout.Close()
	ytCmd.Process.Kill()
	cmd.Process.Kill()
	
	// Wait for processes to finish
	ytCmd.Wait()
	cmd.Wait()
}

// playNextTrack plays the next track in the queue
func playNextTrack(s *discordgo.Session, i *discordgo.InteractionCreate, channelID string) {
	var guildID string
	if i != nil {
		guildID = i.GuildID
	} else {
		// If called from inside playAudioStream, we need to determine the guildID differently
		// This is a simplified approach - in practice, you'd need to track this differently
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
				if vs.ChannelID == vc.ChannelID && vs.UserID != s.State.User.ID {
					// Someone else is still in the channel, don't disconnect
					return
				}
			}
			
			// No one else is in the channel, disconnect
			vc.Disconnect()
		}
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
		log.Printf("Error joining voice channel: %v", err)
		return
	}
	
	// Wait for the connection to be ready
	for !vc.Ready {
		time.Sleep(100 * time.Millisecond)
	}

	// Start playing the audio stream
	go playAudioStream(vc, nextTrack.URL, guildID)
}