package config

import (
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	DiscordToken   string
	ApplicationID  string
	AllowedUserIDs map[string]bool
}

func LoadConfig() *Config {
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
	allowedUserIDsStr := os.Getenv("ALLOWED_USER_IDS")
	if allowedUserIDsStr == "" {
		log.Fatal("ALLOWED_USER_IDS environment variable is not set")
	}

	// Parse the comma-separated user IDs
	allowedUsers := make(map[string]bool)
	idParts := strings.Split(allowedUserIDsStr, ",")
	for _, id := range idParts {
		trimmedID := strings.TrimSpace(id)
		if trimmedID != "" {
			allowedUsers[trimmedID] = true
		}
	}

	return &Config{
		DiscordToken:   token,
		ApplicationID:  appID,
		AllowedUserIDs: allowedUsers,
	}
}
