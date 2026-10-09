# ── Build stage ───────────────────────────────────────────────────────────────
# Build from the repo root:  docker build -f docker/Dockerfile .
FROM --platform=$BUILDPLATFORM golang:1.25 AS builder
ARG TARGETARCH=amd64
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -ldflags="-s -w -X main.Version=${VERSION}" -o /auth-vpn ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        iproute2 iptables ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /auth-vpn /usr/local/bin/auth-vpn
COPY docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
# AUTH_VPN_PORT controls the tunnel port (default 7777).
# Override at runtime: docker run -e AUTH_VPN_PORT=8888 ...
# or in k8s via env: in the deployment manifest.
ENV AUTH_VPN_PORT=7777
EXPOSE 7777 9100 2222
ENTRYPOINT ["docker-entrypoint.sh"]
