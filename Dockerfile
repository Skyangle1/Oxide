# Multi-stage Dockerfile for Oxide Music Bot with updated dependencies

# Build stage
# Build stage
FROM golang:1.23-alpine AS builder

# Install dependencies needed for building
RUN apk add --no-cache git ca-certificates gcc musl-dev opus-dev pkgconfig

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download and update dependencies, forcing latest discordgo that supports new encryption modes
RUN go mod download && \
    go get -u github.com/bwmarrin/discordgo@master && \
    go mod tidy

# Copy source code
COPY . .

# Build the binary with latest dependencies
RUN CGO_ENABLED=1 GOOS=linux go build -o oxide-music-bot .

# Runtime stage
FROM alpine:3.20

# Install packages needed at runtime
RUN apk --no-cache add \
    ca-certificates \
    ffmpeg \
    python3 \
    curl \
    wget \
    opus

# Download yt-dlp binary directly to avoid pip issues
RUN wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/bin/yt-dlp && \
    chmod a+rx /usr/bin/yt-dlp

# Create a non-root user
RUN addgroup -g 65532 appgroup && \
    adduser -D -u 65532 -G appgroup appuser

# Create cache directory and set permissions for non-root user
RUN mkdir -p /app/cache && \
    chown -R appuser:appgroup /app/cache

# Set environment variables
ENV PATH="/usr/local/bin:/usr/bin:${PATH}"
ENV HOME="/app"

# Set working directory
WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/oxide-music-bot .

# Change ownership to the non-root user
RUN chown -R appuser:appgroup /app

# Switch to non-root user
USER appuser

# Run the binary
CMD ["./oxide-music-bot"]