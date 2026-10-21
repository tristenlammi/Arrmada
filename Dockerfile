# syntax=docker/dockerfile:1

# Memory. Docker builds the stages side by side, and with four cores this file used to
# peak at 6.5 GB: whisper.cpp's compilers (one of them 3.6 GB on its own, see the patch
# below) running next to the Go compiler and Intel's. On a small server one of them was
# killed part-way, and all Docker showed was the last line, e.g. "go build ... exit code 1".
#
# So the big compiles (the web UI, the Go binary and the two whisper.cpp builds) run
# through docker/build-heavy under one shared lock — the cache mount with
# id=arrmada-build-lock and sharing=locked, which one step holds at a time — and
# build-heavy fits each one's parallel jobs to the free memory. The image now builds in
# 2 GB (peak 2.0 GB measured; a cold build takes about 20% longer on four cores, since the
# compiles no longer overlap). ARRMADA_BUILD_JOBS (a build argument) fixes the job count.
#
# Platforms: linux/amd64 and linux/arm64, from this one file. The web UI and the Go binary
# are built on the build machine's own platform (Go cross-compiles; the UI is just files),
# so an arm64 image built under emulation only emulates what has to run as arm64. The GPU
# pieces are amd64 only: Intel's oneAPI (SYCL) whisper and Level Zero driver, and the
# Vulkan whisper. arm64 gets a CPU-only whisper instead; the runtime-amd64 and
# runtime-arm64 stages near the end hold everything that differs, and the final stage
# picks one by TARGETARCH (which BuildKit sets), so an arm64 build never touches the
# amd64-only stages. (The amd64 whisper stages changed too, for CPU dispatch; see below.)

# --- Stage 1: build the web UI ---
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY --chmod=755 docker/build-heavy /usr/local/bin/build-heavy
# Install deps first (cached unless the lockfile changes).
COPY web/package.json web/package-lock.json ./
RUN npm ci
# Build the SPA. Vite emits to ../internal/webui/dist (outside web/), so make it.
COPY web/ ./
ARG ARRMADA_BUILD_JOBS
RUN --mount=type=cache,id=arrmada-build-lock,target=/run/build-lock,sharing=locked \
    mkdir -p /src/internal/webui/dist && build-heavy 800 'npm run build'

# --- Stage 2: build the static Go binary (UI embedded) ---
# The Go version is pinned to the patch release. A floating tag (golang:1.26-alpine) is
# only fetched when the build machine doesn't have it yet, so a machine with an older copy
# keeps building with standard-library security holes a later patch fixed. Move it to
# each new patch (CI's govulncheck flags one); keep the aio-supervisor stage below on the same version.
FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine AS build
WORKDIR /src
COPY --chmod=755 docker/build-heavy /usr/local/bin/build-heavy
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overwrite the embed dir with the freshly built UI from stage 1.
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
ARG COMMIT=unknown
ARG ARRMADA_BUILD_JOBS
# CGO off → fully static (modernc.org/sqlite is pure Go), so it runs on scratch. The
# compiler's memory is mostly the one big SQLite package, so it isn't split into jobs.
# GOOS/GOARCH are the image's platform: this stage runs on the build machine's own, and Go
# cross-compiles an arm64 binary without emulation.
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,id=arrmada-build-lock,target=/run/build-lock,sharing=locked \
    build-heavy 900 'CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w \
      -X github.com/tristenlammi/arrmada/internal/buildinfo.Version=${VERSION} \
      -X github.com/tristenlammi/arrmada/internal/buildinfo.Commit=${COMMIT}" \
    -o /out/arrmada ./cmd/arrmada'

# --- Stage 3: HDR tooling (musl static binaries for HDR10+ / Dolby Vision metadata) ---
FROM alpine:3.20 AS hdrtools
# Pinned, and checked: dovi_tool carries Dolby Vision through Convert's "keep" choice and
# hdr10plus_tool HDR10+ through every HEVC conversion, so a swapped release binary would
# write the metadata of every converted film. The releases publish no checksums, so these
# are the SHA-256 of the binaries inside those tarballs, one per platform (both projects
# publish the same musl build for x86-64 and aarch64); a version bump updates them too.
ARG DOVI_VERSION=2.3.3
ARG DOVI_SHA256_AMD64=871a0a5c44d1fd6baa6b972ec65167095e572f572d6c4da80c4b29483e4dfc1e
ARG DOVI_SHA256_ARM64=7958fd93b551bba91720239a645307281c6d5b8c46ca6b327071b353e476ad11
ARG HDR10PLUS_VERSION=1.7.2
ARG HDR10PLUS_SHA256_AMD64=7845916b549c36e5d7fe9dbb3d24c124466d7c71ec3442e207551b677949d0be
ARG HDR10PLUS_SHA256_ARM64=90caed8d955ba7c16e485b23fb34555c95224568869b50daefa5f52ef4824a24
ARG TARGETARCH
RUN apk add --no-cache wget tar && set -eux; \
    case "${TARGETARCH}" in \
        amd64) TRIPLE=x86_64-unknown-linux-musl; DOVI_SHA256=${DOVI_SHA256_AMD64}; HDR10PLUS_SHA256=${HDR10PLUS_SHA256_AMD64} ;; \
        arm64) TRIPLE=aarch64-unknown-linux-musl; DOVI_SHA256=${DOVI_SHA256_ARM64}; HDR10PLUS_SHA256=${HDR10PLUS_SHA256_ARM64} ;; \
        *) echo "Arrmada builds for amd64 and arm64, not ${TARGETARCH}." >&2; exit 1 ;; \
    esac; \
    wget -qO /tmp/dovi.tgz "https://github.com/quietvoid/dovi_tool/releases/download/${DOVI_VERSION}/dovi_tool-${DOVI_VERSION}-${TRIPLE}.tar.gz" && \
    mkdir -p /tmp/dovi && tar -xzf /tmp/dovi.tgz -C /tmp/dovi && \
    cp "$(find /tmp/dovi -type f -name dovi_tool | head -1)" /usr/local/bin/dovi_tool && \
    wget -qO /tmp/h10.tgz "https://github.com/quietvoid/hdr10plus_tool/releases/download/${HDR10PLUS_VERSION}/hdr10plus_tool-${HDR10PLUS_VERSION}-${TRIPLE}.tar.gz" && \
    mkdir -p /tmp/h10 && tar -xzf /tmp/h10.tgz -C /tmp/h10 && \
    cp "$(find /tmp/h10 -type f -name hdr10plus_tool | head -1)" /usr/local/bin/hdr10plus_tool && \
    echo "${DOVI_SHA256}  /usr/local/bin/dovi_tool" | sha256sum -c - && \
    echo "${HDR10PLUS_SHA256}  /usr/local/bin/hdr10plus_tool" | sha256sum -c - && \
    chmod +x /usr/local/bin/dovi_tool /usr/local/bin/hdr10plus_tool && \
    /usr/local/bin/dovi_tool --version && /usr/local/bin/hdr10plus_tool --version

# --- Stage 3a: kepubify (EPUB → KEPUB for Kobo's stock reader, made on download) ---
# Pinned and checked like the HDR tools: the release publishes no checksums, so these are
# the SHA-256 of the two static Go binaries (kepubify-linux-64bit, kepubify-linux-arm64)
# as published for that tag; a version bump updates them too. MIT licensed.
FROM alpine:3.20 AS kepubify
ARG KEPUBIFY_VERSION=v4.0.4
ARG KEPUBIFY_SHA256_AMD64=37d7628d26c5c906f607f24b36f781f306075e7073a6fe7820a751bb60431fc5
ARG KEPUBIFY_SHA256_ARM64=5a15b8f6f6a96216c69330601bca29638cfee50f7bf48712795cff88ae2d03a3
ARG TARGETARCH
RUN apk add --no-cache wget && set -eux; \
    case "${TARGETARCH}" in \
        amd64) ASSET=kepubify-linux-64bit; SHA=${KEPUBIFY_SHA256_AMD64} ;; \
        arm64) ASSET=kepubify-linux-arm64; SHA=${KEPUBIFY_SHA256_ARM64} ;; \
        *) echo "Arrmada builds for amd64 and arm64, not ${TARGETARCH}." >&2; exit 1 ;; \
    esac; \
    wget -qO /usr/local/bin/kepubify "https://github.com/pgaskin/kepubify/releases/download/${KEPUBIFY_VERSION}/${ASSET}" && \
    echo "${SHA}  /usr/local/bin/kepubify" | sha256sum -c - && \
    chmod +x /usr/local/bin/kepubify && \
    /usr/local/bin/kepubify --version

# --- Stage 3b: whisper.cpp CLI (local AI subtitle generation), amd64 ---
# Built with the Vulkan backend so the GPU that already does hardware transcoding (Intel
# Arc / any Mesa- or proprietary-Vulkan card behind /dev/dri) also runs the AI. Vulkan
# is the one backend that covers Intel, AMD and NVIDIA from a single binary; on a host
# with no usable Vulkan device whisper-cli falls back to the CPU on its own, and the app
# retries with --no-gpu if it doesn't. Models are NOT baked in (multi-GB); the app
# downloads them on demand into the data dir.
# Built on Debian, matching the runtime: a musl-linked binary can't run on a glibc image.
#
# The CPU code is picked at run time, not compiled for the build machine. ggml's default
# is -march=native, which made the published whisper-cli need whatever the release
# runner's CPU had: it died with "Illegal instruction" on any CPU without AVX2 (a Celeron
# J4125 or N5105, an older i5), and would on any without AVX-512 if a runner had it.
# GGML_BACKEND_DL + GGML_CPU_ALL_VARIANTS build the CPU backend once per instruction-set
# level (x64, SSE4.2, AVX, AVX2, AVX-512 …) as loadable libggml-cpu-*.so files, and
# whisper-cli loads the best one this CPU can run, next to libggml-vulkan.so. They live
# with the binary in /opt/whisper (whisper-cli finds its backends in its own folder, and
# its libraries through an $ORIGIN RPATH).
FROM debian:bookworm-slim AS whisper
# v1.9.2+ matters: earlier builds didn't map timestamps back through VAD correctly
# (v1.8.4 fixed drift, v1.9.2 the token-level ones), which is what misaligned subtitles.
ARG WHISPER_VERSION=v1.9.3
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake g++ make ca-certificates libvulkan-dev glslc spirv-headers && rm -rf /var/lib/apt/lists/*
# Bookworm's Vulkan headers (1.3.239) predate VK_EXT_layer_settings, which ggml's Vulkan
# backend now uses; the headers are header-only, so install a current set under
# /usr/local and point CMake at them. The loader (libvulkan-dev) stays the distro's.
ARG VULKAN_HEADERS_VERSION=v1.4.357
RUN git clone --depth 1 --branch ${VULKAN_HEADERS_VERSION} https://github.com/KhronosGroup/Vulkan-Headers /src/vk-headers && \
    cmake -S /src/vk-headers -B /src/vk-headers/build && \
    cmake --install /src/vk-headers/build --prefix /usr/local
# No trailing "|| true" on this chain: it used to swallow a failed build and produce an
# image with no whisper-cli in it, which the app then reported as "AI unavailable".
# The patch writes the embedded shaders as string literals, which g++ compiles in a
# fraction of the memory (3.6 GB → 0.4 GB for the biggest file; see the patch, which
# fails the build if it ever stops applying). The --param pair makes g++ collect its
# garbage sooner, as it would on a small machine: the largest remaining file,
# ggml-vulkan.cpp, then peaks at 1.5 GB instead of 1.8, for about 5% more time on it.
COPY docker/whisper-vulkan-shaders.patch /tmp/whisper-vulkan-shaders.patch
RUN git clone --depth 1 --branch ${WHISPER_VERSION} https://github.com/ggerganov/whisper.cpp /src/whisper && \
    cd /src/whisper && \
    git apply /tmp/whisper-vulkan-shaders.patch && \
    cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON -DGGML_VULKAN=1 \
          -DGGML_NATIVE=OFF -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON \
          -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON -DCMAKE_INSTALL_RPATH='$ORIGIN' \
          -DVulkan_INCLUDE_DIR=/usr/local/include \
          -DCMAKE_CXX_FLAGS="--param ggc-min-expand=30 --param ggc-min-heapsize=65536"
COPY --chmod=755 docker/build-heavy /usr/local/bin/build-heavy
ARG ARRMADA_BUILD_JOBS
# Fails the build unless the backends are all there and every library resolves: a backend
# that can't load is skipped without a word at run time, so a missing libggml-vulkan.so
# would only ever show up as "running on the CPU".
RUN --mount=type=cache,id=arrmada-build-lock,target=/run/build-lock,sharing=locked \
    cd /src/whisper && \
    build-heavy 1500 'cmake --build build -j"$JOBS" --target whisper-cli' && \
    mkdir -p /out && cp -a build/bin/whisper-cli build/bin/*.so* /out/ && \
    find /out -type f -exec strip --strip-unneeded {} + && \
    test -f /out/libggml-vulkan.so && \
    for v in x64 sse42 haswell; do test -f "/out/libggml-cpu-$v.so" || { echo "libggml-cpu-$v.so missing" >&2; exit 1; }; done && \
    for f in /out/whisper-cli /out/*.so; do \
        if ldd "$f" | grep "not found"; then echo "$f: unresolved" >&2; exit 1; fi; \
    done && \
    ls -la /out

# --- Stage 3c: whisper.cpp with Intel's oneAPI (SYCL) backend, amd64 ---
# The Vulkan build never touches an Arc A-series card's XMX matrix units: ggml's Vulkan
# backend switches cooperative-matrix off for Alchemist outright ("performance
# regressions"), which left a 55-minute episode taking 38 minutes on an A380. SYCL is the
# backend Intel maintains: the whole model runs on the GPU through Level Zero with the
# matrix units in play. Same whisper.cpp tag, built with Intel's compiler; the app runs
# this build first and falls back to the Vulkan one when it fails or finds no GPU.
#
# The runtime libraries it needs (SYCL, oneMKL, oneDNN, the compiler runtime) are
# collected from ldd into one directory, so the runtime image carries a few hundred MB
# of libraries rather than the multi-GB toolkit. The Unified Runtime adapters are
# dlopen'd, not linked, so they're copied by name.
#
# Its CPU code is built for SSE4.2 (x86-64-v2), not for the build machine: ggml's default
# -march=native would make it need whatever the release runner's CPU has. The run-time
# CPU choice the Vulkan build uses doesn't fit here: with loadable backends, the SYCL one
# initialises as whisper-cli starts and aborts ("No device of requested type available")
# on every host without an Intel GPU, even for --help. This build only runs where an
# Intel GPU does the work, so its CPU share is small; every CPU-only run (including
# after this build is set aside) goes to the Vulkan build and its best CPU backend.
# GGML_NATIVE=OFF alone isn't enough: ggml then switches AVX/AVX2/FMA/F16C/BMI2 on, so
# each is turned off by name.
FROM intel/deep-learning-essentials:2025.3.3-0-devel-ubuntu24.04 AS whisper-sycl
ARG WHISPER_VERSION=v1.9.3
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake make ca-certificates && rm -rf /var/lib/apt/lists/*
# WHISPER_SYCL_NATIVE=1 builds that CPU code for the build machine instead: right for an
# image built and run on the same server (docker-compose.yml passes it for builds there),
# never for one published to other machines. CI and releases leave it at 0.
ARG WHISPER_SYCL_NATIVE=0
RUN git clone --depth 1 --branch ${WHISPER_VERSION} https://github.com/ggerganov/whisper.cpp /src/whisper && \
    cd /src/whisper && \
    (. /opt/intel/oneapi/setvars.sh --force >/dev/null 2>&1 || true) && \
    if [ "${WHISPER_SYCL_NATIVE}" = 1 ]; then \
        CPU_FLAGS="-DGGML_NATIVE=ON"; \
    else \
        CPU_FLAGS="-DGGML_NATIVE=OFF -DGGML_SSE42=ON -DGGML_AVX=OFF -DGGML_AVX2=OFF -DGGML_FMA=OFF -DGGML_F16C=OFF -DGGML_BMI2=OFF"; \
    fi && \
    echo "SYCL whisper CPU flags: ${CPU_FLAGS}" && \
    cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
          -DGGML_SYCL=ON -DGGML_SYCL_F16=ON ${CPU_FLAGS} \
          -DCMAKE_C_COMPILER=icx -DCMAKE_CXX_COMPILER=icpx
COPY --chmod=755 docker/build-heavy /usr/local/bin/build-heavy
ARG ARRMADA_BUILD_JOBS
# About 0.6 GB per icpx job (2.1 GB with four).
RUN --mount=type=cache,id=arrmada-build-lock,target=/run/build-lock,sharing=locked \
    cd /src/whisper && \
    build-heavy 600 'cmake --build build -j"$JOBS" --target whisper-cli'
RUN cd /src/whisper && \
    mkdir -p /out/lib && cp build/bin/whisper-cli /out/whisper-cli && \
    ldd /out/whisper-cli | awk '/=> \/opt\/intel/{print $3}' | xargs -r -I{} cp -L {} /out/lib/ && \
    cp -L /opt/intel/oneapi/compiler/latest/lib/libur_adapter_level_zero*.so* \
          /opt/intel/oneapi/compiler/latest/lib/libur_adapter_opencl*.so* /out/lib/ && \
    # The adapters have dependencies of their own that the binary doesn't (libumf, for
    # one): sweep those too, repeating until nothing new turns up.
    for pass in 1 2 3; do \
        for f in /out/lib/*.so*; do ldd "$f" | awk '/=> \/opt\/intel/{print $3}'; done | sort -u \
            | xargs -r -I{} cp -Ln {} /out/lib/; \
    done && \
    for f in /out/whisper-cli /out/lib/libur_adapter_*.so.0; do \
        if LD_LIBRARY_PATH=/out/lib ldd "$f" | grep "not found"; then echo "$f: unresolved" >&2; exit 1; fi; \
    done && \
    du -sh /out/lib && ls -la /out/lib

# --- Stage 3d: whisper.cpp CLI, CPU only, arm64 ---
# No GPU backend on ARM: oneAPI is x86-only, and the GPUs these boxes have (a Raspberry
# Pi's VideoCore, a NAS's Mali) are slower than their own CPU cores at this through
# Vulkan, when ggml runs on them at all. Same whisper.cpp tag as the other builds.
#
# GGML_NATIVE=OFF matters: left on, ggml compiles for the build machine's own CPU
# (-mcpu=native), and the arm64 image is built on a server-class Neoverse core; the
# result would die with "Illegal instruction" on a Raspberry Pi 4. armv8-a is every
# 64-bit ARM CPU (NEON included).
FROM debian:bookworm-slim AS whisper-cpu
ARG WHISPER_VERSION=v1.9.3
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake g++ make ca-certificates && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch ${WHISPER_VERSION} https://github.com/ggerganov/whisper.cpp /src/whisper && \
    cd /src/whisper && \
    cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
          -DGGML_NATIVE=OFF -DGGML_CPU_ARM_ARCH=armv8-a
COPY --chmod=755 docker/build-heavy /usr/local/bin/build-heavy
ARG ARRMADA_BUILD_JOBS
RUN --mount=type=cache,id=arrmada-build-lock,target=/run/build-lock,sharing=locked \
    cd /src/whisper && \
    build-heavy 600 'cmake --build build -j"$JOBS" --target whisper-cli' && \
    cp build/bin/whisper-cli /usr/local/bin/whisper-cli && \
    strip /usr/local/bin/whisper-cli && \
    (ldd /usr/local/bin/whisper-cli || true)

# --- Stage 4: runtime ---
#
# Ubuntu 24.04 + jellyfin-ffmpeg, NOT Alpine's ffmpeg package. (Ubuntu rather than
# Debian since the SYCL build moved in: Intel publishes its Level Zero GPU driver for
# Ubuntu releases, and jellyfin-ffmpeg is built for both.)
#
# Alpine's libx265 segfaults at frame zero on some newer CPUs (reproduced on a Core Ultra
# 285K: the file decodes fine, libx264 encodes fine, VAAPI encodes fine, and libx265 dies
# instantly on every file even with asm=0). That removes CPU encoding entirely, and with it
# HDR conversion — every HDR metadata path encodes through x265.
#
# jellyfin-ffmpeg is built for exactly this job: verified working x265, VAAPI AND Quick Sync
# on the same hardware where Alpine's build failed all three (QSV had been exiting 171). It
# also bundles the Intel media drivers rather than depending on whatever the base image
# ships, which is one less thing to drift.
#
# Everything that differs by platform is in runtime-amd64 / runtime-arm64; the final stage
# starts from the one for the image's platform and adds what both share.

# amd64: the GPU stack (VAAPI/Quick Sync drivers, Vulkan, Level Zero) and both GPU whisper
# builds.
FROM ubuntu:24.04 AS runtime-amd64
# gosu is the su-exec equivalent: the entrypoint drops from root to a configurable PUID/PGID.
# apprise (Python) is bundled for notifications — one image, 80+ services, no extra container.
# It and its dependencies are pinned with hashes (docker/apprise-requirements.txt).
# jellyfin-ffmpeg7 brings libva and the Intel media drivers it needs for /dev/dri passthrough.
# libgomp1 is the Vulkan whisper-cli's OpenMP runtime; vainfo helps diagnose the GPU.
# libvulkan1 + mesa-vulkan-drivers give whisper-cli's Vulkan backend an Intel/AMD device
# through the same /dev/dri passthrough VAAPI uses; vulkaninfo (vulkan-tools) shows whether
# the container can actually see it.
# libze1 + libze-intel-gpu1 (Intel's Level Zero loader and GPU driver, from Intel's own
# repository) are what the SYCL whisper build talks to; intel-opencl-icd rides along so
# clinfo can show whether the compute side of the card is visible.
COPY docker/apprise-requirements.txt /tmp/apprise-requirements.txt
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg gosu python3 python3-pip libgomp1 vainfo \
        libvulkan1 mesa-vulkan-drivers vulkan-tools ocl-icd-libopencl1 clinfo && \
    mkdir -p /etc/apt/keyrings && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
        | gpg --dearmor -o /etc/apt/keyrings/jellyfin.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/jellyfin.gpg arch=amd64] https://repo.jellyfin.org/ubuntu noble main" \
        > /etc/apt/sources.list.d/jellyfin.list && \
    curl -fsSL https://repositories.intel.com/gpu/intel-graphics.key \
        | gpg --dearmor -o /etc/apt/keyrings/intel-graphics.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/intel-graphics.gpg arch=amd64] https://repositories.intel.com/gpu/ubuntu noble unified" \
        > /etc/apt/sources.list.d/intel-gpu.list && \
    apt-get update && apt-get install -y --no-install-recommends \
        jellyfin-ffmpeg7 libze1 libze-intel-gpu1 intel-opencl-icd && \
    rm -rf /var/lib/apt/lists/* && \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffmpeg /usr/local/bin/ffmpeg && \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffprobe /usr/local/bin/ffprobe && \
    pip3 install --no-cache-dir --break-system-packages --require-hashes --only-binary :all: \
        -r /tmp/apprise-requirements.txt && rm /tmp/apprise-requirements.txt && \
    apprise --version && ffmpeg -version | head -1 && \
    mkdir -p /data /media/downloads /media/library /transcode
# whisper.cpp CLI for local AI subtitle generation (models downloaded on demand into
# /data/whisper): the binary with its backends and libraries in /opt/whisper, and a link
# on the PATH (whisper-cli resolves the link to find its folder).
COPY --from=whisper /out/ /opt/whisper/
# The oneAPI build and its libraries, behind a wrapper that sets the library path and
# device selection. The app prefers it and falls back to the Vulkan build above.
COPY --from=whisper-sycl /out/whisper-cli /opt/whisper-sycl/whisper-cli
COPY --from=whisper-sycl /out/lib /opt/whisper-sycl/lib
COPY --chmod=755 docker/whisper-cli-sycl /usr/local/bin/whisper-cli-sycl
# Fail the build, not the first subtitle job, if a library either build needs didn't
# make it across — the run-time-loaded backends and adapters included, since a missing
# dependency of theirs shows up only as "no GPU found" or "No device of requested type
# available".
RUN ln -s /opt/whisper/whisper-cli /usr/local/bin/whisper-cli && \
    for f in /opt/whisper/whisper-cli /opt/whisper/*.so; do \
        if ldd "$f" | grep "not found"; then echo "$f is missing shared libraries" >&2; exit 1; fi; \
    done && \
    for f in /opt/whisper-sycl/whisper-cli /opt/whisper-sycl/lib/libur_adapter_*.so.0; do \
        if LD_LIBRARY_PATH=/opt/whisper-sycl/lib ldd "$f" | grep "not found"; then \
            echo "$f is missing shared libraries" >&2; exit 1; fi; \
    done && \
    whisper-cli-sycl --help >/dev/null 2>&1
# No LIBVA_DRIVER_NAME here: Convert gives each ffmpeg the VA driver for the GPU it opens
# (iHD for Intel, radeonsi for AMD, both bundled with jellyfin-ffmpeg; internal/convert/
# libva.go). Setting one on the container still overrides that, for every run.

# arm64: CPU only. The same jellyfin-ffmpeg (Jellyfin publishes it for arm64 too), so
# Convert encodes with the same x265 / SVT-AV1 on the CPU; Convert's start-up check finds
# no Intel/AMD/NVIDIA encoder here and never offers one. libgomp1 is whisper-cli's OpenMP
# runtime. No Vulkan, VAAPI or Level Zero packages: nothing in this image would use them.
FROM ubuntu:24.04 AS runtime-arm64
COPY docker/apprise-requirements.txt /tmp/apprise-requirements.txt
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg gosu python3 python3-pip libgomp1 && \
    mkdir -p /etc/apt/keyrings && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
        | gpg --dearmor -o /etc/apt/keyrings/jellyfin.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/jellyfin.gpg arch=arm64] https://repo.jellyfin.org/ubuntu noble main" \
        > /etc/apt/sources.list.d/jellyfin.list && \
    apt-get update && apt-get install -y --no-install-recommends jellyfin-ffmpeg7 && \
    rm -rf /var/lib/apt/lists/* && \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffmpeg /usr/local/bin/ffmpeg && \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffprobe /usr/local/bin/ffprobe && \
    pip3 install --no-cache-dir --break-system-packages --require-hashes --only-binary :all: \
        -r /tmp/apprise-requirements.txt && rm /tmp/apprise-requirements.txt && \
    apprise --version && ffmpeg -version | head -1 && \
    mkdir -p /data /media/downloads /media/library /transcode
COPY --from=whisper-cpu /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
# Tells the app this whisper has no GPU backend, so Settings says "CPU only on ARM" rather
# than "check that /dev/dri is passed", which no setting here could fix.
ENV ARRMADA_WHISPER_CPU_ONLY=1

# --- Final: the image's own platform, plus what every platform shares ---
# Named so the Unraid all-in-one variant (the aio stages below) can build on it.
FROM runtime-${TARGETARCH} AS arrmada
# Native PostgreSQL backups/recovery use supported clients (never a server).
# Ubuntu's default client is older than PostgreSQL 18; use PGDG's signed repo.
RUN mkdir -p /usr/share/postgresql-common/pgdg && \
    curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
        -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc && \
    echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt noble-pgdg main" \
        > /etc/apt/sources.list.d/pgdg.list && \
    apt-get update && apt-get install -y --no-install-recommends postgresql-client-18 && \
    rm -rf /var/lib/apt/lists/* && \
    /usr/lib/postgresql/18/bin/pg_dump --version && /usr/lib/postgresql/18/bin/pg_restore --version
ENV ARRMADA_POSTGRES_BIN=/usr/lib/postgresql/18/bin
COPY --from=build /out/arrmada /usr/local/bin/arrmada
# Dolby Vision (dovi_tool) + HDR10+ (hdr10plus_tool) metadata extractors/injectors.
COPY --from=hdrtools /usr/local/bin/dovi_tool /usr/local/bin/hdr10plus_tool /usr/local/bin/
# kepubify: the KEPUB download for Kobo (internal/kepub). Every variant has it: aio builds
# FROM this stage.
COPY --from=kepubify /usr/local/bin/kepubify /usr/local/bin/kepubify
COPY docker/entrypoint.sh /entrypoint.sh
# The licence and the third-party notices (TagLib, LGPL-2.1, is linked for music tags).
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/arrmada/
COPY LICENSES/ /usr/share/doc/arrmada/LICENSES/
RUN chmod +x /entrypoint.sh && whisper-cli --help >/dev/null 2>&1 && kepubify --version

# Runs as root only long enough to fix data-dir ownership, then drops to PUID:PGID.
ENV ARRMADA_HOST=0.0.0.0 \
    ARRMADA_PORT=7878 \
    ARRMADA_DATA_DIR=/data \
    ARRMADA_LIBRARY_DIR=/media/library \
    ARRMADA_DOWNLOADS_DIR=/media/downloads \
    PUID=1000 \
    PGID=1000
EXPOSE 7878
VOLUME ["/data", "/media"]

# curl, not wget: the Ubuntu runtime has no wget, so the old check failed forever and
# Docker/Unraid showed the container as unhealthy. The first boot runs migrations, hence
# the minute's grace.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD curl -fsS -o /dev/null "http://127.0.0.1:7878/api/health" || exit 1

ENTRYPOINT ["/entrypoint.sh"]

# What update.sh reads before it calls into an image. org.arrmada.cli says the binary
# understands `arrmada <command>`: an older one ignores its arguments and starts a whole
# second server, so scripts never pass arguments to an image without this label.
LABEL org.arrmada.cli="1"
# This build can stage a restore itself (`arrmada restore`), which update.sh --rollback --with-db uses.
LABEL org.arrmada.restore="1"
# The image honors explicit PostgreSQL backend configuration; older images
# ignore it and must never be selected for a PostgreSQL code-only rollback.
LABEL org.arrmada.postgres="1"
# Declared last so a new commit only changes the image's metadata, never the cached
# layers above.
ARG VERSION=docker
ARG COMMIT=unknown
LABEL org.arrmada.version="${VERSION}" org.arrmada.commit="${COMMIT}"

# ==========================================================================================
# The Unraid all-in-one variant (--target aio; published as x.y.z-aio, x.y-aio, stable-aio)
# ==========================================================================================
#
# An Unraid template is one container, so the template's image carries Arrmada's
# companions inside it: qBittorrent and FlareSolverr, each switched off with a template
# variable, and the optional Prowlarr and SABnzbd (the Compose install's --with-prowlarr
# and --with-sabnzbd), each switched on with one. It is the plain image above plus these
# stages, built FROM it, so it runs the exact same Arrmada. The plain image and the Compose
# install don't change: nothing below is in them, and a build without --target still ends
# on the plain image (the last line).
#
# cmd/arrmada-aio supervises the processes behind tini (docs/unraid.md says how). arm64
# gets all but FlareSolverr: its Chromium is published for x86-64 Linux alone.

# qBittorrent: the static qbittorrent-nox build (no Qt or Boost to install, one file),
# the same 5.2.3 the Compose bundle pins, with libtorrent 2.0. Checked against the SHA-256
# GitHub records for each release asset.
FROM alpine:3.20 AS aio-qbittorrent
ARG QBITTORRENT_RELEASE=release-5.2.3_v2.0.15
ARG QBITTORRENT_SHA256_AMD64=eab445f0e7adfca8e33afcd5724cdfd76a09e610d914bec25e8788efbb4e55e2
ARG QBITTORRENT_SHA256_ARM64=335fb2d8038f4b66a444f5645300a98abb2fcebaccf74baf5b30b48c8106082d
ARG TARGETARCH
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) ARCH=x86_64; SHA=${QBITTORRENT_SHA256_AMD64} ;; \
        arm64) ARCH=aarch64; SHA=${QBITTORRENT_SHA256_ARM64} ;; \
        *) echo "no qBittorrent for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    wget -qO /qbittorrent-nox "https://github.com/userdocs/qbittorrent-nox-static/releases/download/${QBITTORRENT_RELEASE}/${ARCH}-qbittorrent-nox"; \
    echo "${SHA}  /qbittorrent-nox" | sha256sum -c -; \
    chmod 755 /qbittorrent-nox

# Prowlarr (optional, switched on in the template): its self-contained .NET build for
# Linux, the same 2.6.5 the Compose bundle pins, checked against the SHA-256 GitHub records
# for each release asset. Only downloaded and unpacked here, so it runs on the build
# machine's platform. Its updater (Prowlarr.Update, a second copy of the .NET runtime) and
# the web UI's source maps are left out; package_info says updates come with the image,
# as in Prowlarr's other Docker images, so it never tries to update itself.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS aio-prowlarr
ARG PROWLARR_VERSION=2.6.5.5623
ARG PROWLARR_SHA256_AMD64=c0824e9f0e9c79e085882f6143041be5c2cde0519baa59e9477d9e4dcbe1b5e3
ARG PROWLARR_SHA256_ARM64=6552ac286d8a1213f99779daac685cc283d69fa6ad3144d811af0289ee5f5656
ARG TARGETARCH
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) ARCH=x64; SHA=${PROWLARR_SHA256_AMD64} ;; \
        arm64) ARCH=arm64; SHA=${PROWLARR_SHA256_ARM64} ;; \
        *) echo "no Prowlarr for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    wget -qO /tmp/prowlarr.tar.gz "https://github.com/Prowlarr/Prowlarr/releases/download/v${PROWLARR_VERSION}/Prowlarr.master.${PROWLARR_VERSION}.linux-core-${ARCH}.tar.gz"; \
    echo "${SHA}  /tmp/prowlarr.tar.gz" | sha256sum -c -; \
    mkdir -p /opt/prowlarr; \
    tar -xzf /tmp/prowlarr.tar.gz -C /tmp; \
    mv /tmp/Prowlarr /opt/prowlarr/bin; \
    # The .NET tracing hook needs LTTng, which isn't installed; .NET runs without it.
    rm -rf /tmp/prowlarr.tar.gz /opt/prowlarr/bin/Prowlarr.Update /opt/prowlarr/bin/libcoreclrtraceptprovider.so; \
    find /opt/prowlarr/bin/UI -name '*.map' -delete; \
    printf 'UpdateMethod=docker\nBranch=master\nPackageVersion=%s\nPackageAuthor=[Arrmada](https://github.com/tristenlammi/Arrmada)\n' \
        "${PROWLARR_VERSION}" > /opt/prowlarr/package_info; \
    chown -R root:root /opt/prowlarr; \
    chmod -R u=rwX,go=rX /opt/prowlarr; \
    chmod 755 /opt/prowlarr/bin/Prowlarr

# SABnzbd (optional, switched on in the template): its source release, the same 5.1.3 the
# Compose bundle pins, checked by SHA-256, with its Python packages pinned by hash in a
# virtualenv on the runtime's own Python (docker/aio/sabnzbd-requirements.txt). Every
# package is a prebuilt wheel but sgmllib3k, which PyPI has only as source: pure Python,
# built here against the setuptools pinned in that file (installed first, from its own
# lines there), never one pip would fetch unchecked. pip and setuptools, the source's
# tests and its translation sources are left out.
# par2cmdline-turbo (the repair tool SABnzbd prefers, as in the Compose bundle's SABnzbd)
# is its static Linux release, checked by SHA-256. unrar and 7-Zip come from Ubuntu, in
# the final stage.
FROM ubuntu:24.04 AS aio-sabnzbd
ARG SABNZBD_VERSION=5.1.3
ARG SABNZBD_SHA256=12a01e30ce166297a375ffc3a761f98bf7d93260e040391497f643f8a3525fed
ARG PAR2TURBO_VERSION=1.5.0
ARG PAR2TURBO_SHA256_AMD64=5a9f64386813456693c2ea1fb7649436fe7544bbdf97fd73b3483dfcc8aca464
ARG PAR2TURBO_SHA256_ARM64=3afb2f0b319fc4e6353c6d4261994757cbe3189f968a72420b4ba1d63f9c9a70
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl unzip python3 python3-venv && rm -rf /var/lib/apt/lists/*
COPY docker/aio/sabnzbd-requirements.txt /tmp/requirements.txt
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) PAR2SHA=${PAR2TURBO_SHA256_AMD64} ;; \
        arm64) PAR2SHA=${PAR2TURBO_SHA256_ARM64} ;; \
        *) echo "no par2cmdline-turbo for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/sab.tar.gz "https://github.com/sabnzbd/sabnzbd/releases/download/${SABNZBD_VERSION}/SABnzbd-${SABNZBD_VERSION}-src.tar.gz"; \
    echo "${SABNZBD_SHA256}  /tmp/sab.tar.gz" | sha256sum -c -; \
    mkdir -p /opt/sabnzbd/app; \
    tar -xzf /tmp/sab.tar.gz --strip-components=1 --no-same-owner -C /opt/sabnzbd/app; \
    rm -rf /tmp/sab.tar.gz /opt/sabnzbd/app/tests /opt/sabnzbd/app/po; \
    python3 -m venv /opt/sabnzbd/venv; \
    pip=/opt/sabnzbd/venv/bin/pip; \
    sed -n '/^setuptools==/,/[^\\]$/p' /tmp/requirements.txt > /tmp/setuptools.txt; \
    grep -q -- '--hash=sha256:' /tmp/setuptools.txt; \
    $pip install --no-cache-dir --require-hashes --only-binary :all: -r /tmp/setuptools.txt; \
    $pip install --no-cache-dir --require-hashes --only-binary :all: --no-binary sgmllib3k --no-build-isolation \
        -r /tmp/requirements.txt; \
    # Nothing imports setuptools once that's built (zc.lockfile still lists it, from
    # before namespace packages needed no help), so it goes with pip.
    $pip uninstall -y setuptools; \
    $pip uninstall -y pip; \
    /opt/sabnzbd/venv/bin/python -m compileall -q /opt/sabnzbd/app; \
    curl -fsSL -o /tmp/par2.zip "https://github.com/animetosho/par2cmdline-turbo/releases/download/v${PAR2TURBO_VERSION}/par2cmdline-turbo-${PAR2TURBO_VERSION}-linux-${TARGETARCH}.zip"; \
    echo "${PAR2SHA}  /tmp/par2.zip" | sha256sum -c -; \
    unzip -q /tmp/par2.zip par2 -d /usr/local/bin; \
    chmod 755 /usr/local/bin/par2; \
    rm -f /tmp/par2.zip /tmp/requirements.txt /tmp/setuptools.txt; \
    chown -R root:root /opt/sabnzbd

# The supervisor: standard library only, so it builds from its own two folders, on the
# build machine's platform (Go cross-compiles it, like arrmada), without the web UI.
FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine AS aio-supervisor
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/aio ./internal/aio
COPY cmd/arrmada-aio ./cmd/arrmada-aio
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w" -o /out/arrmada-aio ./cmd/arrmada-aio

# FlareSolverr (x86-64): its source at a checked commit, its Python packages pinned by hash
# in a virtualenv on the runtime's own Python, and the Chromium build its own Linux release
# bundles (a chromium-browser-snapshots revision) with the chromedriver of that same
# revision, both checked by SHA-256. It sits at /app, where FlareSolverr's code looks for
# the chromedriver and Chromium, as in its own image. Ubuntu 24.04 ships Chromium only as a
# snap, which can't run in a container, hence the upstream build.
FROM ubuntu:24.04 AS aio-flaresolverr
ARG FLARESOLVERR_VERSION=v3.5.2
ARG FLARESOLVERR_COMMIT=93125d6ea6956906c8dfa29e169b2dc67d993dc3
ARG CHROMIUM_REVISION=1681097
ARG CHROMIUM_SHA256=8ae6cec7a56e2966b0523e2857c381ee35df92f034ffd57708465de7b6e60513
ARG CHROMEDRIVER_SHA256=c1be2d78ab1065040f87bfba22131d738dccb257ef3c2476f0eef705d2521d27
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git unzip python3 python3-venv && rm -rf /var/lib/apt/lists/*
COPY docker/aio/flaresolverr-requirements.txt /tmp/requirements.txt
RUN set -eux; \
    git clone --depth 1 --branch "${FLARESOLVERR_VERSION}" https://github.com/FlareSolverr/FlareSolverr /tmp/fs; \
    test "$(git -C /tmp/fs rev-parse HEAD)" = "${FLARESOLVERR_COMMIT}"; \
    mkdir -p /app; cp -r /tmp/fs/src/. /app/; cp /tmp/fs/package.json /app/package.json; \
    rm -rf /app/build_package.py /app/tests* /tmp/fs; \
    python3 -m venv /opt/flaresolverr/venv; \
    /opt/flaresolverr/venv/bin/pip install --no-cache-dir --require-hashes -r /tmp/requirements.txt; \
    base="https://commondatastorage.googleapis.com/chromium-browser-snapshots/Linux_x64/${CHROMIUM_REVISION}"; \
    curl -fsSL -o /tmp/chrome.zip "${base}/chrome-linux.zip"; \
    curl -fsSL -o /tmp/driver.zip "${base}/chromedriver_linux64.zip"; \
    echo "${CHROMIUM_SHA256}  /tmp/chrome.zip" | sha256sum -c -; \
    echo "${CHROMEDRIVER_SHA256}  /tmp/driver.zip" | sha256sum -c -; \
    unzip -q /tmp/chrome.zip -d /tmp/chrome; mv /tmp/chrome/chrome-linux /app/chrome; \
    unzip -q /tmp/driver.zip -d /tmp/driver; mv /tmp/driver/chromedriver_linux64/chromedriver /app/chromedriver; \
    rm -rf /tmp/chrome* /tmp/driver*; \
    # undetected-chromedriver edits the driver the first time it runs it, which a file
    # owned by root (and shared by every start) can't take. The same edit, made here.
    /opt/flaresolverr/venv/bin/python -I -c 'import re,sys; p="/app/chromedriver"; b=open(p,"rb").read(); m=re.search(rb"\{window\.cdc.*?;\}", b); m or sys.exit("chromedriver: no cdc block to patch"); open(p,"wb").write(b.replace(m[0], b"{console.log(\"undetected chromedriver 1337!\")}".ljust(len(m[0]), b" ")))'; \
    grep -q "undetected chromedriver" /app/chromedriver; \
    chmod 755 /app/chromedriver /app/chrome/chrome

# x86-64: FlareSolverr and what its Chromium needs (the libraries Google's own Chrome
# package depends on, fonts, and Xvfb for the hidden display it runs on).
#
# The plain image's packages (the GPU, VAAPI and Level Zero libraries above all) stay at
# exactly the versions the plain image has, so Convert and Whisper behave the same in both
# images: every installed package is held while the aio's are added (--no-upgrade alone
# covers only the packages named), so one that would need a newer library fails the
# build instead of upgrading it quietly.
#
# FlareSolverr runs as a user of its own (flaresolverr, 8191:8191, internal/aio's
# FlareSolverrUID), never PUID: its Chromium opens tracker pages without a sandbox, and
# that user owns nothing of the owner's.
FROM arrmada AS aio-amd64
RUN groupadd --system --gid 8191 flaresolverr && \
    useradd --system --uid 8191 --gid 8191 --no-create-home --home-dir /tmp/flaresolverr \
        --shell /usr/sbin/nologin flaresolverr
RUN dpkg-query -W -f='${binary:Package}\n' | xargs apt-mark hold >/dev/null && \
    apt-get update && apt-get install -y --no-install-recommends --no-upgrade \
        libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 \
        libcups2t64 libdbus-1-3 libdrm2 libexpat1 libgbm1 libglib2.0-0t64 libgtk-3-0t64 \
        libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 \
        libxext6 libxfixes3 libxkbcommon0 libxrandr2 libxshmfence1 fonts-liberation \
        xvfb && \
    apt-mark showhold | xargs -r apt-mark unhold >/dev/null && \
    rm -rf /var/lib/apt/lists/*
COPY --from=aio-flaresolverr /opt/flaresolverr /opt/flaresolverr
COPY --from=aio-flaresolverr /app /app
# Fail the build, not the first Cloudflare challenge, over a missing library.
RUN if ldd /app/chrome/chrome /app/chromedriver | grep "not found"; then \
        echo "Chromium is missing shared libraries" >&2; exit 1; fi && \
    /app/chrome/chrome --version && /app/chromedriver --version && \
    cd /app && /opt/flaresolverr/venv/bin/python -c 'import flaresolverr_service, utils, undetected_chromedriver'

# arm64: no FlareSolverr (see above); the supervisor says so and leaves it off.
FROM arrmada AS aio-arm64

FROM aio-${TARGETARCH} AS aio
# tini is PID 1: it passes the stop signal to the supervisor and reaps the processes
# Chromium leaves behind. unrar and 7-Zip (7za) are what SABnzbd unpacks with. unrar is
# non-free but freely redistributable (its licence: /usr/share/doc/unrar/copyright;
# THIRD_PARTY_NOTICES.md), from Ubuntu's multiverse component, which ubuntu:24.04's own
# apt sources already list: nothing is added to them here. libicu74 is .NET's
# globalisation library for Prowlarr; the plain image has it already on amd64, and it's
# named so a change there can't take it away quietly. Held as for FlareSolverr above, so
# none of the plain image's packages moves.
RUN dpkg-query -W -f='${binary:Package}\n' | xargs apt-mark hold >/dev/null && \
    apt-get update && apt-get install -y --no-install-recommends --no-upgrade \
        tini unrar 7zip libicu74 && \
    apt-mark showhold | xargs -r apt-mark unhold >/dev/null && \
    rm -rf /var/lib/apt/lists/*
COPY --from=aio-qbittorrent /qbittorrent-nox /usr/local/bin/qbittorrent-nox
COPY --from=aio-prowlarr /opt/prowlarr /opt/prowlarr
COPY --from=aio-sabnzbd /opt/sabnzbd /opt/sabnzbd
COPY --from=aio-sabnzbd /usr/local/bin/par2 /usr/local/bin/par2
COPY --from=aio-supervisor /out/arrmada-aio /usr/local/bin/arrmada-aio
# Fail the build, not the first download, over a missing library or package: qBittorrent
# and SABnzbd say their versions (SABnzbd only after loading its code and packages, the
# compiled ones included), SABnzbd's tools answer, and Prowlarr and its .NET libraries
# find every library they link (and the ICU .NET loads by itself).
RUN qbittorrent-nox --version && \
    /opt/sabnzbd/venv/bin/python -c 'import sabctools, Cheetah.Template, cryptography, cherrypy, feedparser, configobj, guessit, orjson, zc.lockfile; print("sabctools", sabctools.__version__)' && \
    cd /opt/sabnzbd/app && /opt/sabnzbd/venv/bin/python SABnzbd.py --version && cd / && \
    par2 -V | grep -q par2cmdline-turbo && \
    unrar | grep -q UNRAR && \
    7za | grep -q 7-Zip && \
    for f in /opt/prowlarr/bin/Prowlarr /opt/prowlarr/bin/*.so; do \
        if ldd "$f" | grep "not found"; then echo "$f is missing shared libraries" >&2; exit 1; fi; \
    done && \
    ldconfig -p | grep -q 'libicuuc.so.74 '
# qBittorrent and FlareSolverr run unless the template switches them off; Prowlarr and
# SABnzbd only when it switches them on.
ENV ARRMADA_BUNDLE_QBITTORRENT=true \
    ARRMADA_BUNDLE_FLARESOLVERR=true \
    ARRMADA_BUNDLE_PROWLARR=false \
    ARRMADA_BUNDLE_SABNZBD=false
# 8080: qBittorrent's web UI; 9696: Prowlarr's; 8085: SABnzbd's. The incoming torrent port
# is the template's ARRMADA_QBIT_PORT.
EXPOSE 8080 9696 8085
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/arrmada-aio"]
LABEL org.arrmada.variant="aio"

# The default target (a build without --target) is the plain image, unchanged.
FROM arrmada AS plain
