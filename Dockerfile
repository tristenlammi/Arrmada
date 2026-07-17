# syntax=docker/dockerfile:1

# --- Stage 1: build the web UI ---
FROM node:20-alpine AS web
WORKDIR /src/web
# Install deps first (cached unless the lockfile changes).
COPY web/package.json web/package-lock.json ./
RUN npm ci
# Build the SPA. Vite emits to ../internal/webui/dist (outside web/), so make it.
COPY web/ ./
RUN mkdir -p /src/internal/webui/dist && npm run build

# --- Stage 2: build the static Go binary (UI embedded) ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overwrite the embed dir with the freshly built UI from stage 1.
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
ARG COMMIT=unknown
# CGO off → fully static (modernc.org/sqlite is pure Go), so it runs on scratch.
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w \
      -X github.com/tristenlammi/arrmada/internal/buildinfo.Version=${VERSION} \
      -X github.com/tristenlammi/arrmada/internal/buildinfo.Commit=${COMMIT}" \
    -o /out/arrmada ./cmd/arrmada

# --- Stage 3: HDR tooling (musl static binaries for HDR10+ / Dolby Vision metadata) ---
FROM alpine:3.20 AS hdrtools
ARG DOVI_VERSION=2.3.3
ARG HDR10PLUS_VERSION=1.7.2
RUN apk add --no-cache wget tar && set -eux; \
    wget -qO /tmp/dovi.tgz "https://github.com/quietvoid/dovi_tool/releases/download/${DOVI_VERSION}/dovi_tool-${DOVI_VERSION}-x86_64-unknown-linux-musl.tar.gz" && \
    mkdir -p /tmp/dovi && tar -xzf /tmp/dovi.tgz -C /tmp/dovi && \
    cp "$(find /tmp/dovi -type f -name dovi_tool | head -1)" /usr/local/bin/dovi_tool && \
    wget -qO /tmp/h10.tgz "https://github.com/quietvoid/hdr10plus_tool/releases/download/${HDR10PLUS_VERSION}/hdr10plus_tool-${HDR10PLUS_VERSION}-x86_64-unknown-linux-musl.tar.gz" && \
    mkdir -p /tmp/h10 && tar -xzf /tmp/h10.tgz -C /tmp/h10 && \
    cp "$(find /tmp/h10 -type f -name hdr10plus_tool | head -1)" /usr/local/bin/hdr10plus_tool && \
    chmod +x /usr/local/bin/dovi_tool /usr/local/bin/hdr10plus_tool && \
    /usr/local/bin/dovi_tool --version && /usr/local/bin/hdr10plus_tool --version

# --- Stage 3b: whisper.cpp CLI (local AI subtitle generation) ---
# CPU build for now — reliable everywhere; GPU (Vulkan on the Arc) is a follow-up. Models are NOT
# baked in (multi-GB); the app downloads them on demand into the data dir.
FROM alpine:3.20 AS whisper
ARG WHISPER_VERSION=v1.7.5
RUN apk add --no-cache git cmake g++ make
RUN git clone --depth 1 --branch ${WHISPER_VERSION} https://github.com/ggerganov/whisper.cpp /src/whisper && \
    cd /src/whisper && \
    cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF && \
    cmake --build build -j"$(nproc)" && \
    (cp build/bin/whisper-cli /usr/local/bin/whisper-cli 2>/dev/null || cp build/bin/main /usr/local/bin/whisper-cli) && \
    strip /usr/local/bin/whisper-cli && \
    ldd /usr/local/bin/whisper-cli || true

# --- Stage 4: minimal runtime ---
FROM alpine:3.20
# apprise (Python) is bundled for notifications — one image, 80+ services, no extra container.
# su-exec lets the entrypoint drop from root to a configurable PUID/PGID.
# intel-media-driver (iHD) + libva enable Intel Quick Sync / VAAPI hardware transcode
# when /dev/dri is passed into the container (Gen8+ HEVC/H.264; Gen12.5+/Arc also AV1).
# libva-utils ships `vainfo` for diagnosing the GPU.
# libstdc++ + libgomp are whisper-cli's runtime deps (C++/OpenMP).
RUN apk add --no-cache ca-certificates wget ffmpeg python3 py3-pip su-exec \
        libva libva-utils intel-media-driver mesa-va-gallium libstdc++ libgomp && \
    pip3 install --no-cache-dir --break-system-packages apprise && \
    apprise --version && \
    mkdir -p /data /media/downloads /media/library /transcode
COPY --from=build /out/arrmada /usr/local/bin/arrmada
# Dolby Vision (dovi_tool) + HDR10+ (hdr10plus_tool) metadata extractors/injectors.
COPY --from=hdrtools /usr/local/bin/dovi_tool /usr/local/bin/hdr10plus_tool /usr/local/bin/
# whisper.cpp CLI for local AI subtitle generation (models downloaded on demand into /data/whisper).
COPY --from=whisper /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Runs as root only long enough to fix data-dir ownership, then drops to PUID:PGID.
ENV ARRMADA_HOST=0.0.0.0 \
    ARRMADA_PORT=7878 \
    ARRMADA_DATA_DIR=/data \
    ARRMADA_LIBRARY_DIR=/media/library \
    ARRMADA_DOWNLOADS_DIR=/media/downloads \
    LIBVA_DRIVER_NAME=iHD \
    PUID=1000 \
    PGID=1000
EXPOSE 7878
VOLUME ["/data", "/media"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:7878/api/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
