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

# Create Python virtual environment and install PTT (parsett)
RUN python3 -m venv /.venv && \
    /.venv/bin/pip install --no-cache-dir parsett

# Download static ffmpeg build with Dolby Vision (libdovi) support
RUN wget -q https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz \
    && tar -xf ffmpeg-master-latest-linux64-gpl.tar.xz \
    && mv ffmpeg-master-latest-linux64-gpl/bin/ffmpeg /usr/local/bin/ \
    && mv ffmpeg-master-latest-linux64-gpl/bin/ffprobe /usr/local/bin/ \
    && rm -rf ffmpeg-master-latest-linux64-gpl ffmpeg-master-latest-linux64-gpl.tar.xz \
    && chmod +x /usr/local/bin/ffmpeg /usr/local/bin/ffprobe

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/strmr .

# Copy Python title parsing scripts to root (expected by parsett package)
COPY backend/parse_title.py backend/parse_title_batch.py /

# Expose port
EXPOSE 7777

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:7777/health > /dev/null || exit 1

# Run the application
CMD ["./strmr"]


