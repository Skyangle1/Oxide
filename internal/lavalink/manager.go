package lavalink

import (
	"context"
	"fmt"
	"kingmyralune/oxide-music-bot/internal/models"
	"log"

	"github.com/disgoorg/disgolink/v3/lavalink"
	"github.com/disgoorg/snowflake/v2"
)

// PlayTrack plays a track in the given guild
func (c *Client) PlayTrack(ctx context.Context, guildID string, channelID string, query string) error {
	// 1. Join Voice Channel
	if err := c.Session.ChannelVoiceJoinManual(guildID, channelID, false, true); err != nil {
		return fmt.Errorf("failed to join voice channel: %w", err)
	}

	// 2. Load Item
	// Note: We need to access the best node to load tracks
	node := c.Disgolink.BestNode()
	if node == nil {
		return fmt.Errorf("no avaliable lavalink nodes")
	}

	// Search for the track
	result, err := node.LoadTracks(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to load track: %w", err)
	}

	var track lavalink.Track

	switch result.LoadType {
	case lavalink.LoadTypeTrack:
		track = result.Data.(lavalink.Track)
	case lavalink.LoadTypePlaylist:
		playlist := result.Data.(lavalink.Playlist)
		track = playlist.Tracks[0] // Play first track
		// TODO: Queue the rest
	case lavalink.LoadTypeSearch:
		tracks := result.Data.(lavalink.Search)
		if len(tracks) == 0 {
			return fmt.Errorf("no matches found for: %s", query)
		}
		track = tracks[0]
	default:
		return fmt.Errorf("no matches found for: %s", query)
	}

	// 3. Play or Queue
	player := c.Disgolink.Player(snowflake.MustParse(guildID))

	// Create model track for queue
	var uri, artwork string
	if track.Info.URI != nil {
		uri = *track.Info.URI
	}
	if track.Info.ArtworkURL != nil {
		artwork = *track.Info.ArtworkURL
	}

	modelTrack := &models.Track{
		Title:     track.Info.Title,
		URL:       uri,
		Duration:  fmt.Sprintf("%v", track.Info.Length),
		Uploader:  track.Info.Author,
		Thumbnail: artwork,
	}

	if player.Track() != nil {
		// Playing, add to queue
		c.AddToQueue(guildID, modelTrack)
		log.Printf("Lavalink: Added %s to queue in guild %s", track.Info.Title, guildID)
		return nil
	}

	if err := player.Update(ctx, lavalink.WithTrack(track)); err != nil {
		return fmt.Errorf("failed to play track: %w", err)
	}

	log.Printf("Lavalink: Playing %s in guild %s", track.Info.Title, guildID)
	return nil
}

// Stop stops playback and leaves the channel
func (c *Client) Stop(ctx context.Context, guildID string) error {
	// Clear queue
	c.Mu.Lock()
	if q, ok := c.Queues[guildID]; ok {
		q.Tracks = nil
	}
	c.Mu.Unlock()

	player := c.Disgolink.Player(snowflake.MustParse(guildID))
	if player != nil {
		if err := player.Update(ctx, lavalink.WithNullTrack()); err != nil {
			return fmt.Errorf("failed to stop player: %w", err)
		}
		// Destroy player to clean up
		if err := player.Destroy(ctx); err != nil {
			return fmt.Errorf("failed to destroy player: %w", err)
		}
	}

	// Leave voice channel using discordgo
	// Note: discordgo doesn't have a direct "disconnect" for manual voice connections easily accessible
	// without storing the VoiceConnection object, but since we use manual updates,
	// we assume the user will be disconnected by the Session.VoiceStateUpdate or similar.
	// Actually for manual join, we might need to send a VoiceStateUpdate with nil channel.

	// Sending an empty VoiceStateUpdate to disconnect
	err := c.Session.ChannelVoiceJoinManual(guildID, "", false, true)
	if err != nil {
		return fmt.Errorf("failed to leave voice channel: %w", err)
	}

	return nil
}
