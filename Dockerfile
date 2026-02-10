# Build stage
FROM golang:1.21-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o oxide-music-bot .

# Runtime stage
FROM alpine:latest

# 1. Tambahin python3 (yt-dlp butuh ini buat jalan!)
RUN apk --no-cache add \
    ca-certificates \
    ffmpeg \
    curl \
    wget \
    python3

# 2. Download yt-dlp ke /usr/bin biar lebih gampang ditemu sama sistem
RUN wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/bin/yt-dlp && \
    chmod a+rx /usr/bin/yt-dlp

# 3. Setup User
RUN addgroup -g 65532 appgroup && \
    adduser -D -u 65532 -G appgroup appuser

WORKDIR /app

# 4. Copy file bot
COPY --from=builder /app/oxide-music-bot .

# 5. Siapin folder cache dan pastiin izinnya pas
RUN mkdir -p /app/cache && chown -R appuser:appgroup /app

# 6. Set PATH secara eksplisit biar gak ada error 127 lagi
ENV PATH="/usr/bin:/usr/local/bin:${PATH}"

USER appuser

CMD ["./oxide-music-bot"]
