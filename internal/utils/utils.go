package utils

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
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

// GetRandomSweetMessage returns a random sweet message
func GetRandomSweetMessage() string {
	messages := []string{
		"Apa kabar, Sayang? 🌸",
		"Aku di sini untukmu. Selalu. 💖",
		"Melodi ini tak seindah senyummu. 🎶",
		"Kamu adalah notasi terindah dalam hidupku. 🎼",
		"Biarkan aku memelukmu dengan lagu ini. 🤗",
		"Hai, alasan aku bernyanyi. ✨",
		"Setiap detak jantungku berirama namamu. 💓",
		"Dunia sunyi tanpamu, Sayang. 🌑",
		"Kamu minta lagu apa? Atau minta hatiku? 😉",
		"Lelah? Sini istirahat sambil dengerin musik. 🛌",
	}

	// Using a simple pseudo-random selection based on time for variety
	// For better randomness, we could use math/rand, but this is sufficient for flavor text
	// and improved in newer Go versions.
	// Note: In Go 1.20+, math/rand is seeded automatically.
	// For older versions, we might need seeding, but let's assume modern Go given go.mod says 1.24
	// However, since we didn't import math/rand, let's just pick one based on time nanoseconds
	// to avoid extra imports if possible, OR just use math/rand with import.
	// Let's use time based index for simplicity if avoiding imports,
	// BUT utils already imports time (indirectly? no). Utils imports fmt, net/url, regexp, strings.
	// I need to import time and math/rand properly if I want to use them.
	// Let's check imports in utils again.
	// It does not import time or math/rand.
	// I will add imports in a separate step or just use a simple rotation map/counter if I could,
	// but random is better.
	// I will use crypto/rand or just time.Now().UnixNano() % len messages.
	// Utils doesn't import time. I need to add it.

	// Waiting for next step to fix imports if I break them.
	// Actually I can just rely on the user to fix or I can do it right now.
	// I'll assume I can just add the function and then fix imports if needed.
	// Actually, let's use a determinstic simple way or just add imports now.
	// I'll stick to valid Go.

	return messages[time.Now().UnixNano()%int64(len(messages))]
}
