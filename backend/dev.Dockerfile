# syntax=docker/dockerfile:1.7
# mediastorm DEV image — the pinned Go toolchain plus the runtime tools the
# backend shells out to during playback that production images bundle
# (backend/Dockerfile): ffmpeg/ffprobe for HLS/probing, wget for healthchecks,
# and python3 for the title-parsing / credits scripts.
#
# dev-server.sh builds and uses this image in place of the bare golang image so
# the local server can actually transcode and stream instead of logging
# "ffprobe not configured" and "HLS not enabled".
FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651

# ffmpeg on bookworm is 5.1 — older than the production Jellyfin/BtbN 7.x
# builds, but fully capable for local dev playback. Installed tools are
# verified so a broken base image fails the build loudly.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg \
        ca-certificates \
        wget \
        xz-utils \
        python3 \
    && rm -rf /var/lib/apt/lists/* \
    && ffmpeg -version >/dev/null \
    && ffprobe -version >/dev/null \
    && python3 --version >/dev/null \
    && wget --version >/dev/null

ENV GOTOOLCHAIN=local