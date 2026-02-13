package utils

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SanitizeURL sanitizes the URL to prevent command injection
func SanitizeURL(input string) string {
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

// FormatDuration formats duration in seconds to MM:SS or HH:MM:SS
func FormatDuration(seconds float64) string {
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

// FormatTime formats seconds to MM:SS or HH:MM:SS
func FormatTime(seconds int) string {
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

// CreateProgressBar creates a visual progress bar for the current track
func CreateProgressBar(currentTime, totalTime int) string {
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

// StringPtr Helper function to create string pointers for Discord API
func StringPtr(s string) *string {
	return &s
}
