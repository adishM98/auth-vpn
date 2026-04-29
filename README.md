# auth-vpn

A lightweight, self-hosted VPN tunnel for developers and teams. One binary, one open port — every application on the machine routes through an encrypted TLS 1.3 tunnel automatically.

Built for the common case: a team that needs secure access to services running in Docker containers on a cloud VM, without exposing ports to the internet.

---

## How it works

```
VM (Docker containers)   ←   auth-vpn server   (configurable port, TLS)
          │
          │  encrypted TLS 1.3 tunnel
          │
          ├── Dev laptop        (auth-vpn client)
          ├── QA laptop         (auth-vpn client)
          ├── Test VM           (auth-vpn client)
          └── GitHub Actions    (auth-vpn action — ephemeral token per job)
```

The server creates a TUN interface at `10.8.0.1` and assigns each connecting client an IP from `10.8.0.2–254`. All IP traffic to the `10.8.0.0/24` subnet is routed through the tunnel at the OS level — no per-app configuration needed.

Every container on the VM is reachable the same way:

```
PostgreSQL  →  10.8.0.1:5432
MySQL       →  10.8.0.1:3306
MongoDB     →  10.8.0.1:27017
Redis       →  10.8.0.1:6379
Any service →  10.8.0.1:<port>
```

> **Internet speed is not affected.** auth-vpn is a split-tunnel — only traffic to `10.8.0.0/24` goes through the tunnel. All other traffic (browsing, downloads, Slack) uses your normal connection.

---

## Quick start

### 1 — Server (the VM with your Docker containers)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server
```

No Go, no Git, nothing to install first. The script detects your platform, downloads the right binary, generates a self-signed TLS cert, creates an initial access token, and registers a systemd service.

The installer will prompt you for the tunnel port:

```
  Enter tunnel port [7777]:
```

Press Enter to keep the default (`7777`), or type any port you prefer. If port 7777 is already taken on your VM, enter a different one here.

At the end you'll see:

```
  ─────────────────────────────────────────────
  Connect with:
    auth-vpn connect 20.98.154.174:7777 --token abc123xyz

  Web dashboard:  http://localhost:9100/ui
  API key:        <generated-key>
  ─────────────────────────────────────────────
```

**Save that token** — it's the first team member's token. Create one per person, one for CI, one for each external service. Revoke any individual token without affecting anyone else.

### 2 — Client (dev/QA laptop or another VM)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash
```

Then connect:

```bash
# Interactive (stay in foreground — Ctrl+C to disconnect)
auth-vpn connect 20.98.154.174:7777 --token abc123xyz

# Background (stays alive after terminal closes)
auth-vpn connect 20.98.154.174:7777 --token abc123xyz --background

# Check status
auth-vpn status

# Disconnect
auth-vpn disconnect
```

Save a profile so you never type the token again:

```bash
auth-vpn profile save staging \
  --host 20.98.154.174:7777 \
  --token abc123xyz

auth-vpn connect staging
auth-vpn connect staging --background --reconnect  # auto-reconnect on drop
```

---

## Security model

- **TLS 1.3** — all traffic is encrypted in transit
- **Token auth** — SHA-256 hashed tokens stored on server, raw token never persisted
- **SSH key auth** — connect the embedded SSH server using an RSA key pair; generate server-side or register your own; system `authorized_keys` automatically trusted
- **Rate limiting** — 5 failed auth attempts = 60 second IP ban
- **Ephemeral tokens** — `--github-action` flag auto-mints a unique per-job token so parallel CI jobs never share credentials; token is revoked automatically on exit
- **Token controls** — permanent, expiring, or one-time tokens; revoke any token instantly without restarting the server
- **ACL rules** — per-device allow/deny lists enforced at the packet level (optional)
- **API key** — Bearer token required for the Web UI and HTTP API (optional)
- **Single port** — close all container ports from the internet; only the tunnel port (default `7777`, configurable at install or via `server change-port`) needs to be open
- **IP whitelist** — static IPs/CIDRs (VMs, PaaS) can connect without a token; managed from the dashboard
- **Direct forwards** — expose backend ports to whitelisted IPs with no auth-vpn client required
- **SSH tunnel** — tools like your app or BI tools connect via standard SSH port forwarding; no auth-vpn binary needed on the client side

---

## CLI reference

### Server

```bash
# Install + configure (run once per VM — prompts for tunnel port)
sudo auth-vpn server install

# Install with a specific port (skips the prompt)
sudo auth-vpn server install --port 8888

# Change the tunnel port after installation (prompts for new port, restarts service)
sudo auth-vpn server change-port

# Start the server (systemd handles this automatically after install)
sudo auth-vpn server start

# Start with custom settings (overrides server.yaml)
sudo auth-vpn server start --subnet 10.8.0.0/24 --server-ip 10.8.0.1
sudo auth-vpn server start --metrics-addr 0.0.0.0:9100 --api-key <key>
sudo auth-vpn server start --acl /etc/auth-vpn/acl.yaml

# See who is currently connected
sudo auth-vpn server clients

# Token management
auth-vpn server tokens list
auth-vpn server tokens add --name "dev-alice"
auth-vpn server tokens add --name "ci-runner"   --expires 24h
auth-vpn server tokens add --name "temp"        --one-time
auth-vpn server tokens revoke --name "dev-alice"
```

### Client

```bash
# Connect interactively (Ctrl+C to disconnect)
auth-vpn connect 20.98.154.174:7777 --token <token>

# Connect using a saved profile
auth-vpn connect staging

# Background mode (survives terminal close)
auth-vpn connect staging --background

# Background + auto-reconnect on unexpected drop (exponential backoff, max 2 min)
auth-vpn connect staging --background --reconnect

# Wait until server is reachable before connecting (useful in CI)
auth-vpn connect staging --background --wait

# GitHub Actions: auto-mint a unique ephemeral token per job (reads AUTH_VPN_API_KEY env var)
# Note: run with & so the step doesn't block; use `auth-vpn disconnect` at the end to revoke the token
auth-vpn connect $VPN_HOST --github-action --forward 5432:localhost:5432 &

# Check tunnel status
auth-vpn status

# Disconnect background tunnel
auth-vpn disconnect
```

### Proxy mode (no TUN device, no root required)

Use `--forward` instead of a TUN tunnel. auth-vpn binds a local port and forwards
all TCP traffic through the encrypted tunnel to the remote host:port.
Works anywhere — Docker containers, Render, Railway, Cloud Run, CI runners.

```bash
# Forward local 5432 → postgres on the VM, local 6379 → redis on the VM
auth-vpn connect 20.98.154.174:7777 --token <token> \
  --forward 5432:10.8.0.1:5432 \
  --forward 6379:10.8.0.1:6379 \
  --background --reconnect

# Multiple forwards, background + auto-reconnect
auth-vpn connect staging \
  --forward 5432:10.8.0.1:5432 \
  --forward 3306:10.8.0.1:3306 \
  --forward 27017:10.8.0.1:27017 \
  --background --reconnect
```

Point your app at `127.0.0.1:<localPort>` — it connects as if the service is local.
No routing table changes, no kernel privileges needed.

### Profiles

```bash
auth-vpn profile save staging --host 20.98.154.174:7777 --token <token>
auth-vpn profile list
```

---

## Web dashboard

After `server install`, a dashboard is available at `http://localhost:9100/ui` on the server:

- Live stats: active clients, total connections, auth failures, bytes in/out, uptime
- Connected clients table with tunnel IP, connection time, bytes in/out, and a **Kick** button to force-disconnect any client instantly
- Token management: create and revoke tokens from the browser
- **IP Whitelist**: add/remove IPs or CIDRs that can connect without a token
- **SSH Keys**: generate server-side RSA keypairs or register existing public keys for SSH tunnel auth
- **Direct Forwards**: expose backend ports to whitelisted IPs — no auth-vpn client needed

To access the dashboard remotely, use an SSH tunnel:

```bash
ssh -L 9100:localhost:9100 user@<vm-ip>
# then open http://localhost:9100/ui in your browser
```

---

## IP whitelist

VMs, PaaS services, and CI runners with static public IPs can be whitelisted so they
connect **without a token**. Managed from the dashboard or API — changes take effect
instantly, no server restart needed.

```
Dashboard → IP Whitelist → add name + IP or CIDR
```

```bash
# API
curl -X POST http://localhost:9100/api/whitelist \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-vm","ip":"20.x.x.x"}'

# CIDR range (e.g. Render outbound IPs)
curl -X POST http://localhost:9100/api/whitelist \
  -d '{"name":"render-us-east","ip":"3.210.0.0/16"}'

# Remove
curl -X DELETE http://localhost:9100/api/whitelist/my-vm
```

| Who | Use |
|---|---|
| Cloud VMs (Azure, AWS, GCP) | Static public IP — whitelist the IP |
| Render / Railway / PaaS | Whitelist the published outbound CIDR |
| Office with static IP | Whitelist the office public IP |
| Dev/QA laptops | Use tokens — home IPs change too often |

Whitelisted IPs can also connect via auth-vpn client without `--token`:
```bash
auth-vpn connect 20.98.154.174:7777   # no --token needed
```

---

## Direct forwards (no auth-vpn client required)

Expose backend service ports directly to whitelisted IPs. The external machine connects
with a plain TCP connection — **no auth-vpn binary needed on their side**.

```
Dashboard → Direct Forwards → add listen port + target
```

Example: whitelist `my-vm` (IP `20.x.x.x`), then add a forward:

```
Listen port  →  Target
5432         →  127.0.0.1:5432   (postgres container)
3306         →  127.0.0.1:3306   (mysql container)
```

Your app connects directly — no VPN client needed:
```
Host: 20.98.154.174   ← auth-vpn server public IP
Port: 5432
```

```bash
# API
curl -X POST http://localhost:9100/api/forwards \
  -H 'Content-Type: application/json' \
  -d '{"listen_port":5432,"target":"127.0.0.1:5432"}'

# Remove
curl -X DELETE http://localhost:9100/api/forwards/5432
```

Connection flow:
```
External machine (whitelisted IP)
  → TCP connect to auth-vpn server :5432
    → IP check: whitelisted? YES
      → proxied to 127.0.0.1:5432 (postgres container)
        → plain TCP from here on
```

> Remember to open the forwarded ports in your firewall/NSG for the whitelisted IPs.
> Only those IPs can connect — all others are dropped immediately.

---

## SSH tunnel

auth-vpn runs an embedded SSH server on port **2222** that supports standard SSH local port forwarding.
Any tool that supports SSH tunneling can connect to backend services this way — **no auth-vpn binary needed**.

### Auth methods

| Method | How |
|--------|-----|
| Private key | Generate a keypair from the dashboard → SSH Keys → Generate, or register an existing public key |
| Password | Use any auth-vpn token as the SSH password (any username) |
| System key | Any key in `~/.ssh/authorized_keys` on the server VM automatically works |

### Generate a keypair (from the dashboard)

1. Open `http://localhost:9100/ui` → **SSH Keys** tab
2. Click **Generate Keypair**, enter a name
3. Copy the private key — it is shown only once, never stored server-side
4. Paste it into your SSH client or tool

### API

```bash
# List registered keys
curl http://localhost:9100/api/ssh-keys

# Generate a new keypair (private key returned once)
curl -X POST http://localhost:9100/api/ssh-keys/generate \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-app"}'

# Register an existing public key
curl -X POST http://localhost:9100/api/ssh-keys \
  -H 'Content-Type: application/json' \
  -d '{"name":"alice","public_key":"ssh-rsa AAAA..."}'

# Remove a key
curl -X DELETE http://localhost:9100/api/ssh-keys/my-app
```

---

## Prometheus metrics

```bash
curl http://localhost:9100/metrics
```

```
auth_vpn_uptime_seconds
auth_vpn_active_connections
auth_vpn_connections_total
auth_vpn_auth_failures_total
auth_vpn_bytes_in_total
auth_vpn_bytes_out_total
auth_vpn_dropped_packets_total
```

---

## ACL rules

Create `/etc/auth-vpn/acl.yaml` to restrict what each device can reach:

```yaml
default_policy: deny

rules:
  - device: dev-alice
    allow:
      - proto: tcp
        port: 5432   # PostgreSQL only
      - proto: tcp
        port: 6379   # Redis

  - device: ci-runner
    allow:
      - proto: tcp
        port: 5432
```

Reload without restarting the server:

```bash
sudo kill -SIGHUP <server-pid>
# or: sudo systemctl kill -s HUP auth-vpn
```

---

## HTTP API

The server exposes an HTTP API at `http://localhost:9100/api/`:

```
GET /api/status              — server health + active client count
GET /api/clients             — list of connected devices
GET /api/probe?host=IP&port=N  — verify a host:port is reachable via VPN
```

### Connected clients API

```bash
# List connected clients
curl http://localhost:9100/api/clients

# Force-disconnect a client by name (frees the token immediately)
curl -X DELETE http://localhost:9100/api/clients/dev-alice
```

Protect with an API key (set in `server.yaml` or `--api-key` flag):

```
Authorization: Bearer <api-key>
```

---

## Verify your connection

```bash
# Tunnel status
auth-vpn status

# Ping the VM through the tunnel
ping 10.8.0.1

# Reach any container
psql      -h 10.8.0.1 -p 5432
mysql     -h 10.8.0.1 -P 3306 -u root -p
mongosh      10.8.0.1:27017
redis-cli -h 10.8.0.1 -p 6379
curl         http://10.8.0.1:8080/health
```

---

## Installation options

### Single curl command (recommended — no dependencies needed)

```bash
# Server — prompts for the tunnel port interactively (default 7777)
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server

# Server on a specific port (skips the prompt — useful for automation)
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server --port=8888

# Client
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash

# Pin to a specific version
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --version=v1.2.3
```

> **Non-interactive installs** (piped curl, CI scripts): the port prompt is automatically skipped when stdin is not a terminal. Pass `--port=<n>` explicitly, or set the `TJ_VPN_PORT` environment variable before piping.

### Deploy from your Mac (VM needs nothing)

```bash
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
sudo ./install.sh              # client
sudo ./install.sh --server     # server
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
      - "5432:5432"       # ✅ accessible through tunnel
      # not: "127.0.0.1:5432:5432"  — tunnel can't reach this
```

---

## Use in GitHub Actions

auth-vpn ships a ready-made GitHub Action. Add two steps to any job — one to connect, one to disconnect — and every service on the server VM becomes reachable at `10.8.0.1`.

Tokens are fully automatic: a unique ephemeral token is created for each job run and revoked when the job ends. No manual token management, no conflicts between parallel matrix jobs.

### 1 — Add secrets

In your repository go to **Settings → Secrets and variables → Actions** and add:

| Secret | Value |
|--------|-------|
| `VPN_SERVER` | `<vm-public-ip>:7777` |
| `VPN_API_KEY` | The `api_key` from `/etc/auth-vpn/server.yaml` on the server VM |

### 2 — Add the steps

```yaml
jobs:
  test:
    runs-on: ubuntu-22.04
    steps:
      - name: Connect to VPN
        uses: adishM98/auth-vpn@v2
        with:
          server: ${{ secrets.VPN_SERVER }}
          api-key: ${{ secrets.VPN_API_KEY }}

      # Your steps here — use 10.8.0.1 as the host for any service on the VM
      # e.g. DB_HOST=10.8.0.1, REDIS_HOST=10.8.0.1

      - name: Disconnect VPN
        if: always()
        uses: adishM98/auth-vpn/disconnect@v2
```

### Parallel matrix jobs

The same two secrets work for any number of parallel jobs simultaneously — each job gets its own token automatically.

```yaml
strategy:
  matrix:
    edition: [ee, ce, community]

steps:
  - name: Connect to VPN
    uses: adishM98/auth-vpn@v2
    with:
      server: ${{ secrets.VPN_SERVER }}
      api-key: ${{ secrets.VPN_API_KEY }}   # one secret, no per-job config
```

### Docker containers in the workflow

If your job runs `docker-compose up`, those containers can also reach `10.8.0.1` with no changes to your `docker-compose.yaml`. Docker routes container traffic through the host network stack, so the VPN tunnel applies to container traffic automatically.

### Action inputs

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `server` | yes | — | VPN server address, e.g. `203.0.113.10:7777` |
| `api-key` | yes | — | Server API key for ephemeral token generation |
| `api-url` | no | `http://<host>:9100` | Override the API endpoint (use `https://` if the server has TLS certs) |
| `routes` | no | — | Extra CIDRs to route via VPN, comma-separated (e.g. `10.20.0.0/16`) |
| `mode` | no | `tun` | `tun` (full OS routing, needs sudo) or `proxy` (explicit port-forwards, no root) |
| `forwards` | no | — | Proxy mode only: `"5432:10.8.0.1:5432 6379:10.8.0.1:6379"` |
| `version` | no | `latest` | Binary version to download, e.g. `v2.0.2` |

> See [docs/github-actions.md](docs/github-actions.md) for a detailed guide, full Cypress example, and troubleshooting steps.

### Non-GitHub CI (GitLab, Bitbucket, etc.)

Use the binary directly with a static token stored as a CI secret:

```bash
# Install
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash

# Connect
sudo auth-vpn connect $VPN_SERVER --token $VPN_TOKEN --background --reconnect

# Run tests ...

# Disconnect (revokes the session)
auth-vpn disconnect
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
make build-windows      # Windows amd64 (.exe)
make build-all          # all four platforms
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
│  │  :<port>(TLS) │    │  postgres :5432   │  │
│  │  :9100 (HTTP)│    │  mysql    :3306   │  │
│  │  :2222 (SSH) │    │  redis    :6379   │  │
│  │  TUN: tun0   │    │  ...              │  │
│  │  10.8.0.1/24 │    └───────────────────┘  │
│  └──────────────┘                           │
└─────────────────────────────────────────────┘
          │  TLS 1.3 / configurable port (default 7777)
          │
   ┌──────┴──────────────────────────────┐  ┌──────────────────────────────────────┐
   │  Dev / QA machine                   │  │  GitHub Actions runner               │
   │                                     │  │                                      │
   │  TUN: utun3 (macOS) / tun0 (Linux)  │  │  TUN: tun0 (Linux)                   │
   │  IP: 10.8.0.2                       │  │  IP: 10.8.0.x (assigned per job)     │
   │                                     │  │                                      │
   │  Route: 10.8.0.0/24 → TUN           │  │  Ephemeral token auto-created        │
   │  Everything else → normal internet  │  │  and revoked per job run             │
   └─────────────────────────────────────┘  └──────────────────────────────────────┘
```

**Packet flow:**

```
App on client writes to 10.8.0.1:5432
  → OS routes packet to TUN (split-tunnel — only VPN subnet)
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
| `make build-windows` | Build Windows amd64 → `dist/` |
| `make build-all` | All four platforms |
| `make install` | Build + install on this machine (client) |
| `make install-server` | Build + install + configure server on this machine |
| `make deploy VM=user@host` | Build + deploy + configure server on remote VM |
| `make deploy-client VM=user@host` | Build + deploy client on remote VM |
| `make release` | Build all + publish GitHub release |
| `make clean` | Remove `dist/` |

---

## Updating

A single command updates the binary and (on the server) restarts the service automatically:

```bash
# Server VM
sudo auth-vpn update

# Client (laptop or other VM)
sudo auth-vpn update
```

`auth-vpn update` checks the latest GitHub release, downloads the right binary for your platform, atomically replaces the running binary, and — if the `auth-vpn` systemd service is active — restarts it. No config files are touched.

Re-running the installer on an already-configured server is also safe:

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server
```

On a re-install, the installer preserves your existing `server.yaml` (keeping `forward_bind_addr`, custom subnet, API key, and all other settings), skips TLS cert regeneration so connected clients are not interrupted, and keeps all existing tokens.

### Changing the tunnel port after installation

```bash
sudo auth-vpn server change-port
```

This prompts for a new port, updates `server.yaml` and the systemd service, and restarts auth-vpn. It will remind you to:
1. Open the new port in your firewall / NSG
2. Close the old port
3. Update `DATASOURCE_VPN_HOST` (or any saved profiles) to `<ip>:<new-port>`

Check the current version at any time:

```bash
auth-vpn version
```

---

## Uninstalling

### Client

```bash
# Disconnect first if running in background
auth-vpn disconnect

# Remove the binary
sudo rm /usr/local/bin/auth-vpn

# Remove saved profiles and state
rm -rf ~/.auth-vpn
```

### Server

```bash
# Stop and disable the systemd service
sudo systemctl stop auth-vpn
sudo systemctl disable auth-vpn

# Remove the binary
sudo rm /usr/local/bin/auth-vpn

# Remove config, TLS certs, and tokens
sudo rm -rf /etc/auth-vpn

# Remove the systemd service file
sudo rm -f /etc/systemd/system/auth-vpn.service
sudo systemctl daemon-reload

# Remove the Unix control socket (if present)
sudo rm -f /var/run/auth-vpn.sock
```

---

## License

MIT
