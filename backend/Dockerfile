# syntax=docker/dockerfile:1.7
# mediastorm Backend Dockerfile

# Web client (web/), served at /watch. Its output is static and architecture
# independent, so build it once on the native builder platform instead of per
# target platform in multi-arch buildx jobs.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim@sha256:6c74791e557ce11fc957704f6d4fe134a7bc8d6f5ca4403205b2966bd488f6b3 AS web-builder

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY web/ ./
RUN npm run build

# Build stage
FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS builder

# Set working directory
WORKDIR /app

# Install build dependencies (required for CGO/rapidyenc)
RUN apt-get update && apt-get install -y --no-install-recommends \
    git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Copy go mod files, plus the locally patched modules go.mod replaces
COPY backend/go.mod backend/go.sum ./
COPY backend/third_party ./third_party

# The builder image matches go.mod, so avoid downloading a toolchain during
# dependency resolution.
ENV GOTOOLCHAIN=local

# Download dependencies. The retry handles transient Go proxy TLS timeouts
# during multi-platform buildx runs.
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    for attempt in 1 2 3; do \
        go mod download && break; \
        if [ "$attempt" = "3" ]; then exit 1; fi; \
        sleep $((attempt * 5)); \
    done

# Copy source code
COPY backend/ .

# Build the application (CGO required for rapidyenc). GIT_COMMIT is the
# build digest shown in the admin UI and /api/version.
ARG GIT_COMMIT=""
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=1 go build -ldflags "-X novastream/handlers.buildCommit=${GIT_COMMIT}" -o mediastorm .

# iroh bridge build stage. The Go backend shells out to this Rust binary to host
# the iroh remote-access endpoint, so the image needs a Linux build of it.
FROM rust:1-bookworm@sha256:77fac8b98f9f46062bb680b6d25d5bcaabfc400143952ebc572e924bcbedc3fa AS iroh-builder
WORKDIR /iroh
COPY backend/iroh-host/ ./
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,target=/iroh/target,sharing=locked \
    cargo build --release && \
    cp target/release/iroh-direct-spike /iroh/iroh-direct-spike

# ccextractor builder. Live TV EIA-608 extraction prefers this bitstream tool
# over re-decoding segments with ffmpeg. Built from a checksum-pinned source
# tarball for multi-arch (amd64/arm64); minimal feature set (no OCR/hardsubx).
FROM rust:1-bookworm@sha256:77fac8b98f9f46062bb680b6d25d5bcaabfc400143952ebc572e924bcbedc3fa AS ccextractor-builder
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget xz-utils gcc g++ make cmake pkg-config bash \
    zlib1g-dev libpng-dev libjpeg-dev libssl-dev libfreetype-dev \
    libxml2-dev libcurl4-gnutls-dev clang libclang-dev git \
    && rm -rf /var/lib/apt/lists/*

# GPAC is required by ccextractor and is not packaged for bookworm.
ARG GPAC_VERSION=v2.4.0
WORKDIR /root
RUN git clone -b "${GPAC_VERSION}" --depth 1 https://github.com/gpac/gpac.git
WORKDIR /root/gpac
RUN ./configure && make -j"$(nproc)" lib && make install-lib && ldconfig

ARG CCEXTRACTOR_VERSION=0.96.6
ARG CCEXTRACTOR_SHA256=cc292b6627067e0cdb5325fe74cade5a3881cbfb86096a527301a7320ff46486
WORKDIR /root
RUN wget -q "https://github.com/CCExtractor/ccextractor/releases/download/v${CCEXTRACTOR_VERSION}/ccextractor.${CCEXTRACTOR_VERSION}.tar.gz" -O /tmp/ccextractor.tar.gz \
	&& echo "${CCEXTRACTOR_SHA256}  /tmp/ccextractor.tar.gz" | sha256sum -c - \
	&& tar -xzf /tmp/ccextractor.tar.gz -C /root \
	&& rm /tmp/ccextractor.tar.gz

WORKDIR /root/ccextractor/linux
RUN ./pre-build.sh
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,target=/root/ccextractor/linux/rust,sharing=locked \
    cd ../src/rust && CARGO_TARGET_DIR=../../linux/rust cargo build --release \
	&& cp ../../linux/rust/release/libccx_rust.a ../../linux/libccx_rust.a
# Minimal build: EIA-608/708 from TS, no tesseract OCR / hardsubx.
RUN BLD_FLAGS="-std=gnu99 -Wno-write-strings -Wno-pointer-sign -D_FILE_OFFSET_BITS=64 -DVERSION_FILE_PRESENT -DFT2_BUILD_LIBRARY -DGPAC_DISABLE_VTT -DGPAC_DISABLE_OD_DUMP -DGPAC_DISABLE_REMOTERY -DNO_GZIP -DGPAC_64_BITS" \
	&& BLD_INCLUDE="-I../src -I../src/lib_ccx/ -I/usr/local/include -I/usr/include/gpac -I../src/thirdparty/libpng -I../src/thirdparty/zlib -I../src/lib_ccx/zvbi -I../src/thirdparty/lib_hash -I../src/thirdparty -I../src/thirdparty/freetype/include" \
	&& BLD_LINKER="-lm -Wl,--allow-multiple-definition -lpthread -ldl -L/usr/local/lib -lgpac ./libccx_rust.a" \
	&& SRC_LIBPNG="$(find ../src/thirdparty/libpng/ -name '*.c')" \
	&& SRC_ZLIB="$(find ../src/thirdparty/zlib/ -name '*.c')" \
	&& SRC_CCX="$(find ../src/lib_ccx/ -name '*.c')" \
	&& SRC_HASH="$(find ../src/thirdparty/lib_hash/ -name '*.c')" \
	&& SRC_UTF8PROC="../src/thirdparty/utf8proc/utf8proc.c" \
	&& SRC_FREETYPE="../src/thirdparty/freetype/autofit/autofit.c \
		../src/thirdparty/freetype/base/ftbase.c \
		../src/thirdparty/freetype/base/ftbbox.c \
		../src/thirdparty/freetype/base/ftbdf.c \
		../src/thirdparty/freetype/base/ftbitmap.c \
		../src/thirdparty/freetype/base/ftcid.c \
		../src/thirdparty/freetype/base/ftfntfmt.c \
		../src/thirdparty/freetype/base/ftfstype.c \
		../src/thirdparty/freetype/base/ftgasp.c \
		../src/thirdparty/freetype/base/ftglyph.c \
		../src/thirdparty/freetype/base/ftgxval.c \
		../src/thirdparty/freetype/base/ftinit.c \
		../src/thirdparty/freetype/base/ftlcdfil.c \
		../src/thirdparty/freetype/base/ftmm.c \
		../src/thirdparty/freetype/base/ftotval.c \
		../src/thirdparty/freetype/base/ftpatent.c \
		../src/thirdparty/freetype/base/ftpfr.c \
		../src/thirdparty/freetype/base/ftstroke.c \
		../src/thirdparty/freetype/base/ftsynth.c \
		../src/thirdparty/freetype/base/ftsystem.c \
		../src/thirdparty/freetype/base/fttype1.c \
		../src/thirdparty/freetype/base/ftwinfnt.c \
		../src/thirdparty/freetype/bdf/bdf.c \
		../src/thirdparty/freetype/bzip2/ftbzip2.c \
		../src/thirdparty/freetype/cache/ftcache.c \
		../src/thirdparty/freetype/cff/cff.c \
		../src/thirdparty/freetype/cid/type1cid.c \
		../src/thirdparty/freetype/gzip/ftgzip.c \
		../src/thirdparty/freetype/lzw/ftlzw.c \
		../src/thirdparty/freetype/pcf/pcf.c \
		../src/thirdparty/freetype/pfr/pfr.c \
		../src/thirdparty/freetype/psaux/psaux.c \
		../src/thirdparty/freetype/pshinter/pshinter.c \
		../src/thirdparty/freetype/psnames/psnames.c \
		../src/thirdparty/freetype/raster/raster.c \
		../src/thirdparty/freetype/sfnt/sfnt.c \
		../src/thirdparty/freetype/smooth/smooth.c \
		../src/thirdparty/freetype/truetype/truetype.c \
		../src/thirdparty/freetype/type1/type1.c \
		../src/thirdparty/freetype/type42/type42.c \
		../src/thirdparty/freetype/winfonts/winfnt.c" \
	&& gcc $BLD_FLAGS $BLD_INCLUDE -o ccextractor ../src/ccextractor.c $SRC_CCX $SRC_ZLIB $SRC_LIBPNG $SRC_HASH $SRC_UTF8PROC $SRC_FREETYPE $BLD_LINKER \
	&& mkdir -p /out \
	&& cp ccextractor /out/ccextractor \
	&& cp -a /usr/local/lib/libgpac.so* /out/ \
	&& chmod 0755 /out/ccextractor

# Download the architecture-specific FFmpeg build in a separate stage so the
# final image does not retain download tooling or package artifacts. Jellyfin's
# amd64 package provides the required legacy NVIDIA driver compatibility, but
# its arm64 package omits libplacebo. Keep using the checksum-pinned BtbN arm64
# build so Dolby Vision tone mapping remains available in both images.
#
# Jellyfin FFmpeg 7 is built against NVENC API 12.0 (Linux driver 520.56.06+).
# NVIDIA guarantees older NVENC client APIs remain binary-compatible with newer
# drivers, so one build covers legacy R535 vGPU deployments and current GPUs.
# Keep this package pinned: moving to an FFmpeg build compiled against a newer
# nv-codec-headers release can silently raise the minimum NVIDIA driver.
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS ffmpeg-downloader
ARG TARGETARCH
ARG JELLYFIN_FFMPEG_VERSION=7.1.4-3
ARG JELLYFIN_FFMPEG_AMD64_SHA256=0cf62bc2423822c9ec7a38dbc8f526d9a58671bd01843daa7817ece35619fc1c
# BtbN retains the final build of each month for two years; use that durable
# release instead of a daily artifact that is pruned after 14 days.
ARG BTBN_FFMPEG_RELEASE=autobuild-2026-07-31-14-10
ARG BTBN_FFMPEG_ARM64_ARCHIVE=ffmpeg-N-125875-g5d4d3bdc61-linuxarm64-gpl.tar.xz
ARG BTBN_FFMPEG_ARM64_SHA256=a38f9976ff6377ed0a1117ed726c580da968cc8a0e9dc1328297cc60673e6f92
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /out
RUN if [ "$TARGETARCH" = "arm64" ]; then \
		wget -q "https://github.com/BtbN/FFmpeg-Builds/releases/download/${BTBN_FFMPEG_RELEASE}/${BTBN_FFMPEG_ARM64_ARCHIVE}" -O ffmpeg-package.tar.xz; \
		echo "${BTBN_FFMPEG_ARM64_SHA256}  ffmpeg-package.tar.xz" | sha256sum -c -; \
	else \
		FFMPEG_PACKAGE="jellyfin-ffmpeg7_${JELLYFIN_FFMPEG_VERSION}-bookworm_amd64.deb"; \
		wget -q "https://github.com/jellyfin/jellyfin-ffmpeg/releases/download/v${JELLYFIN_FFMPEG_VERSION}/${FFMPEG_PACKAGE}" -O ffmpeg-package.deb; \
		echo "${JELLYFIN_FFMPEG_AMD64_SHA256}  ffmpeg-package.deb" | sha256sum -c -; \
	fi

# comskip finds commercial breaks in finished recordings (Live TV > Commercial
# Detection, off by default). Linked statically against a minimal FFmpeg (the
# decoders and parsers of broadcast MPEG-TS only), so the image needs no
# second set of FFmpeg libraries next to jellyfin-ffmpeg.
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS comskip-builder
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget xz-utils git build-essential autoconf automake libtool pkg-config nasm libargtable2-dev \
    && rm -rf /var/lib/apt/lists/*
ARG COMSKIP_FFMPEG_VERSION=5.1.6
ARG COMSKIP_FFMPEG_SHA256=f4fa066278f7a47feab316fef905f4db0d5e9b589451949740f83972b30901bd
RUN wget -q "https://ffmpeg.org/releases/ffmpeg-${COMSKIP_FFMPEG_VERSION}.tar.xz" -O /tmp/ffmpeg.tar.xz \
	&& echo "${COMSKIP_FFMPEG_SHA256}  /tmp/ffmpeg.tar.xz" | sha256sum -c - \
	&& mkdir /tmp/ffmpeg && tar -xJf /tmp/ffmpeg.tar.xz -C /tmp/ffmpeg --strip-components=1 \
	&& cd /tmp/ffmpeg \
	&& ./configure --prefix=/opt/comskip-ffmpeg --enable-static --disable-shared --disable-programs --disable-doc \
		--disable-network --disable-autodetect --disable-everything \
		--enable-avcodec --enable-avformat --enable-avutil --enable-swscale \
		--enable-protocol=file --enable-demuxer=mpegts,mpegtsraw,mov,matroska \
		--enable-decoder=h264,hevc,mpeg2video,mpeg1video,mp2,mp3,ac3,eac3,aac,aac_latm \
		--enable-parser=h264,hevc,mpegvideo,mpegaudio,ac3,aac,aac_latm \
	&& make -j"$(nproc)" && make install
ARG COMSKIP_COMMIT=a140b6ac8bc8f596729e9052819affc779c3b377
RUN git clone --filter=blob:none https://github.com/erikkaashoek/Comskip /tmp/comskip \
	&& cd /tmp/comskip && git checkout "${COMSKIP_COMMIT}" \
	&& ./autogen.sh \
	&& PKG_CONFIG_PATH=/opt/comskip-ffmpeg/lib/pkgconfig PKG_CONFIG="pkg-config --static" ./configure \
	&& make -j"$(nproc)" && strip comskip \
	&& mkdir -p /out && cp comskip /out/comskip \
	&& /out/comskip --help 2>&1 | grep -qi comskip

# Final stage
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
ARG TARGETARCH

COPY --from=ffmpeg-downloader /out/ffmpeg-package.* /tmp/

# Install ca-certificates, download tools, Python for title parsing, and Vulkan
# drivers. Mesa's Lavapipe remains available as a thumbnail-only software
# fallback, while real GPU ICDs can be selected when devices are passed through.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget xz-utils python3 python3-venv python3-pip \
    libvulkan1 mesa-vulkan-drivers \
    libssl3 libpng16-16 libjpeg62-turbo libopenjp2-7 \
    && if [ "$TARGETARCH" = "arm64" ]; then \
        tar -xJf /tmp/ffmpeg-package.tar.xz -C /tmp; \
        FFMPEG_DIR="$(find /tmp -maxdepth 1 -type d -name 'ffmpeg-*-linuxarm64-gpl' | head -n 1)"; \
        test -n "$FFMPEG_DIR"; \
        mv "$FFMPEG_DIR/bin/ffmpeg" "$FFMPEG_DIR/bin/ffprobe" /usr/local/bin/; \
    else \
        apt-get install -y --no-install-recommends /tmp/ffmpeg-package.deb; \
        ln -sf /usr/lib/jellyfin-ffmpeg/ffmpeg /usr/local/bin/ffmpeg; \
        ln -sf /usr/lib/jellyfin-ffmpeg/ffprobe /usr/local/bin/ffprobe; \
    fi \
    && rm -rf /var/lib/apt/lists/* /tmp/ffmpeg-package.* /tmp/ffmpeg-*-linuxarm64-gpl \
    && chmod 0755 /usr/local/bin/ffmpeg /usr/local/bin/ffprobe

RUN ln -sf "$(find /usr/share/vulkan/icd.d -name 'lvp_icd*.json' | head -n 1)" /usr/share/vulkan/icd.d/lavapipe_icd.json

# Create Python virtual environment and install dependencies
RUN python3 -m venv /.venv && \
    /.venv/bin/pip install --no-cache-dir parsett==1.8.5 subliminal==2.6.0

# Guard the compatibility contract and Dolby Vision feature set at image build
# time. The driver-floor string is compiled into libavcodec by FFmpeg's NVENC
# integration; checking it prevents a package update from silently requiring a
# newer driver. DoVi checks cover parsing, metadata editing, and libplacebo
# RPU-aware tone mapping used by web playback and thumbnails. Jellyfin's
# fork-specific remove_dovi option is required only of the amd64 package; the
# backend does not invoke it, and the BtbN arm64 build provides standard
# hevc_metadata instead.
RUN if [ "$TARGETARCH" = "amd64" ]; then \
        grep -aFq '520.56.06' /usr/lib/jellyfin-ffmpeg/lib/libavcodec.so.61 \
            && grep -aFq 'DOVI configuration record' /usr/lib/jellyfin-ffmpeg/lib/libavcodec.so.61; \
    else \
        grep -aFq 'DOVI configuration record' /usr/local/bin/ffmpeg; \
    fi \
    && ffmpeg -hide_banner -encoders | grep -Eq '(^|[[:space:]])h264_nvenc([[:space:]]|$)' \
    && ffmpeg -hide_banner -filters | grep -Eq '(^|[[:space:]])libplacebo([[:space:]]|$)' \
    && ffmpeg -hide_banner -filters | grep -Eq '(^|[[:space:]])zscale([[:space:]]|$)' \
    && ffmpeg -hide_banner -h filter=libplacebo | grep -Eq '(^|[[:space:]])apply_dolbyvision([[:space:]]|$)' \
    && if [ "$TARGETARCH" = "amd64" ]; then \
        ffmpeg -hide_banner -h bsf=hevc_metadata | grep -Eq '(^|[[:space:]])-remove_dovi([[:space:]]|$)'; \
    else \
        ffmpeg -hide_banner -bsfs | grep -Eq '(^|[[:space:]])hevc_metadata([[:space:]]|$)'; \
    fi
RUN LIBGL_ALWAYS_SOFTWARE=1 \
    VK_ICD_FILENAMES=/usr/share/vulkan/icd.d/lavapipe_icd.json \
    ffmpeg -hide_banner -loglevel error \
        -f lavfi -i color=c=black:s=16x16:d=0.1 \
        -frames:v 1 \
        -vf "libplacebo=w=16:h=16:colorspace=bt709:color_primaries=bt709:color_trc=bt709,format=yuv420p" \
        -f null - \
    || echo "warning: libplacebo runtime probe failed during image build; application runtime probe will select fallback"

# Live TV closed-caption extractor (EIA-608 bitstream parse). Built multi-arch
# in the ccextractor-builder stage; libgpac is a shared runtime dependency.
COPY --from=ccextractor-builder --chown=root:root --chmod=0755 /out/ccextractor /usr/local/bin/ccextractor
COPY --from=ccextractor-builder /out/libgpac.so* /usr/local/lib/
RUN ldconfig \
	&& ccextractor -V >/dev/null

# Commercial detection (see comskip-builder); argtable2 is its one shared library.
RUN apt-get update && apt-get install -y --no-install-recommends libargtable2-0 \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=comskip-builder --chown=root:root --chmod=0755 /out/comskip /usr/local/bin/comskip
RUN comskip --help 2>&1 | grep -qi comskip

# The release build resolves latest stable once and passes the version/checksum
# as build args. Keep verified defaults for direct Docker builds and rollback.
ARG YTDLP_VERSION=2026.08.19
ARG YTDLP_SHA256=1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6
LABEL io.mediastorm.ytdlp.version="${YTDLP_VERSION}"
RUN wget -q "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp" -O /usr/local/bin/yt-dlp \
	&& echo "${YTDLP_SHA256}  /usr/local/bin/yt-dlp" | sha256sum -c - \
	&& chmod +x /usr/local/bin/yt-dlp \
	&& test "$(/usr/local/bin/yt-dlp --version)" = "${YTDLP_VERSION}"

# Install deno (JavaScript runtime required by yt-dlp for YouTube challenge solving)
ARG DENO_VERSION=2.9.3
RUN if [ "$TARGETARCH" = "arm64" ]; then \
		DENO_ARCH="aarch64-unknown-linux-gnu"; \
		DENO_SHA256="753937db98a4b56cbbbd26e8f00eb4b789191a229afec93f74bcfa4e79bc2c8b"; \
	else \
		DENO_ARCH="x86_64-unknown-linux-gnu"; \
		DENO_SHA256="8101865641cbede56f08ad19c0a67a87df84bce127fee0d3e3e1f7467717ffa6"; \
	fi && \
	wget -q "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/deno-${DENO_ARCH}.zip" -O /tmp/deno.zip \
	&& echo "${DENO_SHA256}  /tmp/deno.zip" | sha256sum -c - \
	&& apt-get update && apt-get install -y --no-install-recommends unzip \
    && unzip -q /tmp/deno.zip -d /usr/local/bin/ \
    && rm /tmp/deno.zip \
    && chmod +x /usr/local/bin/deno \
    && apt-get purge -y unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /app /root/cache \
	&& chmod 0755 /app \
	&& chmod 0711 /root \
	&& ln -s /root/cache /app/cache \
	&& ln -s /app/mediastorm /root/mediastorm

ENV HOME=/tmp
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder --chown=root:root --chmod=0755 /app/mediastorm .

# Copy the web client. The backend serves it from /watch.
COPY --from=web-builder /web/dist /opt/strmr-web
ENV STRMR_WEB_APP_DIR=/opt/strmr-web

# Copy the iroh remote-access bridge binary. MEDIASTORM_IROH_DIRECT_DIR points the
# host manager at this dir; it runs <dir>/iroh-direct-spike directly (no cargo needed).
COPY --from=iroh-builder /iroh/iroh-direct-spike /opt/iroh/iroh-direct-spike
ENV MEDIASTORM_IROH_DIRECT_DIR=/opt/iroh

# Copy version file
COPY --chown=root:root --chmod=0444 backend/version.txt /app/version.txt

# Copy Python scripts to root (title parsing, subtitle search, credits detection)
COPY --chown=root:root --chmod=0555 backend/parse_title.py backend/parse_title_batch.py backend/search_subtitles.py backend/download_subtitle.py backend/detect_credits.py /

# Expose port
EXPOSE 7777

# Ask Docker to use the signal handled by mediastorm's graceful shutdown path.
STOPSIGNAL SIGTERM

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:7777/health > /dev/null || exit 1

# Run the application
CMD ["./mediastorm"]
