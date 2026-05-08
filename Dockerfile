# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:latest AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /auth-vpn ./cmd

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        iproute2 iptables ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /auth-vpn /usr/local/bin/auth-vpn
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
EXPOSE 7777 9100 2222
ENTRYPOINT ["docker-entrypoint.sh"]
