# Multi-stage Dockerfile for Oxide Music Bot

# Build stage
FROM golang:1.21-alpine AS builder

# Install dependencies needed for building
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o oxide-music-bot .

# Runtime stage
FROM alpine:latest

# Install packages needed at runtime
RUN apk --no-cache add \
    ca-certificates \
    ffmpeg \
    python3 \
    curl \
    wget

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

# Expose port (though Discord bots don't typically expose ports)
EXPOSE 8080

# Run the binary
CMD ["./oxide-music-bot"]