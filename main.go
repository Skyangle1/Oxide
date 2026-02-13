package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"kingmyralune/oxide-music-bot/internal/bot"
	"kingmyralune/oxide-music-bot/internal/config"
)

func main() {
	// Load configuration
	cfg := config.LoadConfig()

	// Initialize bot
	b, err := bot.NewBot(cfg)
	if err != nil {
		log.Fatal("Error creating bot: ", err)
	}

	// Start bot
	err = b.Start()
	if err != nil {
		log.Fatal("Error starting bot: ", err)
	}

	// Wait here until CTRL+C or other term signal is received
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// Cleanly close down the Discord session
	b.Session.Close()
}
