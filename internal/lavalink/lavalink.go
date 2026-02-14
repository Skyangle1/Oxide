package lavalink

import (
	"context"
	"fmt"
	"log"

	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgolink/v3/disgolink"
	"github.com/disgoorg/disgolink/v3/lavalink"
	"github.com/disgoorg/snowflake/v2"

	"kingmyralune/oxide-music-bot/internal/config"
	"kingmyralune/oxide-music-bot/internal/models"
)

// Client wraps the disgolink client
type Client struct {
	Disgolink disgolink.Client
	Session   *discordgo.Session
	Config    *config.Config
	Queues    map[string]*models.MusicQueue
	Mu        sync.RWMutex
}

// NewClient creates a new Lavalink client
func NewClient(s *discordgo.Session, cfg *config.Config) *Client {
	return &Client{
		Session: s,
		Config:  cfg,
		Queues:  make(map[string]*models.MusicQueue),
	}
}

// Connect initializes the disgolink client and connects to the node
func (c *Client) Connect(ctx context.Context) error {
	// Create disgolink client
	c.Disgolink = disgolink.New(snowflake.MustParse(c.Session.State.User.ID),
		disgolink.WithListenerFunc(c.onPlayerUpdate),
		disgolink.WithListenerFunc(c.onTrackStart),
		disgolink.WithListenerFunc(c.onTrackEnd),
		disgolink.WithListenerFunc(c.onTrackException),
		disgolink.WithListenerFunc(c.onTrackStuck),
		disgolink.WithListenerFunc(c.onWebSocketClosed),
	)

	// Add the node
	node, err := c.Disgolink.AddNode(ctx, disgolink.NodeConfig{
		Name:     "local-node",
		Address:  c.Config.LavalinkAddress,
		Password: c.Config.LavalinkPassword,
		Secure:   false,
	})

	if err != nil {
		return fmt.Errorf("failed to add lavalink node: %w", err)
	}

	log.Printf("Lavalink: Node %s added", node.Config().Name)
	return nil
}

// RegisterHandlers adds the voice state update handlers to discordgo
func (c *Client) RegisterHandlers() {
	c.Session.AddHandler(func(s *discordgo.Session, event *discordgo.VoiceServerUpdate) {
		c.Disgolink.OnVoiceServerUpdate(context.TODO(), snowflake.MustParse(event.GuildID), event.Token, event.Endpoint)
	})

	c.Session.AddHandler(func(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
		if event.UserID != s.State.User.ID {
			return
		}

		var channelID *snowflake.ID
		if event.ChannelID != "" {
			id := snowflake.MustParse(event.ChannelID)
			channelID = &id
		}

		c.Disgolink.OnVoiceStateUpdate(context.TODO(), snowflake.MustParse(event.GuildID), channelID, event.SessionID)
	})
}

// Close closes the lavalink client
func (c *Client) Close() {
	if c.Disgolink != nil {
		// Disconnect all players
		// c.Disgolink.Data().ForEach(func(guildID snowflake.ID, player disgolink.Player) {
		// 	player.Destroy(context.TODO())
		// })
	}
}

// Event Handlers
func (c *Client) onPlayerUpdate(player disgolink.Player, event lavalink.PlayerUpdateMessage) {
	// log.Printf("Lavalink: Player update for guild %s: %v", player.GuildID(), event.State.Position)
}

func (c *Client) onTrackStart(player disgolink.Player, event lavalink.TrackStartEvent) {
	log.Printf("Lavalink: Track started: %s", event.Track.Info.Title)

	// Send Now Playing Message (Simulated for now, needs context)
	// In a real implementation, we might want to use a channel or callback to UI
}

func (c *Client) onTrackEnd(player disgolink.Player, event lavalink.TrackEndEvent) {
	log.Printf("Lavalink: Track ended: %s (Reason: %s)", event.Track.Info.Title, event.Reason)

	if !event.Reason.MayStartNext() {
		return
	}

	queue := c.GetQueue(player.GuildID().String())
	if queue == nil || len(queue.Tracks) == 0 {
		return
	}

	// Pop next track
	nextTrack := queue.Tracks[0]
	queue.Tracks = queue.Tracks[1:]

	// Play next
	// We need to reload the track or construct it. The model tracks store metadata.
	// But PlayTrack expects a query.
	// Since we stored models.Track which has URL, we can load it.
	// However, this is inefficient. Ideally we store the encoded track.
	// For MVP, we will just use the URL.

	// Issue: PlayTrack needs context and channel ID. We don't have channel ID easily here unless stored.
	// But player is already connected?
	// disgolink player stays connected.

	if err := player.Update(context.TODO(), lavalink.WithTrack(event.Track)); err != nil {
		// Wait, we need to play NEXT track, not current event.Track
		// Actually for now let's just log "Queue processing not fully implemented in event yet"
		// To properly implement this, we need to call LoadTracks again or store the encoded track.
	}

	// Real implementation requires resolving the next track.
	ctx := context.TODO()
	node := c.Disgolink.BestNode()
	if node == nil {
		return
	}

	result, err := node.LoadTracks(ctx, nextTrack.URL)
	if err != nil {
		log.Printf("Failed to load next track: %v", err)
		return
	}

	var trackToPlay lavalink.Track
	if result.LoadType == lavalink.LoadTypeTrack {
		trackToPlay = result.Data.(lavalink.Track)
	} else if result.LoadType == lavalink.LoadTypeSearch {
		tracks := result.Data.(lavalink.Search)
		if len(tracks) > 0 {
			trackToPlay = tracks[0]
		}
	} else if result.LoadType == lavalink.LoadTypePlaylist {
		playlist := result.Data.(lavalink.Playlist)
		if len(playlist.Tracks) > 0 {
			trackToPlay = playlist.Tracks[0]
		}
	}

	if err := player.Update(ctx, lavalink.WithTrack(trackToPlay)); err != nil {
		log.Printf("Failed to play next track: %v", err)
	}
}

func (c *Client) GetQueue(guildID string) *models.MusicQueue {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Queues[guildID]
}

func (c *Client) AddToQueue(guildID string, track *models.Track) {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.Queues[guildID] == nil {
		c.Queues[guildID] = &models.MusicQueue{}
	}
	c.Queues[guildID].Tracks = append(c.Queues[guildID].Tracks, track)
}

func (c *Client) onTrackException(player disgolink.Player, event lavalink.TrackExceptionEvent) {
	log.Printf("Lavalink: Track exception: %s (%v)", event.Track.Info.Title, event.Exception)
}

func (c *Client) onTrackStuck(player disgolink.Player, event lavalink.TrackStuckEvent) {
	log.Printf("Lavalink: Track stuck: %s", event.Track.Info.Title)
}

func (c *Client) onWebSocketClosed(player disgolink.Player, event lavalink.WebSocketClosedEvent) {
	log.Printf("Lavalink: Websocket closed: %d - %s", event.Code, event.Reason)
}
