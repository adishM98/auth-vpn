# tj-vpn — Design & Architecture Plan

> A lightweight, self-hosted VPN tunnel binary built for developer and CI/CD workflows.
> Install on any VM, connect from anywhere, every application just works.

---

## Problem Statement

Teams working with private infrastructure (databases, internal services) face a recurring challenge:

- Opening ports publicly is insecure
- Tailscale/WireGuard require per-device setup and accounts
- SSH tunnels require per-app configuration
- GitHub Actions CI runners have no clean way to reach private services
- Managing VPN clients across VMs, laptops, and CI is operationally heavy

**tj-vpn solves this with a single binary, one port, token-based auth, and OS-level traffic routing.**

---

## Goals

- Single binary (`tj-vpn`) for both server and client
- One port open on the VM — handles auth + tunnel traffic
- Token-based enrollment — no key files, no accounts
- OS-level TUN interface — every application routes through tunnel automatically
- Works in GitHub Actions — connect in 2 lines, no special config
- Self-hosted — no third-party service dependency
- Cross-platform — Linux (server + client), macOS (client)

---

## Non-Goals (v1)

- Windows client (Phase 2)
- Mobile clients
- Full mesh networking (peer-to-peer)
- Web UI for device management (Phase 2)
- BGP/subnet routing

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    tj-vpn server                        │
│             (runs on private VM, always on)             │
│                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │  TLS Server  │  │ Auth Handler │  │ TUN Interface│   │
│  │  :7777       │  │ token check  │  │  10.0.0.1    │   │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘   │
│         └─────────────────┴─────────────────┘           │
│                      Single Port                        │
└─────────────────────────────┬───────────────────────────┘
                               │ TLS encrypted TCP :7777
          ┌────────────────────┼───────────────────┐
          │                    │                   │
┌─────────▼──────┐  ┌──────────▼──────┐  ┌─────────▼──────┐
│  Dev Laptop    │  │   QA Laptop     │  │ GitHub Actions │
│  tj-vpn client │  │  tj-vpn client  │  │ tj-vpn client  │
│  TUN: 10.0.0.2 │  │  TUN: 10.0.0.3  │  │ TUN: 10.0.0.4  │
│                │  │                 │  │                │
│  psql ✅       │  │  TablePlus ✅   │  │  Cypress ✅     │
│  TablePlus ✅  │  │  ToolJet ✅     │  │  pg client ✅   │
│  Any app ✅    │  │  Any app ✅     │  │  Any app ✅     │
└────────────────┘  └─────────────────┘  └────────────────┘
```

---

## Connection Flow

```
1. CLIENT                          SERVER
   │                                │
   │──── TCP connect :7777 ────────▶│
   │◀─── TLS handshake ────────────▶│  (encryption established)
   │                                │
   │──── AUTH { token: "abc123" }──▶│
   │◀─── AUTH_OK {                  │  (token validated)
   │       client_ip: "10.0.0.2",   │
   │       server_ip: "10.0.0.1",   │
   │       subnet: "10.0.0.0/24"    │
   │     } ─────────────────────────│
   │                                │
   │  [client creates TUN: 10.0.0.2]│  [server creates peer entry]
   │  [OS route: 10.0.0.0/24 → tun] │
   │                                │
   │══════ TUNNEL FRAMES ══════════▶│  (data flows both ways)
   │◀═════ TUNNEL FRAMES ═══════════│
   │                                │

2. ANY APP on client machine:
   app → connects to 10.0.0.1:5432
       → OS routes to TUN interface
       → tj-vpn reads packet from TUN
       → wraps in frame, sends over TLS TCP
       → server unwraps frame
       → writes to its TUN interface
       → OS delivers to PostgreSQL
```

---

## Packet Framing Protocol

TCP is a stream — packets need framing:

```
┌─────────────────────────────────────────┐
│  4 bytes     │  N bytes                 │
│  length      │  IP packet data          │
└─────────────────────────────────────────┘
```

Control messages use the same framing with a reserved packet type:

```
┌──────────┬────────────┬─────────────────┐
│ 4 bytes  │  1 byte    │  N bytes        │
│ length   │  type      │  payload        │
└──────────┴────────────┴─────────────────┘

Types:
  0x01 — AUTH request
  0x02 — AUTH_OK response
  0x03 — AUTH_FAIL response
  0x04 — IP packet (tunnel data)
  0x05 — PING (keepalive)
  0x06 — PONG
  0x07 — DISCONNECT
```

---

## Security Model

```
Layer 1: TLS 1.3
  - All traffic encrypted in transit
  - Server presents TLS certificate (self-signed on install)
  - Client verifies server certificate fingerprint

Layer 2: Token Authentication
  - Client presents token in AUTH message
  - Token is SHA-256 hashed on disk (never stored plaintext)
  - Failed auth closes connection immediately
  - Brute force: 5 failed attempts = 60s ban per IP

Layer 3: Per-client isolation
  - Each client gets unique IP in 10.0.0.0/24
  - Client can only communicate with server (10.0.0.1)
  - No client-to-client traffic (not a mesh VPN)
```

**Token types:**

| Type | Use case | Expiry |
|---|---|---|
| Permanent | Dev/QA laptops | Never |
| Expiring | CI/CD runners | Hours/days |
| One-time | Temporary access | Single use |

---

## CLI Interface

### Server Commands

```bash
# Install and start server (run once on VM)
sudo tj-vpn server install
# Output:
#   ✓ Detecting public IP... 20.98.154.174
#   ✓ Generating token...    abc123xyz
#   ✓ TLS certificate generated
#   ✓ Listening on :7777
#   ✓ Systemd service enabled
#
#   ─────────────────────────────────────
#   Connect with:
#     tj-vpn connect 20.98.154.174:7777 --token abc123xyz
#   ─────────────────────────────────────

# Start / stop / restart
sudo tj-vpn server start
sudo tj-vpn server stop
sudo tj-vpn server restart
sudo tj-vpn server status

# Token management
tj-vpn server tokens list
tj-vpn server tokens add --name "dev-john"
tj-vpn server tokens add --name "ci-runner" --expires 24h
tj-vpn server tokens add --name "qa-temp"   --expires 7d --one-time
tj-vpn server tokens revoke --name "dev-john"

# Show connected clients
tj-vpn server clients

# Server config
tj-vpn server config show
tj-vpn server config set port 8888
```

### Client Commands

```bash
# Connect (interactive, foreground)
tj-vpn connect 20.98.154.174:7777 --token abc123xyz

# Connect (background, for CI/scripts)
tj-vpn connect 20.98.154.174:7777 --token abc123xyz --background

# Connect and wait until tunnel is verified
tj-vpn connect 20.98.154.174:7777 --token abc123xyz --background --wait

# Disconnect
tj-vpn disconnect

# Status
tj-vpn status

# Save connection profile (so you don't repeat flags)
tj-vpn profile save staging --host 20.98.154.174:7777 --token abc123xyz
tj-vpn connect staging   # uses saved profile
```

---

## Configuration Files

### Server — `/etc/tj-vpn/server.yml`

```yaml
port: 7777
tls:
  cert: /etc/tj-vpn/tls/cert.pem
  key:  /etc/tj-vpn/tls/key.pem

tunnel:
  subnet:  10.0.0.0/24
  server_ip: 10.0.0.1

tokens:
  - name: dev-adish
    hash: sha256:abc...
    created_at: 2026-04-17
    expires: never

  - name: ci-runner
    hash: sha256:xyz...
    created_at: 2026-04-17
    expires: 2026-04-18T00:00:00Z

security:
  max_auth_failures: 5
  ban_duration: 60s
```

### Client — `~/.tj-vpn/config.yml`

```yaml
profiles:
  staging:
    host:  20.98.154.174:7777
    token: abc123xyz
    tls_fingerprint: aa:bb:cc:...  # pinned on first connect

  production:
    host:  10.1.2.3:7777
    token: xyz789abc
```

---

## GitHub Actions Integration

### Simple (inline)

```yaml
jobs:
  cypress:
    runs-on: ubuntu-22.04
    steps:
      - name: Connect to private network
        run: |
          curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/tj-vpn-linux-amd64 \
            -o tj-vpn && chmod +x tj-vpn
          sudo ./tj-vpn connect ${{ secrets.TJ_VPN_HOST }} \
            --token ${{ secrets.TJ_VPN_TOKEN }} \
            --background --wait

      - name: Run Cypress tests
        run: cypress run
        env:
          PG_HOST: 10.0.0.1     # VM is now reachable
          PG_PORT: 5432
```

### Reusable Action (Phase 2)

```yaml
      - uses: tooljet/tj-vpn-action@v1
        with:
          host:  ${{ secrets.TJ_VPN_HOST }}
          token: ${{ secrets.TJ_VPN_TOKEN }}
```

### Required GitHub Secrets

```
TJ_VPN_HOST   = 20.98.154.174:7777
TJ_VPN_TOKEN  = abc123xyz          (use expiring token for CI)
```

---

## Project Structure

```
tj-vpn/
├── cmd/
│   └── main.go                  — CLI entry point (cobra)
│
├── internal/
│   ├── server/
│   │   ├── server.go            — TLS listener, client manager
│   │   ├── install.go           — detect IP, gen token, systemd setup
│   │   ├── tokens.go            — token CRUD, hashing
│   │   └── clients.go           — connected client registry
│   │
│   ├── client/
│   │   ├── client.go            — connect, auth, tunnel lifecycle
│   │   └── profile.go           — saved connection profiles
│   │
│   ├── tunnel/
│   │   ├── tun_linux.go         — TUN interface (Linux)
│   │   ├── tun_darwin.go        — TUN interface (macOS utun)
│   │   ├── frame.go             — packet framing over TCP
│   │   └── router.go            — OS route injection/cleanup
│   │
│   └── auth/
│       ├── token.go             — generate, hash, validate tokens
│       └── ratelimit.go         — brute force protection
│
├── pkg/
│   └── protocol/
│       └── messages.go          — AUTH, AUTH_OK, FRAME types
│
├── scripts/
│   └── install.sh               — one-liner install script
│
├── .github/
│   └── workflows/
│       └── release.yml          — build + release binaries
│
├── Makefile
├── go.mod
└── go.sum
```

---

## Tech Stack

| Component | Choice | Reason |
|---|---|---|
| Language | Go | Single binary, cross-platform, great networking |
| TUN interface | `github.com/songgao/water` | Abstracts Linux/macOS TUN |
| TLS | `crypto/tls` (stdlib) | TLS 1.3, no dependencies |
| CLI | `github.com/spf13/cobra` | Standard Go CLI |
| Config | `github.com/spf13/viper` | YAML + env var support |
| Framing | Custom (stdlib `encoding/binary`) | Simple, no overhead |
| Service | systemd (Linux) / launchd (macOS) | Native OS service |

---

## Build & Release

```makefile
# Makefile

build-linux:
	GOOS=linux GOARCH=amd64 go build -o dist/tj-vpn-linux-amd64 ./cmd

build-mac-intel:
	GOOS=darwin GOARCH=amd64 go build -o dist/tj-vpn-darwin-amd64 ./cmd

build-mac-arm:
	GOOS=darwin GOARCH=arm64 go build -o dist/tj-vpn-darwin-arm64 ./cmd

build-all: build-linux build-mac-intel build-mac-arm

release: build-all
	gh release create v$(VERSION) dist/*
```

GitHub Actions auto-builds and publishes binaries on each tag push.

---

## Installation UX

### On VM (server)

```bash
curl -fsSL https://get.tj-vpn.dev/install.sh | sudo bash server
# Downloads binary, runs: sudo tj-vpn server install
# Prints connection string at the end
```

### On Laptop / VM (client)

```bash
curl -fsSL https://get.tj-vpn.dev/install.sh | bash
tj-vpn connect 20.98.154.174:7777 --token abc123xyz
```

---

## Build Phases

### Phase 1 — Core Tunnel (MVP)
- [ ] TUN interface (Linux + macOS)
- [ ] Packet framing over TCP
- [ ] TLS server + client
- [ ] Token auth (generate, hash, validate)
- [ ] OS route injection on connect / cleanup on disconnect
- [ ] `server install` command (detect IP, gen token, systemd)
- [ ] `client connect` command (foreground + background + wait)
- [ ] Makefile with cross-platform builds

### Phase 2 — Production Ready
- [ ] Expiring + one-time tokens
- [ ] Brute force protection (rate limiting)
- [ ] Client keepalive / auto-reconnect
- [ ] `tj-vpn server clients` — show live connections
- [ ] Connection profiles (`tj-vpn profile save`)

### Phase 3 — Polish
- [ ] Web UI for device management
- [ ] Windows client
- [ ] ACLs (device A can reach port X, device B cannot)
- [ ] Metrics / connection logs
- [ ] ToolJet datasource native integration

---

## Use Cases Covered

| Scenario | Solution | Port 5432 public? |
|---|---|---|
| Dev laptop → PostgreSQL | tj-vpn connect | No |
| QA laptop → PostgreSQL | tj-vpn connect | No |
| ToolJet VM → PostgreSQL | tj-vpn connect | No |
| GitHub Actions → PostgreSQL | tj-vpn connect --background | No |
| Any VM → PostgreSQL | tj-vpn connect | No |
| Any app on any machine | TUN routes automatically | No |

---

## Summary

tj-vpn is a single Go binary that:

1. **Server**: Runs on any Linux VM. One port. Auto-detects public IP. Generates token. Prints connection string.
2. **Client**: Connects with token. Creates TUN interface. Injects OS route. Every app on the machine reaches the server's network automatically.
3. **CI/CD**: Two lines in any GitHub Actions workflow. Token stored as secret. Ephemeral connection, dies with the job.

No accounts. No third-party service. No per-app configuration. Just install, connect, done.
