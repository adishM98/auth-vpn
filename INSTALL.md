# auth-vpn — Installation Guide

A lightweight self-hosted VPN tunnel. Install once, connect from anywhere —
every application on the machine routes through the tunnel automatically.

> **Internet speed is not affected.** auth-vpn is a split-tunnel VPN — only traffic to `10.8.0.0/24` goes through the tunnel. All other traffic (browsing, downloads, Slack) uses your normal connection unchanged.

---

## How it works

```
VM (any containers)  ← auth-vpn server (one port open: 7777)
     │
     │  encrypted TLS 1.3 tunnel
     │
     ├── Dev laptop       (auth-vpn client)
     ├── QA laptop        (auth-vpn client)
     └── client VM       (auth-vpn client)
```

Every container running on the VM is reachable through the tunnel:

```
PostgreSQL  → 10.8.0.1:5432
MySQL       → 10.8.0.1:3306
MongoDB     → 10.8.0.1:27017
Redis       → 10.8.0.1:6379
Any service → 10.8.0.1:<port>
```

No extra config per service — if the container has a port mapping, it is automatically reachable through the tunnel once connected.

---

## TL;DR — the two commands everyone needs

### VM with containers (server, run once per VM)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server
```

### Dev/QA laptop or another VM (client)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash
```

Then connect:

```bash
auth-vpn connect 20.98.154.174:7777 --token abc123xyz
```

---

## How the installer works

```
curl ... | bash
  │
  ├── Pre-built binary already on disk (from make deploy)?
  │     └── YES → install it directly
  │
  ├── Try GitHub releases download
  │     └── SUCCESS → install downloaded binary
  │
  └── Fallback: build from source
        ├── Go installed? → build directly
        └── Linux + no Go → auto-install Go, then build
```

You never need Go, Git, or anything pre-installed — the script handles it.

---

## Part 1 — VM with containers (server setup)

Run **once** on any VM that has containers you want to access securely.
Generates TLS cert, creates initial token, registers systemd service.

### Option A — Single curl command (recommended, VM needs nothing)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server

# Custom port:
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server --port=8888
```

### Option B — Deploy from your Mac (VM needs nothing)

```bash
# From inside the auth-vpn repo on your Mac:
make deploy VM=azureuser@<vm-public-ip>

# With a custom port or SSH key:
make deploy VM=azureuser@<vm-public-ip> PORT=8888 SSH_KEY=~/.ssh/my_key
```

### Option C — Clone and install on the VM directly

```bash
ssh azureuser@<vm-public-ip>
git clone https://github.com/adishM98/auth-vpn
cd auth-vpn
sudo ./install.sh --server
```

### After the server is installed

```
  ─────────────────────────────────────────────
  Connect with:
    auth-vpn connect 20.98.154.174:7777 --token abc123xyz

  Web dashboard:  http://localhost:9100/ui
  API key:        <generated-key>
  ─────────────────────────────────────────────
```

**Save that token** — it's the first team member's token. Create one per person (`auth-vpn server tokens add --name x`); each token can only be active in one place at a time.

Close all container ports from the public internet — only port **7777 (TCP)** needs to be open:

```
Azure Portal → VM → Networking → Inbound port rules
  → Delete rules for port 5432, 3306, 27017, 6379 (any exposed container ports)
  → Keep or add rule for port 7777 TCP only
```

---

## Part 2 — Dev / QA laptops (macOS / Linux / Windows)

### Option A — Single curl command (macOS / Linux)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash
```

### Option B — Windows

Download `auth-vpn-windows-amd64.exe` from the [latest release](https://github.com/adishM98/auth-vpn/releases/latest), rename it to `auth-vpn.exe`, and add it to your `PATH`.

### Connect to the server

```bash
# Interactive — Ctrl+C or auth-vpn disconnect to stop
auth-vpn connect 20.98.154.174:7777 --token abc123xyz

# Background — survives terminal close, no speed impact on other traffic
auth-vpn connect 20.98.154.174:7777 --token abc123xyz --background

# Background + auto-reconnect on unexpected drop
auth-vpn connect 20.98.154.174:7777 --token abc123xyz --background --reconnect
```

**Save a profile so you never type the token again:**

```bash
auth-vpn profile save staging \
  --host 20.98.154.174:7777 \
  --token abc123xyz

# From now on:
auth-vpn connect staging
auth-vpn connect staging --background --reconnect
```

### Stopping the tunnel

```bash
# If running in background:
auth-vpn disconnect

# If running in foreground (terminal still open):
Ctrl+C

# If installed as a systemd service (Linux VMs):
sudo systemctl stop auth-vpn
```

### Check tunnel status

```bash
auth-vpn status
```

Output when connected:

```
status: connected
  PID          : 12345
  Server       : 20.98.154.174:7777
  Tunnel IP    : 10.8.0.2
  Server IP    : 10.8.0.1
  Connected at : 2025-01-15 09:30:00
  Uptime       : 2h15m30s
```

---

## Part 3 — Other VMs (client VMs, test VMs, etc.)

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash

# Connect in background — stays alive after SSH session ends
auth-vpn connect 20.98.154.174:7777 --token abc123xyz --background --reconnect
```

Deploy from Mac (VM needs nothing):

```bash
make deploy-client VM=azureuser@<other-vm-ip>
ssh azureuser@<other-vm-ip>
auth-vpn connect 20.98.154.174:7777 --token abc123xyz --background --reconnect
```

---

## Part 4 — Token management (run on the server VM)

```bash
# List all tokens and expiry dates
auth-vpn server tokens list

# Add permanent token for a dev/QA member
auth-vpn server tokens add --name "dev-john"

# Add short-lived token for CI
auth-vpn server tokens add --name "ci-runner" --expires 24h

# Add one-time token (revokes itself after first connection)
auth-vpn server tokens add --name "temp-access" --one-time

# Revoke access immediately — no server restart needed
auth-vpn server tokens revoke --name "dev-john"
```

Tokens can also be created and revoked from the **Web dashboard** at `http://localhost:9100/ui`.

---

## Part 5 — Web dashboard

The server exposes a live dashboard at `http://localhost:9100/ui`:

- Active client count, uptime, bytes transferred
- Connected clients table (name, tunnel IP, connected at)
- Token management — create/revoke from the browser
- **IP Whitelist** — add/remove IPs and CIDRs that bypass token auth
- **SSH Keys** — generate server-side RSA keypairs or register existing public keys for SSH tunnel auth
- **Direct Forwards** — expose backend ports to whitelisted IPs, no client needed

**Access remotely via SSH tunnel:**

```bash
ssh -L 9100:localhost:9100 user@<vm-ip>
# then open: http://localhost:9100/ui
```

---

## Part 6 — IP whitelist (VMs and static IPs, no token needed)

Whitelist IPs or CIDR ranges so those machines connect without a token.
Intended for VMs, PaaS services, and CI runners with known static IPs.

**From the dashboard** (`http://localhost:9100/ui` → IP Whitelist):
- Enter a name and IP or CIDR → click Add
- Changes take effect instantly

**Use cases:**

| Machine | What to whitelist |
|---|---|
| Azure / AWS / GCP VM | Private IP (same VNet) or public IP |
| Render / Railway | Published outbound CIDR from their dashboard |
| Office network | Office public IP |
| Dev/QA laptops | Use tokens instead — home IPs change |

Once whitelisted, the machine can connect with no `--token`:
```bash
auth-vpn connect 20.98.154.174:7777   # IP is checked, token skipped
```

---

## Part 7 — Direct forwards (no auth-vpn client, whitelist only)

Expose a backend service port directly to whitelisted IPs.
The external machine uses a plain TCP connection — **no auth-vpn binary needed**.

**From the dashboard** (→ Direct Forwards):
- Enter listen port (e.g. `5432`) and target (e.g. `127.0.0.1:5432`) → click Add
- The listener starts immediately — no restart needed

**Example: accessing a service without installing auth-vpn:**

1. Whitelist the client VM's public IP (Part 6 above)
2. Add a direct forward: `5432 → 127.0.0.1:5432`
3. Open port 5432 in your firewall/NSG for that IP only
4. Configure your app datasource:
   ```
   Host: 20.98.154.174   ← auth-vpn server public IP
   Port: 5432
   ```

Connection flow:
```
Client VM → TCP :5432 → auth-vpn server
  → IP whitelisted? YES → proxy to 127.0.0.1:5432 → postgres
```

> Only whitelisted IPs reach the backend — all other connections are dropped immediately.

---

## Part 8 — SSH tunnel (any SSH client, no auth-vpn binary needed)

auth-vpn runs an embedded SSH server on port **2222**. Any tool that supports SSH can connect via standard SSH local port forwarding — no auth-vpn binary or VPN client required.

### Auth methods

| Method | How |
|--------|-----|
| Private key | Generate from dashboard → SSH Keys → Generate Keypair |
| Password | Use any auth-vpn token as the SSH password (any username) |
| System key | Any key in `~/.ssh/authorized_keys` on the server VM works automatically |

### SSH Keys API

```bash
# List registered keys
curl http://localhost:9100/api/ssh-keys

# Generate a new keypair
curl -X POST http://localhost:9100/api/ssh-keys/generate \
  -d '{"name":"my-app-prod"}'

# Register an existing public key
curl -X POST http://localhost:9100/api/ssh-keys \
  -d '{"name":"alice","public_key":"ssh-rsa AAAA..."}'

# Remove a key
curl -X DELETE http://localhost:9100/api/ssh-keys/my-app-prod
```

---

## Part 9 — ACL rules (optional, restrict per device)

Edit `/etc/auth-vpn/acl.yaml`:

```yaml
default_policy: deny

rules:
  - device: dev-alice
    allow:
      - proto: tcp
        port: 5432   # PostgreSQL

  - device: ci-runner
    allow:
      - proto: tcp
        port: 5432
      - proto: tcp
        port: 6379   # Redis
```

Reload without restarting:

```bash
sudo kill -SIGHUP $(pgrep auth-vpn)
# or
sudo systemctl kill -s HUP auth-vpn
```

---

## Part 10 — Server clients list and force-disconnect

See who is currently connected:

```bash
sudo auth-vpn server clients
```

```
NAME                  TUNNEL IP        CONNECTED AT
dev-alice             10.8.0.2         2025-01-15 09:30:00
ci-runner             10.8.0.3         2025-01-15 10:00:00
```

Force-disconnect a stuck or unwanted client (frees the token immediately):

```bash
# From the dashboard: Connected Clients → Kick button

# Or via API:
curl -X DELETE http://localhost:9100/api/clients/dev-alice
```

---

## Verify your connection

```bash
# Is the tunnel up?
auth-vpn status

# Ping the VM through the tunnel
ping 10.8.0.1

# Reach any container by port:
psql    -h 10.8.0.1 -p 5432                    # PostgreSQL
mysql   -h 10.8.0.1 -P 3306 -u root -p         # MySQL
mongosh    10.8.0.1:27017                       # MongoDB
redis-cli -h 10.8.0.1 -p 6379                  # Redis
curl       http://10.8.0.1:8080/health          # any HTTP service
```

---

## Updating

Run one command — no reinstall, no config changes, no downtime on clients already connected.

### Server VM

```bash
sudo auth-vpn update
```

Downloads the latest binary, replaces it atomically, then automatically restarts the `auth-vpn` systemd service. Connected clients will reconnect on their own if `--reconnect` is set.

### Client (laptop or other VM)

```bash
sudo auth-vpn update
```

Same thing — downloads the latest binary and replaces it. Reconnect with `auth-vpn connect` as usual.

### Check current version

```bash
auth-vpn version
```

---

## Troubleshooting

**`permission denied` when running install.sh**
```bash
chmod +x install.sh
sudo ./install.sh --server   # server
sudo ./install.sh            # client
```

**`Cannot write to /usr/local/bin`**

When installing via curl pipe, use `sudo bash` (not `sudo curl`):
```bash
# Correct:
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash

# Wrong — sudo applies to curl, not bash:
sudo curl -fsSL ... | bash
```

**Download fails (no internet / firewall)**
```bash
# Download the binary manually on a machine with internet access:
curl -LO https://github.com/adishM98/auth-vpn/releases/latest/download/auth-vpn-linux-amd64
# Copy it to the target machine — install.sh will detect and use it directly
```

**Tunnel connects but can't reach a container**
- Check the container is running: `docker ps`
- Check port mapping uses `0.0.0.0`: `docker ps --format "{{.Ports}}"`
- If it shows `127.0.0.1:<port>`, update your compose file:

```yaml
ports:
  - "5432:5432"           # ✅ accessible through tunnel
  # not:
  - "127.0.0.1:5432:5432" # ❌ only localhost, tunnel can't reach it
```

**Internet is slow while tunnel is running**

It shouldn't be — auth-vpn is a split-tunnel. Only traffic to `10.8.0.0/24` goes through the VPN. Run `netstat -rn` to confirm only the VPN subnet is routed through the TUN interface and your default route (`0.0.0.0`) is unchanged.

---

## DevOps: publishing a new release

```bash
# Build all platforms + upload to GitHub releases
make release

# Pin a specific version
make release VERSION=1.2.3
```

Requires the `gh` CLI (`brew install gh`) and `gh auth login` once.
After `make release`, the curl one-liners above automatically pick up the new version.

---

## Quick reference — all Makefile targets

| Command | What it does |
|---|---|
| `make build-linux` | Build Linux amd64 binary → `dist/` |
| `make build-mac-arm` | Build macOS arm64 binary → `dist/` |
| `make build-mac-intel` | Build macOS amd64 binary → `dist/` |
| `make build-windows` | Build Windows amd64 `.exe` → `dist/` |
| `make build-all` | Build all four platforms |
| `make install` | Build + install on this machine (client) |
| `make install-server` | Build + install + configure server on this machine |
| `make deploy VM=user@host` | Build + deploy + configure **server** on remote VM |
| `make deploy-client VM=user@host` | Build + deploy **client** on remote VM |
| `make release` | Build all + create GitHub release |
| `make clean` | Remove `dist/` |

---

## Who does what — at a glance

| Role | Command | When |
|---|---|---|
| DevOps | `make release` | Shipping a new version |
| DevOps | `sudo auth-vpn update` | Update server to latest release (restarts service automatically) |
| Dev / QA | `sudo auth-vpn update` | Update client to latest release |
| DevOps | `curl ... \| sudo bash -s -- --server` | Once per VM that has containers |
| DevOps | `make deploy-client VM=user@host` | Once per additional VM (alternative) |
| Dev / QA | `curl ... \| sudo bash` then `auth-vpn connect staging` | Once per laptop |
| Dev / QA | `auth-vpn disconnect` | When done for the day |
| DevOps | `auth-vpn server tokens add --name x` | Onboarding new team member (one token per person) |
| DevOps | `auth-vpn server tokens revoke --name x` | Offboarding |
| DevOps | `sudo auth-vpn server clients` | Check who is connected |
| DevOps | Dashboard → Clients → Kick | Force-disconnect a stuck client |
