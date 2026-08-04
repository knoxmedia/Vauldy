# Build frontend
FROM node:20-bookworm AS webbuild
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY static/powerplayer6 /static/powerplayer6
RUN npm run build

# Build backend with embedded web/dist
FROM golang:1.22-bookworm AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuild /web/dist /src/internal/webembed/dist
RUN CGO_ENABLED=0 GOOS=linux go build -tags embedweb -ldflags="-s -w" -o /knox-media ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=gobuild /knox-media /app/knox-media
COPY config.yml /app/config.yml
ENV KNOX_MEDIA_CONFIG=/app/config.yml
EXPOSE 8200
VOLUME ["/app/data"]
CMD ["/app/knox-media"]
