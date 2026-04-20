# auth-vpn

A lightweight, self-hosted VPN tunnel for developers and teams. One binary, one open port — every application on the machine routes through an encrypted TLS tunnel automatically.

Built for the common case: a team that needs secure access to services running in Docker containers on a cloud VM, without exposing ports to the internet.

---

## How it works

```
VM (Docker containers)   ←   auth-vpn server   (port 7777, TLS)
          │
          │  encrypted TLS 1.3 tunnel
          │
          ├── Dev laptop        (auth-vpn client)
          ├── QA laptop         (auth-vpn client)
          └── Test VM           (auth-vpn client)
```

The server creates a TUN interface at `10.0.0.1` and assigns each connecting client an IP from `10.0.0.2–254`. All IP traffic to the `10.0.0.0/24` subnet is routed through the tunnel at the OS level — no per-app configuration needed.

Every container on the VM is reachable the same way:

```
PostgreSQL  →  10.0.0.1:5432
MySQL       →  10.0.0.1:3306
MongoDB     →  10.0.0.1:27017
Redis       →  10.0.0.1:6379
Any service →  10.0.0.1:<port>
```

---

## Quick start

### 1 — Server (the VM with your Docker containers)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server
```

No Go, no Git, nothing to install first. The script detects your platform, downloads the right binary, generates a self-signed TLS cert, creates an initial access token, and registers a systemd service.

At the end you'll see:

```
  ─────────────────────────────────────────────
  Connect with:
    auth-vpn connect 20.98.154.174:7777 --token abc123xyz
  ─────────────────────────────────────────────
```

**Save that token.** Share it with anyone who needs access.

### 2 — Client (dev/QA laptop or another VM)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | bash
```

Then connect:

```bash
auth-vpn connect 20.98.154.174:7777 --token abc123xyz
```

Save a profile so you never type the token again:

```bash
auth-vpn profile save staging \
  --host 20.98.154.174:7777 \
  --token abc123xyz

auth-vpn connect staging
```

---

## Security model

- **TLS 1.3** — all traffic is encrypted in transit
- **Token auth** — SHA-256 hashed tokens stored on server, raw token never persisted
- **Rate limiting** — 5 failed auth attempts = 60 second IP ban
- **Token controls** — permanent, expiring, or one-time tokens; revoke any token instantly without restarting the server
- **Single port** — close all container ports from the internet, only 7777 TCP needs to be open

---

## CLI reference

### Server

```bash
# Install + configure (run once per VM)
sudo auth-vpn server install --port 7777

# Start the server (systemd handles this automatically after install)
sudo auth-vpn server start --port 7777

# Token management
auth-vpn server tokens list
auth-vpn server tokens add --name "dev-alice"
auth-vpn server tokens add --name "ci-runner" --expires 24h
auth-vpn server tokens add --name "temp"       --one-time
auth-vpn server tokens revoke --name "dev-alice"
```

### Client

```bash
# Connect with a token
auth-vpn connect 20.98.154.174:7777 --token <token>

# Connect using a saved profile
auth-vpn connect staging

# Connect in background (for VMs, CI)
auth-vpn connect staging --background

# Connect in CI — block until tunnel is verified before returning
auth-vpn connect staging --background --wait

# Disconnect
auth-vpn disconnect

# Status
auth-vpn status
```

### Profiles

```bash
auth-vpn profile save staging --host 20.98.154.174:7777 --token <token>
auth-vpn profile list
```

---

## Verify your connection

```bash
# Tunnel status
auth-vpn status

# Ping the VM through the tunnel
ping 10.0.0.1

# Reach any container
psql      -h 10.0.0.1 -p 5432
mysql     -h 10.0.0.1 -P 3306 -u root -p
mongosh      10.0.0.1:27017
redis-cli -h 10.0.0.1 -p 6379
curl         http://10.0.0.1:8080/health
```

---

## Installation options

### Single curl command (recommended — no dependencies needed)

```bash
# Server
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server

# Server on a custom port
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server --port=8888

# Client
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | bash

# Pin to a specific version
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | bash -s -- --version=v1.2.3
```

### Deploy from your Mac (VM needs nothing)

```bash
# Clone the repo on your Mac
git clone https://github.com/adishM98/auth-vpn
cd auth-vpn

# Build + push + install as server on remote VM
make deploy VM=azureuser@<vm-ip>

# Build + push + install as client on another VM
make deploy-client VM=azureuser@<vm-ip>

# Custom port or SSH key
make deploy VM=azureuser@<vm-ip> PORT=8888 SSH_KEY=~/.ssh/id_ed25519
```

### From source

```bash
git clone https://github.com/adishM98/auth-vpn
cd auth-vpn
./install.sh              # client
sudo ./install.sh --server   # server
```

---

## Docker compose — make containers reachable through the tunnel

Containers must bind to `0.0.0.0`, not `127.0.0.1`:

```yaml
# docker-compose.yml
services:
  postgres:
    image: postgres:16
    ports:
      - "5432:5432"       # correct — accessible through tunnel
      # not: "127.0.0.1:5432:5432"  — tunnel can't reach this
```

---

## Use in CI / CD

```yaml
# GitHub Actions example
- name: Install auth-vpn
  run: |
    curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | bash

- name: Connect to staging
  run: |
    auth-vpn profile save staging --host ${{ secrets.VPN_HOST }} --token ${{ secrets.VPN_TOKEN }}
    auth-vpn connect staging --background --wait

- name: Run tests against staging DB
  run: psql -h 10.0.0.1 -p 5432 -U postgres -c '\l'
```

---

## Building from source

Requires Go 1.22+.

```bash
git clone https://github.com/adishM98/auth-vpn
cd auth-vpn

# Build for your current machine
go build -o auth-vpn ./cmd

# Cross-compile
make build-linux        # Linux amd64
make build-mac-arm      # macOS Apple Silicon
make build-mac-intel    # macOS Intel
make build-all          # all three
```

---

## Architecture

```
┌─────────────────────────────────────────────┐
│  VM                                         │
│                                             │
│  ┌──────────────┐    ┌───────────────────┐  │
│  │  auth-vpn    │    │  Docker containers│  │
│  │  server      │    │                   │  │
│  │  :7777 (TLS) │    │  postgres :5432   │  │
│  │              │    │  mysql    :3306   │  │
│  │  TUN: tun0   │    │  redis    :6379   │  │
│  │  10.0.0.1/24 │    │  ...              │  │
│  └──────────────┘    └───────────────────┘  │
│         │                                   │
└─────────┼───────────────────────────────────┘
          │  TLS 1.3 / port 7777
          │
   ┌──────┴──────────────────────────────┐
   │                                     │
   │  Client machine                     │
   │                                     │
   │  TUN: utun3 (macOS) / tun0 (Linux)  │
   │  IP: 10.0.0.2                       │
   │                                     │
   │  Route: 10.0.0.0/24 → TUN          │
   │  (all apps reach 10.0.0.1:* for free│
   │   — no per-app config)              │
   └─────────────────────────────────────┘
```

**Packet flow:**

```
App on client writes to 10.0.0.1:5432
  → OS routes packet to TUN interface
    → auth-vpn client reads from TUN
      → wraps in length-prefixed frame
        → sends over TLS connection to server
          → server unwraps frame
            → writes raw IP packet to server TUN
              → OS delivers to postgres container
```

**Protocol framing:**

```
[ 4 bytes: payload length ][ 1 byte: message type ][ payload ]

Types: Auth(0x01) AuthOK(0x02) AuthFail(0x03) IPPacket(0x04)
       Ping(0x05) Pong(0x06) Disconnect(0x07)
```

---

## Makefile targets

| Target | Description |
|--------|-------------|
| `make build-linux` | Build Linux amd64 → `dist/` |
| `make build-mac-arm` | Build macOS arm64 → `dist/` |
| `make build-mac-intel` | Build macOS amd64 → `dist/` |
| `make build-all` | All three platforms |
| `make install` | Build + install on this machine (client) |
| `make install-server` | Build + install + configure server on this machine |
| `make deploy VM=user@host` | Build + push + configure server on remote VM |
| `make deploy-client VM=user@host` | Build + push client on remote VM |
| `make release` | Build all + publish GitHub release |
| `make clean` | Remove `dist/` |

---

## License

MIT
