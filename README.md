# Oxide Music Bot

A powerful Discord music bot built with Go that streams music from various sources using yt-dlp and ffmpeg.

## Features

- Play music from YouTube, SoundCloud, and other supported platforms
- Interactive controls (play/pause, skip, stop, loop)
- Music queue system
- Rich embeds with track information
- Secure implementation with input sanitization
- Optimized for deployment on Coolify

## Requirements

- Go 1.21+
- Docker (for containerized deployment)

## Setup

1. Create a Discord bot at [Discord Developer Portal](https://discord.com/developers/applications)
2. Copy `.env.example` to `.env` and add your bot token:
   ```
   DISCORD_TOKEN=your_discord_bot_token_here
   ```

## Running Locally

```bash
# Install dependencies
go mod download

# Run the bot
go run main.go
```

## Docker Deployment

Build and run with Docker:

```bash
# Build the image
docker build -t oxide-music-bot .

# Run the container
docker run -d --env-file .env oxide-music-bot
```

## Commands

- `/oxide-play <url>` - Play a song from a URL
- `/oxide-skip` - Skip the current song
- `/oxide-stop` - Stop playback and clear the queue
- `/oxide-queue` - Show the current music queue
- `/oxide-nowplaying` - Show the currently playing track

## Security

- All user inputs are sanitized to prevent command injection
- Uses non-root user in Docker container
- Environment variables for sensitive data

## Architecture

- Built with Go for efficiency and reliability
- Uses discordgo library for Discord API interaction
- yt-dlp for media extraction from various platforms
- ffmpeg for audio transcoding
- Thread-safe queue system for music playback