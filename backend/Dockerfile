# strmr Backend Dockerfile

# Build stage
FROM golang:1.23-bookworm AS builder

# Set working directory
WORKDIR /app

# Install build dependencies (required for CGO/rapidyenc)
RUN apt-get update && apt-get install -y --no-install-recommends \
    git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Copy go mod files
COPY backend/go.mod backend/go.sum ./

# Allow Go to download newer toolchains if dependencies require it
ENV GOTOOLCHAIN=auto

# Download dependencies
RUN go mod download

# Copy source code
COPY backend/ .

# Build the application (CGO required for rapidyenc)
RUN GOTOOLCHAIN=auto CGO_ENABLED=1 go build -o strmr .

# Final stage
FROM debian:bookworm-slim

# Install ca-certificates, download tools, and Python for title parsing
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget xz-utils python3 python3-venv python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Create Python virtual environment and install PTT (parsett) and subliminal (subtitle search)
RUN python3 -m venv /.venv && \
    /.venv/bin/pip install --no-cache-dir parsett subliminal

# Download static ffmpeg build with Dolby Vision (libdovi) support
# Use TARGETARCH to select the correct binary for multi-platform builds
ARG TARGETARCH
RUN if [ "$TARGETARCH" = "arm64" ]; then \
        FFMPEG_ARCH="linuxarm64"; \
    else \
        FFMPEG_ARCH="linux64"; \
    fi && \
    wget -q "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-${FFMPEG_ARCH}-gpl.tar.xz" \
    && tar -xf "ffmpeg-master-latest-${FFMPEG_ARCH}-gpl.tar.xz" \
    && mv "ffmpeg-master-latest-${FFMPEG_ARCH}-gpl/bin/ffmpeg" /usr/local/bin/ \
    && mv "ffmpeg-master-latest-${FFMPEG_ARCH}-gpl/bin/ffprobe" /usr/local/bin/ \
    && rm -rf "ffmpeg-master-latest-${FFMPEG_ARCH}-gpl" "ffmpeg-master-latest-${FFMPEG_ARCH}-gpl.tar.xz" \
    && chmod +x /usr/local/bin/ffmpeg /usr/local/bin/ffprobe

# Install yt-dlp nightly (required for YouTube trailer support, handles SABR streaming)
RUN wget -q "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp" -O /usr/local/bin/yt-dlp \
    && chmod +x /usr/local/bin/yt-dlp

# Install deno (JavaScript runtime required by yt-dlp for YouTube challenge solving)
RUN if [ "$TARGETARCH" = "arm64" ]; then \
        DENO_ARCH="aarch64-unknown-linux-gnu"; \
    else \
        DENO_ARCH="x86_64-unknown-linux-gnu"; \
    fi && \
    wget -q "https://github.com/denoland/deno/releases/latest/download/deno-${DENO_ARCH}.zip" -O /tmp/deno.zip \
    && apt-get update && apt-get install -y --no-install-recommends unzip \
    && unzip -q /tmp/deno.zip -d /usr/local/bin/ \
    && rm /tmp/deno.zip \
    && chmod +x /usr/local/bin/deno \
    && apt-get purge -y unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/strmr .

# Copy version file
COPY backend/version.txt /app/version.txt

# Copy Python scripts to root (title parsing and subtitle search)
COPY backend/parse_title.py backend/parse_title_batch.py backend/search_subtitles.py backend/download_subtitle.py /

# Expose port
EXPOSE 7777

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:7777/health > /dev/null || exit 1

# Run the application
CMD ["./strmr"]


