package audio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"

	"kingmyralune/oxide-music-bot/internal/models"
	"kingmyralune/oxide-music-bot/internal/utils"
)

// Global variables for audio system
var (
	// Allowed users for exclusive access (need to be injected or accessed via config)
	// For audio package, we might not need this if we handle permissions in bot package

	// MaxConcurrentStreams limits global audio streams
	MaxConcurrentStreams = 5
	StreamSem            = make(chan struct{}, MaxConcurrentStreams)

	// RoomGuardMode maps guildID to guard mode status
	RoomGuardMode  = make(map[string]bool)
	GuardModeMutex sync.RWMutex

	// GuildContexts holds the context for each guild
	GuildContexts = make(map[string]*models.GuildContext)
	Mutex         sync.RWMutex
)

// SetRoomGuard toggles the room guard mode for a specific guild
func SetRoomGuard(guildID string, enable bool) {
	GuardModeMutex.Lock()
	defer GuardModeMutex.Unlock()
	RoomGuardMode[guildID] = enable
}

// GetTrackInfoWithContext gets track info using yt-dlp with context timeout.
// It supports both direct URLs (YouTube, SoundCloud, etc.) and search queries.
func GetTrackInfoWithContext(ctx context.Context, input string) (*models.Track, error) {
	log.Printf("GetTrackInfo: Processing input: %s", input)

	var query string
	// Check if input is a URL
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		query = utils.SanitizeURL(input)
		if query == "" {
			log.Printf("GetTrackInfo: Invalid URL provided: %s", input)
			return nil, fmt.Errorf("invalid URL provided")
		}
	} else {
		// It's a search query
		query = "ytsearch1:" + input
	}

	log.Printf("GetTrackInfo: Final query: %s", query)

	// Create the command with context and additional flags
	cmd := exec.CommandContext(ctx, "/usr/bin/yt-dlp",
		"--dump-json",
		"-f", "bestaudio",
		"--no-check-certificate",
		"--no-warnings",
		"--flat-playlist",
		"--default-search", "ytsearch", // Ensure search works if prefix fails for some reason
		"-4", // Force IPv4
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36",
		query)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		stderrContent := stderrBuf.String()
		log.Printf("GetTrackInfo: Error getting track info from yt-dlp: %v", err)
		log.Println("GetTrackInfo: yt-dlp stderr: " + stderrContent)

		// Check for specific error conditions
		if strings.Contains(stderrContent, "Unsupported URL") {
			return nil, fmt.Errorf("unsupported input: %s", input)
		} else if strings.Contains(stderrContent, "This video is unavailable") {
			return nil, fmt.Errorf("media unavailable: %s", input)
		} else if strings.Contains(stderrContent, "Sign in to confirm your age") {
			return nil, fmt.Errorf("age restricted content: %s", input)
		} else if strings.Contains(stderrContent, "This live event will begin in") {
			return nil, fmt.Errorf("scheduled live event: %s", input)
		}

		return nil, fmt.Errorf("error getting track info: %v", err)
	}

	output := stdoutBuf.Bytes()

	// Validate output before parsing JSON
	if len(output) == 0 {
		log.Printf("GetTrackInfo: Empty output from yt-dlp")
		return nil, fmt.Errorf("no results found for: %s", input)
	}

	// Check if output starts with unexpected character
	if len(output) > 0 && output[0] != '{' {
		outputStr := string(output)
		log.Printf("GetTrackInfo: Unexpected output format, starts with: '%c', full output: %s", output[0], outputStr)
		return nil, fmt.Errorf("unexpected output format from yt-dlp")
	}

	var info struct {
		Title      string  `json:"title"`
		Duration   float64 `json:"duration"`
		Uploader   string  `json:"uploader"`
		Thumbnail  string  `json:"thumbnail"`
		WebpageURL string  `json:"webpage_url"`
	}

	if err := json.Unmarshal(output, &info); err != nil {
		log.Printf("GetTrackInfo: Error parsing JSON: %v", err)
		return nil, fmt.Errorf("error parsing track info")
	}

	// Validate that we got meaningful data
	if info.Title == "" {
		return nil, fmt.Errorf("could not retrieve title for: %s", input)
	}

	// Format duration
	durationStr := utils.FormatDuration(info.Duration)

	log.Printf("GetTrackInfo: Successfully retrieved info for '%s' (%s)", info.Title, info.WebpageURL)

	return &models.Track{
		Title:     info.Title,
		URL:       info.WebpageURL,
		Duration:  durationStr,
		Uploader:  info.Uploader,
		Thumbnail: info.Thumbnail,
	}, nil
}

// PlayNextTrack plays the next track in the queue
func PlayNextTrack(s *discordgo.Session, guildID string, channelID string) {
	// Guard clause: Check if guildID is empty
	if guildID == "" {
		log.Println("Error: guildID kosong, membatalkan playback.")
		return
	}

	// Acquire a slot from the global stream semaphore
	select {
	case StreamSem <- struct{}{}: // Acquire a slot
		log.Printf("Acquired stream slot for guild %s, active streams: %d/%d", guildID, len(StreamSem), cap(StreamSem))
	default:
		log.Printf("Maximum concurrent streams reached (%d), rejecting playback for guild %s", MaxConcurrentStreams, guildID)
		return
	}

	// releaseSlot helper - call when playback is truly done
	releaseSlot := func() {
		select {
		case <-StreamSem:
			log.Printf("Released stream slot for guild %s", guildID)
		default:
			log.Printf("Warning: Tried to release stream slot but semaphore was empty for guild %s", guildID)
		}
	}

	Mutex.Lock()

	// Ensure guild context exists
	guildCtx, exists := GuildContexts[guildID]
	if !exists {
		guildCtx = &models.GuildContext{
			MusicQueue: &models.MusicQueue{},
		}
		GuildContexts[guildID] = guildCtx
	}

	// Ensure MusicQueue is initialized
	if guildCtx.MusicQueue == nil {
		guildCtx.MusicQueue = &models.MusicQueue{}
	}

	queue := guildCtx.MusicQueue

	// Check if queue is empty
	if queue == nil || len(queue.Tracks) == 0 {
		Mutex.Unlock()

		// Queue is empty, disconnect from voice if no one else is listening
		vc, err := GetConnectedVoiceConnection(s, guildID)
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

		// Check if room guard mode is enabled for this guild
		GuardModeMutex.RLock()
		shouldGuard := RoomGuardMode[guildID]
		GuardModeMutex.RUnlock()

		if shouldGuard {
			// Room guard mode is enabled, keep the connection alive
			return
		} else {
			// Room guard mode is disabled, disconnect as before
			vc.Disconnect()
			return
		}
	}

	// Get the next track
	if len(queue.Tracks) == 0 {
		log.Printf("playNextTrack: Queue is empty for guild %s", guildID)
		Mutex.Unlock()
		return
	}

	nextTrack := queue.Tracks[0]

	if nextTrack == nil {
		log.Printf("playNextTrack: Next track is nil for guild %s", guildID)
		if len(queue.Tracks) > 0 {
			queue.Tracks = queue.Tracks[1:]
		}
		Mutex.Unlock()
		return
	}

	// Remove the played track from the queue
	if len(queue.Tracks) > 0 {
		queue.Tracks = queue.Tracks[1:]
	}

	// Store the current track in the guild context
	guildCtx.CurrentTrack = nextTrack
	Mutex.Unlock()

	// Check if we already have a voice connection for this guild
	Mutex.RLock()
	existingVc := guildCtx.VoiceConnection
	Mutex.RUnlock()

	var vc *discordgo.VoiceConnection
	var err error

	// Reuse existing voice connection if it's still active
	if existingVc != nil && existingVc.Ready && existingVc.ChannelID == channelID {
		vc = existingVc
		log.Printf("playNextTrack: Reusing existing voice connection for guild %s", guildID)
	} else {
		// Join voice channel logic with retry
		maxRetries := 5
		retryCount := 0

		for retryCount < maxRetries {
			if existingVc != nil && (existingVc.ChannelID != channelID || !existingVc.Ready) {
				existingVc.Disconnect()
				time.Sleep(500 * time.Millisecond)
			}

			vc, err = s.ChannelVoiceJoin(guildID, channelID, false, true)
			if err == nil && vc != nil {
				readyWait := 0
				for readyWait < 50 && !vc.Ready {
					time.Sleep(100 * time.Millisecond)
					readyWait++
				}

				if vc.Ready {
					log.Printf("playNextTrack: Successfully joined voice channel for guild %s", guildID)
					break
				} else {
					log.Printf("playNextTrack: Connection not ready after waiting, retrying... (%d/%d)", retryCount+1, maxRetries)
					vc.Disconnect()
				}
			} else if vc != nil {
				vc.Disconnect()
			}

			// Check if the error is related to encryption mode
			if err != nil && (strings.Contains(err.Error(), "4016") || strings.Contains(err.Error(), "encryption")) {
				log.Printf("Encryption mode error detected (attempt %d/%d), reconnecting...", retryCount+1, maxRetries)
				retryCount++
				time.Sleep(3 * time.Second)
				continue
			} else if err != nil {
				log.Printf("playNextTrack: Error joining voice channel (attempt %d/%d): %v", retryCount+1, maxRetries, err)
				retryCount++
				time.Sleep(2 * time.Second)
				continue
			} else if vc == nil {
				log.Printf("playNextTrack: Voice connection is nil (attempt %d/%d), retrying...", retryCount+1, maxRetries)
				retryCount++
				time.Sleep(1 * time.Second)
				continue
			}
		}

		if vc == nil {
			log.Printf("playNextTrack: Failed to join voice channel after %d attempts", maxRetries)
			return
		}

		Mutex.Lock()
		guildCtx.VoiceConnection = vc
		Mutex.Unlock()
	}

	vc.LogLevel = discordgo.LogDebug

	if vc.OpusSend == nil {
		vc.OpusSend = make(chan []byte, 100)
	}

	// Wait for connection ready
	connectionReady := false
	for i := 0; i < 100 && !vc.Ready; i++ {
		time.Sleep(100 * time.Millisecond)
		if vc.Ready {
			connectionReady = true
			break
		}
	}

	if !connectionReady {
		log.Println("Warning: Voice connection might not be perfectly ready, but proceeding to maintain stream continuity.")
	} else {
		log.Println("Voice connection is ready")
	}

	// Add forced delay to ensure connection stability
	time.Sleep(1 * time.Second)

	HandleEncryptionMode(vc)

	if vc == nil {
		log.Println("KRITIS: voiceConnection is nil! Cannot proceed.")
		return
	} else if !vc.Ready {
		log.Println("Warning: voiceConnection.Ready is false, but forcing playback attempt for smooth transition.")
	}

	Mutex.RLock()
	contextVC := guildCtx.VoiceConnection
	Mutex.RUnlock()

	if contextVC == nil {
		log.Println("Voice connection NULL in playNextTrack, attempting reconnection...")
		return
	}

	// --- UI MODIFICATION START ---
	// Send "Now Playing" embedding with buttons
	go func() {
		embed := &discordgo.MessageEmbed{
			Title:       "🎶 Now Playing",
			Description: fmt.Sprintf("[%s](%s)\n\nRequested by: %s", nextTrack.Title, nextTrack.URL, nextTrack.RequesterUsername),
			Color:       0xFF69B4, // Hot Pink color (Modern/Vibrant)
			Thumbnail: &discordgo.MessageEmbedThumbnail{
				URL: nextTrack.Thumbnail,
			},
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Duration",
					Value:  nextTrack.Duration,
					Inline: true,
				},
				{
					Name:   "Status",
					Value:  "▶️ Playing",
					Inline: true,
				},
			},
			Footer: &discordgo.MessageEmbedFooter{
				Text: "Queen's Lɣre୨ৎ⭑ | Connected voice",
			},
		}

		// Buttons
		buttons := []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Emoji:    &discordgo.ComponentEmoji{Name: "⏯️"},
						Style:    discordgo.SecondaryButton,
						CustomID: "oxide_pause",
					},
					discordgo.Button{
						Emoji:    &discordgo.ComponentEmoji{Name: "⏭️"},
						Style:    discordgo.SecondaryButton,
						CustomID: "oxide_skip",
					},
					discordgo.Button{
						Emoji:    &discordgo.ComponentEmoji{Name: "⏹️"},
						Style:    discordgo.DangerButton,
						CustomID: "oxide_stop",
					},
					discordgo.Button{
						Emoji:    &discordgo.ComponentEmoji{Name: "📜"},
						Style:    discordgo.SecondaryButton,
						CustomID: "oxide_queue",
					},
				},
			},
		}

		msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: buttons,
		})

		if err == nil {
			Mutex.Lock()
			if ctx, ok := GuildContexts[guildID]; ok {
				ctx.LastMessageID = msg.ID
				ctx.LastChannelID = channelID
			}
			Mutex.Unlock()
		} else {
			log.Printf("Error sending now playing message: %v", err)
		}
	}()
	// --- UI MODIFICATION END ---

	// Allow playback if vc exists, even if Ready flag is lagging (we trust the heartbeat/session)
	if vc != nil {
		vc.LogLevel = discordgo.LogDebug

		go func(nextTrack *models.Track) {
			// Release semaphore when this goroutine exits (playback truly done)
			defer releaseSlot()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered from panic in playAudioStream goroutine: %v", r)
					log.Printf("Stack trace:\n%s", debug.Stack())
				}
			}()

			err := playAudioStream(s, vc, nextTrack.URL, guildID, nextTrack.RequesterUsername)
			if err != nil {
				log.Printf("Error playing audio stream: %v", err)
			} else {
				log.Printf("Successfully played track: %s", nextTrack.Title)
			}

			// Chain to next track after this one finishes
			// (semaphore will be released by defer above AFTER this returns)
		}(nextTrack)
	} else {
		log.Printf("playNextTrack: Conditions not met for playback - vc is nil")
		releaseSlot()
	}
}

// playAudioStream plays audio from a URL using yt-dlp and ffmpeg
func playAudioStream(s *discordgo.Session, vc *discordgo.VoiceConnection, url string, guildID string, requesterUsername string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in playAudioStream: %v", r)
			log.Printf("Stack trace:\n%s", debug.Stack())
		}
	}()

	log.Printf("playAudioStream: Starting to play audio for guild %s, URL: %s, requested by: %s", guildID, url, requesterUsername)

	if vc == nil {
		log.Printf("playAudioStream: Voice connection is nil")
		return fmt.Errorf("voice connection is nil")
	}

	sanitizedURL := utils.SanitizeURL(url)
	if sanitizedURL == "" {
		log.Printf("playAudioStream: Invalid URL provided: %s", url)
		return fmt.Errorf("invalid URL provided: %s", url)
	}

	// Get direct URL
	getUrlCmd := exec.CommandContext(ctx, "/usr/bin/yt-dlp", "--get-url", "-f", "bestaudio", "--no-check-certificate", "--no-warnings", "--no-playlist", "--", sanitizedURL)
	urlOutput, err := getUrlCmd.Output()
	if err != nil {
		log.Printf("playAudioStream: Error getting direct URL from yt-dlp: %v", err)
		return fmt.Errorf("error getting direct URL from yt-dlp: %w", err)
	}

	directURL := strings.TrimSpace(string(urlOutput))
	log.Printf("playAudioStream: Got direct URL: %s", directURL)

	// Here we would get user preferences, but for now default to 1.0 or implement a way to access prefs
	// For simplicity in refactoring, we'll assume 1.0 or access via a global/config if needed.
	// We'll skip user prefs for volume here to avoid circular dependencies or complexity for now.
	volume := 1.0

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-threads", "1",
		"-i", directURL,
		"-filter:a", fmt.Sprintf("volume=%.2f", volume),
		"-f", "s16le",
		"-ar", "48000",
		"-ac", "2",
		"-preset", "ultrafast",
		"-c:a", "pcm_s16le",
		"-loglevel", "error",
		"pipe:1")

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	ffmpegOut, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("playAudioStream: Error creating ffmpeg output pipe: %v", err)
		return fmt.Errorf("error creating ffmpeg output pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("playAudioStream: Error starting ffmpeg: %v", err)
		return fmt.Errorf("error starting ffmpeg: %w", err)
	}

	cmdDone := make(chan error, 1)
	go func() {
		cmdDone <- cmd.Wait()
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in ffmpeg stderr reader: %v", r)
			}
		}()
		scannerErr := bufio.NewScanner(&stderrBuf)
		for scannerErr.Scan() {
			line := scannerErr.Text()
			log.Printf("FFmpeg: %s", line)
		}
	}()

	reader := bufio.NewReaderSize(ffmpegOut, 32768)

	// Wait for voice connection
	readyAttempts := 0
	maxReadyAttempts := 100
	for vc != nil && !vc.Ready && readyAttempts < maxReadyAttempts {
		time.Sleep(50 * time.Millisecond)
		readyAttempts++
	}

	if vc == nil || !vc.Ready {
		log.Println("playAudioStream: Voice connection is not ready after waiting")
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return fmt.Errorf("voice connection is not ready after waiting")
	}

	enc, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		log.Printf("playAudioStream: Error creating Opus encoder: %v", err)
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return fmt.Errorf("error creating Opus encoder: %w", err)
	}

	enc.SetBitrate(128 * 1000)

	Mutex.Lock()
	guildCtx, exists := GuildContexts[guildID]
	if !exists {
		guildCtx = &models.GuildContext{
			MusicQueue: &models.MusicQueue{},
		}
		GuildContexts[guildID] = guildCtx
	}

	if guildCtx != nil {
		guildCtx.StartTime = time.Now()
	}
	Mutex.Unlock()

	// Progress updater and playback loop
	progressUpdaterCtx, progressUpdaterCancel := context.WithCancel(ctx)
	progressUpdaterDone := make(chan struct{})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in progress updater goroutine: %v", r)
			}
			close(progressUpdaterDone)
		}()

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				Mutex.RLock()
				guildCtx, exists := GuildContexts[guildID]
				if !exists || guildCtx == nil || guildCtx.StartTime.IsZero() {
					Mutex.RUnlock()
					continue
				}
				lastMessageID := guildCtx.LastMessageID
				lastChannelID := guildCtx.LastChannelID
				currentTrack := guildCtx.CurrentTrack
				startTime := guildCtx.StartTime
				Mutex.RUnlock()

				if lastMessageID == "" || lastChannelID == "" || currentTrack == nil {
					continue
				}

				elapsed := int(time.Since(startTime).Seconds())
				trackDuration := 0
				if currentTrack.Duration != "" {
					parts := strings.Split(currentTrack.Duration, ":")
					if len(parts) == 2 {
						mins, _ := strconv.Atoi(parts[0])
						secs, _ := strconv.Atoi(parts[1])
						trackDuration = mins*60 + secs
					} else if len(parts) == 3 {
						hours, _ := strconv.Atoi(parts[0])
						mins, _ := strconv.Atoi(parts[1])
						secs, _ := strconv.Atoi(parts[2])
						trackDuration = hours*3600 + mins*60 + secs
					}
				}

				progressBar := utils.CreateProgressBar(elapsed, trackDuration)
				elapsedStr := utils.FormatTime(elapsed)

				// Recreate embed
				embed := &discordgo.MessageEmbed{
					Title:       "🎶 Now Playing",
					Description: fmt.Sprintf("[%s](%s)\n\nRequested by: %s\n\n`%s`\n**%s / %s**", currentTrack.Title, currentTrack.URL, currentTrack.RequesterUsername, progressBar, elapsedStr, currentTrack.Duration),
					Color:       0xFF69B4,
					Thumbnail: &discordgo.MessageEmbedThumbnail{
						URL: currentTrack.Thumbnail,
					},
					Footer: &discordgo.MessageEmbedFooter{
						Text: "Queen's Lɣre୨ৎ⭑ | Connected voice",
					},
				}

				// Keep buttons
				buttons := []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.Button{
								Emoji:    &discordgo.ComponentEmoji{Name: "⏯️"},
								Style:    discordgo.SecondaryButton,
								CustomID: "oxide_pause",
							},
							discordgo.Button{
								Emoji:    &discordgo.ComponentEmoji{Name: "⏭️"},
								Style:    discordgo.SecondaryButton,
								CustomID: "oxide_skip",
							},
							discordgo.Button{
								Emoji:    &discordgo.ComponentEmoji{Name: "⏹️"},
								Style:    discordgo.DangerButton,
								CustomID: "oxide_stop",
							},
							discordgo.Button{
								Emoji:    &discordgo.ComponentEmoji{Name: "📜"},
								Style:    discordgo.SecondaryButton,
								CustomID: "oxide_queue",
							},
						},
					},
				}

				_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
					ID:         lastMessageID,
					Channel:    lastChannelID,
					Embeds:     &[]*discordgo.MessageEmbed{embed},
					Components: &buttons,
				})

				if err != nil {
					log.Printf("Error updating progress bar: %v", err)
					// If message deleted, stop updating
					if strings.Contains(err.Error(), "404 Not Found") {
						return
					}
				}

			case <-progressUpdaterCtx.Done():
				return
			}
		}
	}()

	if vc != nil {
		vc.LogLevel = discordgo.LogDebug
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered from panic in vc.Speaking(): %v", r)
				}
			}()
			vc.Speaking(true)
		}()
		time.Sleep(1 * time.Second)
	}

	defer func() {
		if vc != nil {
			time.Sleep(200 * time.Millisecond)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("Recovered from panic in vc.Speaking(false): %v", r)
					}
				}()
				vc.Speaking(false)
			}()
		}

		Mutex.Lock()
		guildCtx, exists := GuildContexts[guildID]
		if exists && guildCtx != nil {
			guildCtx.CurrentTrack = nil
		}
		Mutex.Unlock()

		progressUpdaterCancel()

		select {
		case <-progressUpdaterDone:
		case <-time.After(1 * time.Second):
		}

		// Auto-advance: play next track in queue
		if vc != nil {
			PlayNextTrack(s, guildID, vc.ChannelID)
		}
	}()

	audioBytes := make([]byte, 3840)
	pcmBuf := make([]int16, 960*2)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	frameCounter := 0

audioStreamLoop:
	for {
		select {
		case <-ctx.Done():
			break audioStreamLoop
		default:
			if vc == nil || !vc.Ready || vc.OpusSend == nil {
				break audioStreamLoop
			}

			// Check pause state
			Mutex.RLock()
			guildCtx, exists := GuildContexts[guildID]
			isPaused := false
			if exists && guildCtx != nil {
				isPaused = guildCtx.IsPaused
			}
			Mutex.RUnlock()

			if isPaused {
				// Send silence frame to keep connection alive
				// Encode silence
				opusSilence, err := enc.Encode(make([]int16, 960*2), 960, 4000)
				if err == nil && vc.OpusSend != nil {
					select {
					case vc.OpusSend <- opusSilence:
					case <-time.After(100 * time.Millisecond):
					}
				}
				time.Sleep(20 * time.Millisecond)
				continue
			}

			// Clear buffer and read audio
			for i := range audioBytes {
				audioBytes[i] = 0
			}

			n, err := io.ReadFull(reader, audioBytes)
			if err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					// Only break if we are truly at end of stream and not just paused
					if !isPaused {
						time.Sleep(500 * time.Millisecond)
						break audioStreamLoop
					}
				}
				continue
			}

			if n > 0 && vc != nil && vc.OpusSend != nil {
				for i := 0; i < n/2; i++ {
					pcmBuf[i] = int16(audioBytes[i*2]) | int16(audioBytes[i*2+1])<<8
				}

				opusData, err := enc.Encode(pcmBuf[:n/2], 960, 4000)
				if err != nil {
					continue
				}

				<-ticker.C

				if vc == nil || !vc.Ready {
					break audioStreamLoop
				}

				if vc.OpusSend != nil {
					select {
					case vc.OpusSend <- opusData:
						frameCounter++
					case <-time.After(100 * time.Millisecond):
						log.Printf("Timeout sending frame %d", frameCounter)
					}
				} else {
					break audioStreamLoop
				}
			}
		}
	}

	select {
	case <-cmdDone:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}

	return nil
}

// GetConnectedVoiceConnection helper
func GetConnectedVoiceConnection(s *discordgo.Session, guildID string) (*discordgo.VoiceConnection, error) {
	Mutex.RLock()
	guildCtx, exists := GuildContexts[guildID]
	if exists && guildCtx != nil && guildCtx.VoiceConnection != nil {
		vc := guildCtx.VoiceConnection
		Mutex.RUnlock()

		if vc != nil && vc.Ready {
			return vc, nil
		}
	} else {
		Mutex.RUnlock()
	}

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
func GetVoiceState(s *discordgo.Session, userID, guildID string) (*discordgo.VoiceState, error) {
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

	// Refresh cache logic
	refreshedGuild, err := s.Guild(guildID)
	if err != nil {
		return nil, err
	}

	if refreshedGuild != nil && refreshedGuild.VoiceStates != nil {
		for _, vs := range refreshedGuild.VoiceStates {
			if vs != nil && vs.UserID == userID {
				s.State.GuildAdd(refreshedGuild)
				return vs, nil
			}
		}
	}

	return nil, fmt.Errorf("user not in a voice channel")
}

// HandleEncryptionMode handles the encryption mode selection for Discord voice connections
func HandleEncryptionMode(vc *discordgo.VoiceConnection) {
	if vc == nil {
		return
	}

	for i := 0; i < 100 && !vc.Ready; i++ {
		time.Sleep(100 * time.Millisecond)
	}

	if !vc.Ready {
		log.Println("Warning: Voice connection is not ready")
	} else {
		log.Println("Voice connection is ready")
	}
}
