# syntax=docker/dockerfile:1

# Build frontend (platform-independent artifacts; built once on the native arch)
FROM --platform=$BUILDPLATFORM node:20-bookworm AS webbuild
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY static/powerplayer6 /static/powerplayer6
RUN npm run build

# Build backend (cross-compiled on the native arch for each target platform)
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS gobuild
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG DIRTY=unknown
ARG SOURCE_DATE_EPOCH
ARG ALLOW_DIRTY=false
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuild /web/dist /src/internal/webembed/dist
RUN set -eu; \
    test -d .git || { echo "release Docker builds require a full-clone .git directory in the build context" >&2; exit 1; }; \
    actual_commit="$(git rev-parse HEAD)"; \
    if [ "$actual_commit" != "$COMMIT" ]; then echo "COMMIT does not match source HEAD" >&2; exit 1; fi; \
    source_status="$(git status --porcelain --untracked-files=all)"; \
    if [ "$ALLOW_DIRTY" = "false" ] && [ -n "$source_status" ]; then echo "refusing dirty release Docker build" >&2; exit 1; fi; \
    actual_dirty=false; if [ -n "$source_status" ]; then actual_dirty=true; fi; \
    if [ "$DIRTY" != "$actual_dirty" ]; then echo "DIRTY does not match actual source state" >&2; exit 1; fi; \
    build_time="$BUILD_TIME"; \
    if [ -n "$SOURCE_DATE_EPOCH" ]; then build_time="$(date -u -d "@$SOURCE_DATE_EPOCH" '+%Y-%m-%dT%H:%M:%SZ')"; fi; \
    ldflags="-s -w -X knox-media/internal/buildinfo.Version=$VERSION -X knox-media/internal/buildinfo.Commit=$COMMIT -X knox-media/internal/buildinfo.BuildTime=$build_time -X knox-media/internal/buildinfo.Dirty=$DIRTY"; \
    CGO_ENABLED=0 go build -ldflags="$ldflags" -o /buildinfo-check ./cmd/buildinfo-check; \
    if [ "$ALLOW_DIRTY" = "true" ]; then /buildinfo-check --allow-dirty; else /buildinfo-check; fi; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -tags embedweb -ldflags="$ldflags" -o /knox-media ./cmd/server; \
    rm -f /buildinfo-check

# Extract the special HW-accelerated ffmpeg from the release bundle.
# The bundle only exists for linux/amd64; on arm64 this stage stays empty
# and the runtime image falls back to Debian's ffmpeg (software transcode).
FROM debian:bookworm-slim AS ffmpeg-special
ARG TARGETARCH
ARG FFMPEG_PACKAGE_URL
RUN set -eu; \
    mkdir -p /ffmpeg-bin; \
    if [ "$TARGETARCH" = "amd64" ] && [ -n "$FFMPEG_PACKAGE_URL" ]; then \
        apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
            && rm -rf /var/lib/apt/lists/*; \
        curl -fsSL "$FFMPEG_PACKAGE_URL" -o /tmp/ffpkg.tar.gz; \
        tar -xzf /tmp/ffpkg.tar.gz -C /tmp --wildcards '*/tools/ffmpeg/bin/*'; \
        cp -a /tmp/vauldy/tools/ffmpeg/bin/* /ffmpeg-bin/; \
        rm -rf /tmp/ffpkg.tar.gz /tmp/vauldy; \
    fi

# Runtime image
FROM debian:bookworm-slim
ARG TARGETARCH
ARG VERSION=dev
ARG FFMPEG_PACKAGE_URL
LABEL org.opencontainers.image.title="Vauldy" \
      org.opencontainers.image.description="Private, permanent, secure family digital safe" \
      org.opencontainers.image.source="https://github.com/knoxmedia/Vauldy" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION"
RUN set -eu; \
    if [ "$TARGETARCH" = "amd64" ] && [ -n "$FFMPEG_PACKAGE_URL" ]; then \
        # special ffmpeg needs libfdk-aac (non-free) plus a legacy libvpx.so.3
        # (Debian bookworm only ships libvpx.so.7), fetched from the Ubuntu archive.
        echo "deb http://deb.debian.org/debian bookworm main non-free contrib" \
            > /etc/apt/sources.list.d/bookworm-nonfree.list; \
    fi; \
    apt-get update; \
    if [ "$TARGETARCH" = "amd64" ] && [ -n "$FFMPEG_PACKAGE_URL" ]; then \
        apt-get install -y --no-install-recommends \
            ca-certificates curl libstdc++6 libnuma1 libopus0 libmp3lame0 \
            libx11-6 libfdk-aac2; \
        curl -fsSL "https://archive.ubuntu.com/ubuntu/pool/main/libv/libvpx/libvpx3_1.5.0-2ubuntu1.1_amd64.deb" \
            -o /tmp/libvpx3.deb; \
        dpkg-deb -x /tmp/libvpx3.deb /tmp/libvpx3; \
        cp -a /tmp/libvpx3/usr/lib/x86_64-linux-gnu/libvpx.so.3.0.0 /usr/lib/x86_64-linux-gnu/; \
        ln -sf libvpx.so.3.0.0 /usr/lib/x86_64-linux-gnu/libvpx.so.3; \
        rm -rf /tmp/libvpx3 /tmp/libvpx3.deb; \
    else \
        apt-get install -y --no-install-recommends ca-certificates ffmpeg; \
    fi; \
    rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=gobuild /knox-media /app/knox-media
# On amd64 this is the special ffmpeg bundle; on arm64 it stays empty and
# /usr/bin/ffmpeg (installed above) is used instead.
COPY --from=ffmpeg-special /ffmpeg-bin /app/tools/ffmpeg/bin
COPY docker/config.yml /app/config.yml
# Point the config at the special ffmpeg on amd64; keep /usr/bin paths on arm64.
RUN if [ "$TARGETARCH" = "amd64" ] && [ -n "$FFMPEG_PACKAGE_URL" ]; then \
        sed -i -e 's#ffprobe_path: "/usr/bin/ffprobe"#ffprobe_path: "/app/tools/ffmpeg/bin/ffprobe"#' \
               -e 's#ffmpeg_path: "/usr/bin/ffmpeg"#ffmpeg_path: "/app/tools/ffmpeg/bin/ffmpeg"#' \
            /app/config.yml; \
    fi
ENV KNOX_MEDIA_CONFIG=/app/config.yml
# The special ffmpeg loads bundled libdrm/libnppi/libva/libXfixes from its dir.
ENV LD_LIBRARY_PATH=/app/tools/ffmpeg/bin
ENV PATH=/app/tools/ffmpeg/bin:$PATH
EXPOSE 8200
VOLUME ["/app/data"]
# Self-check: the special ffmpeg must actually start inside the container
# (validates the libvpx.so.3 / libfdk-aac compatibility on this base image).
RUN if [ "$TARGETARCH" = "amd64" ] && [ -n "$FFMPEG_PACKAGE_URL" ]; then \
        /app/tools/ffmpeg/bin/ffmpeg -version >/dev/null && echo "special ffmpeg OK"; \
        /app/tools/ffmpeg/bin/ffmpeg -hide_banner -encoders 2>/dev/null | grep -q nvenc && echo "NVENC encoder available" || echo "NVENC not compiled-in"; \
    else \
        /usr/bin/ffmpeg -version >/dev/null && echo "debian ffmpeg OK"; \
    fi
CMD ["/app/knox-media"]
