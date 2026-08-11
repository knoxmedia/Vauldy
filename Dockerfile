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

# Runtime image
FROM debian:bookworm-slim
ARG VERSION=dev
LABEL org.opencontainers.image.title="Vauldy" \
      org.opencontainers.image.description="Private, permanent, secure family digital safe" \
      org.opencontainers.image.source="https://github.com/knoxmedia/Vauldy" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION"
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=gobuild /knox-media /app/knox-media
COPY docker/config.yml /app/config.yml
ENV KNOX_MEDIA_CONFIG=/app/config.yml
EXPOSE 8200
VOLUME ["/app/data"]
CMD ["/app/knox-media"]
