FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /auth-vpn ./cmd

FROM alpine:3.19

COPY --from=builder /auth-vpn /usr/local/bin/auth-vpn
COPY docker-entrypoint.sh /docker-entrypoint.sh

RUN chmod +x /usr/local/bin/auth-vpn /docker-entrypoint.sh

ENTRYPOINT ["/docker-entrypoint.sh"]
