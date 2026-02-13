package models

import (
	"math"
	"time"

	"github.com/bwmarrin/discordgo"
)

// MusicQueue represents a queue of tracks
type MusicQueue struct {
	Tracks []*Track
	Loop   bool
}

// Track represents a music track
type Track struct {
	Title             string
	URL               string
	Duration          string
	Uploader          string
	Thumbnail         string
	RequesterID       string
	RequesterUsername string
}

// TrackWithDuration extends Track with duration information in seconds
type TrackWithDuration struct {
	*Track
	DurationSeconds int       // Duration in seconds
	StartTime       time.Time // Time when this track started playing
}

// SavedPlaylist represents a user's saved playlist
type SavedPlaylist struct {
	Name   string
	Tracks []*Track
}

// UserPreferences holds user-specific settings
type UserPreferences struct {
	Volume         float64 // Volume level (0.0 to 1.0)
	SavedPlaylists map[string]*SavedPlaylist
}

// UserStats holds user statistics for the leveling system
type UserStats struct {
	LovePoints          int
	Level               int
	TotalMessages       int
	LastActivity        time.Time
	CurrentSongRequests int
}

// GuildContext holds the context for each guild
type GuildContext struct {
	VoiceConnection *discordgo.VoiceConnection
	MusicQueue      *MusicQueue
	CurrentTrack    *Track
	StartTime       time.Time // Time when the current track started playing
	LastMessageID   string    // ID of the last "Now Playing" message
	LastChannelID   string    // Channel ID of the last "Now Playing" message
}

// RateLimiter tracks user requests to prevent abuse (interface or implementation can go here too if needed everywhere)
// However, the implementation is simple enough to reside here or in utils.
// Let's keep it here alongside other core structures.

// CalculateLevel calculates the level based on love points
func CalculateLevel(points int) int {
	// Simple formula: level = sqrt(points) rounded down
	return int(math.Sqrt(float64(points)))
}

// GetLevelName returns the name for a given level
func GetLevelName(level int) string {
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
