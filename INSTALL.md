# tj-vpn — Installation Guide

A lightweight self-hosted VPN tunnel. Install once, connect from anywhere —
every application on the machine routes through the tunnel automatically.

---

## How it works

```
VM (any containers)  ← tj-vpn server (one port open: 7777)
     │
     │  encrypted TLS tunnel
     │
     ├── Dev laptop       (tj-vpn client)
     ├── QA laptop        (tj-vpn client)
     └── ToolJet VM       (tj-vpn client)
```

tj-vpn tunnels **all traffic** to the VM — not just one service.
Every container running on the VM is reachable through the same tunnel:

```
PostgreSQL  → 10.0.0.1:5432
MySQL       → 10.0.0.1:3306
MongoDB     → 10.0.0.1:27017
Redis       → 10.0.0.1:6379
Any service → 10.0.0.1:<port>
```

No extra config per service — if the container has a port mapping on the VM,
it is automatically reachable through the tunnel once connected.

---

## TL;DR — the two commands everyone needs

### VM with containers (server, run once per VM)

```bash
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server
```

No Go, no Git, nothing to install first. The script downloads the right binary
for the machine, installs it, generates a TLS cert, and starts the systemd service.

### Dev/QA laptop or another VM (client)

```bash
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | bash
```

Then connect:

```bash
tj-vpn connect 20.98.154.174:7777 --token abc123xyz
```

---

## How the installer works

The installer detects the situation and takes the right path:

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

You never need Go, Git, or anything else pre-installed — the script handles it.

---

## Part 1 — VM with containers (server setup)

Run **once** on any VM that has containers you want to access securely.
Generates TLS cert, creates initial token, registers systemd service.

### Option A — Single curl command (recommended, VM needs nothing)

```bash
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server

# Custom port:
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server --port=8888
```

### Option B — Deploy from your Mac (VM needs nothing)

Best for controlled rollouts — DevOps builds locally and pushes everything.

```bash
# From inside the tj-vpn repo on your Mac:
make deploy VM=azureuser@<vm-public-ip>

# With a custom port:
make deploy VM=azureuser@<vm-public-ip> PORT=8888

# With a specific SSH key:
make deploy VM=azureuser@<vm-public-ip> SSH_KEY=~/.ssh/my_key
```

This automatically:
1. Builds the Linux binary on your Mac
2. Copies binary + `install.sh` to the VM
3. SSHs in and runs `sudo ./install.sh --server`

### Option C — Clone and install on the VM directly

```bash
ssh azureuser@<vm-public-ip>
git clone https://github.com/tooljet/tj-vpn
cd tj-vpn
sudo ./install.sh --server
```

### After the server is installed

You will see the connection string printed:

```
  ────────────────────────────────────────────
  Connect with:
    tj-vpn connect 20.98.154.174:7777 --token abc123xyz
  ────────────────────────────────────────────
```

**Save that token — share it with your team.**

Close all container ports from the public internet — only port **7777 (TCP)** needs to be open:

```
Azure Portal → VM → Networking → Inbound port rules
  → Delete rules for port 5432, 3306, 27017, 6379 (any exposed container ports)
  → Keep or add rule for port 7777 TCP only
```

All container ports are now only reachable through the tunnel.

---

## Part 2 — Dev / QA laptops (macOS)

### Option A — Single curl command (recommended)

```bash
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | bash
```

### Option B — Clone and install

```bash
git clone https://github.com/tooljet/tj-vpn
cd tj-vpn
./install.sh
```

### Option C — DevOps deploys for the team

```bash
# From Mac, inside the tj-vpn repo:
make deploy-client VM=username@<laptop-ip>
```

### Connect to the server

```bash
tj-vpn connect 20.98.154.174:7777 --token abc123xyz
```

**Save a profile so you never type the token again:**

```bash
tj-vpn profile save staging \
  --host 20.98.154.174:7777 \
  --token abc123xyz

# From now on:
tj-vpn connect staging

# Disconnect when done:
tj-vpn disconnect
```

---

## Part 3 — Other VMs (ToolJet VM, test VMs, etc.)

### Option A — Single curl command (recommended)

```bash
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | bash

tj-vpn connect 20.98.154.174:7777 --token abc123xyz --background
```

### Option B — Deploy from Mac (VM needs nothing)

```bash
make deploy-client VM=azureuser@<other-vm-ip>

# SSH in and connect:
ssh azureuser@<other-vm-ip>
tj-vpn connect 20.98.154.174:7777 --token abc123xyz --background
```

### Option C — Clone and install on the VM directly

```bash
ssh azureuser@<other-vm-ip>
git clone https://github.com/tooljet/tj-vpn && cd tj-vpn
sudo ./install.sh   # auto-installs Go if missing
tj-vpn connect 20.98.154.174:7777 --token abc123xyz --background
```

`--background` keeps the tunnel alive after the SSH session ends.

---

## Part 4 — Token management (run on the server VM)

```bash
# List all tokens and expiry dates
tj-vpn server tokens list

# Add permanent token for a dev/QA member
tj-vpn server tokens add --name "dev-john"

# Add short-lived token for CI
tj-vpn server tokens add --name "ci-runner" --expires 24h

# Add one-time token (revokes itself after first connection)
tj-vpn server tokens add --name "temp-access" --one-time

# Revoke access immediately — no server restart needed
tj-vpn server tokens revoke --name "dev-john"
```

---

## Verify your connection

```bash
# Is the tunnel up?
tj-vpn status

# Ping the VM through the tunnel
ping 10.0.0.1

# Reach any container by port — examples:
psql    -h 10.0.0.1 -p 5432                    # PostgreSQL
mysql   -h 10.0.0.1 -P 3306 -u root -p         # MySQL
mongosh    10.0.0.1:27017                       # MongoDB
redis-cli -h 10.0.0.1 -p 6379                  # Redis
curl       http://10.0.0.1:8080/health          # any HTTP service

# Disconnect
tj-vpn disconnect
```

---

## Pin to a specific version

```bash
# Server
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server --version=v1.2.3

# Client
curl -fsSL https://github.com/tooljet/tj-vpn/releases/latest/download/install.sh \
  | bash -s -- --version=v1.2.3
```

Or set the environment variable:

```bash
VERSION=v1.2.3 ./install.sh
```

---

## Troubleshooting

**`permission denied` when running install.sh**
```bash
chmod +x install.sh
sudo ./install.sh --server   # server
./install.sh                 # client
```

**`Cannot write to /usr/local/bin`**
```bash
sudo ./install.sh   # always run with sudo on Linux
```

**Download fails (no internet / firewall)**
```bash
# Download the binary manually on a machine with internet:
curl -LO https://github.com/tooljet/tj-vpn/releases/latest/download/tj-vpn-linux-amd64
# Copy it to the target machine and run install.sh — it will detect the local binary
```

**Tunnel connects but can't reach a container**
- Check the container is running: `docker ps`
- Check port mapping includes `0.0.0.0` not `127.0.0.1`: `docker ps --format "{{.Ports}}"`
- If it shows `127.0.0.1:<port>`, update `docker-compose.yml` ports to `"<port>:<port>"` (no localhost prefix)
- Example fix in `docker-compose.yml`:
  ```yaml
  ports:
    - "5432:5432"    # ✅ accessible through tunnel
    # not:
    - "127.0.0.1:5432:5432"  # ❌ only localhost, tunnel can't reach it
  ```

---

## DevOps: publishing a new release

```bash
# Build all platforms + upload to GitHub releases
make release

# Pin a specific version
make release VERSION=1.2.3
```

Requires the `gh` CLI (`brew install gh`) and `gh auth login` run once.
After `make release`, the curl one-liners above automatically pick up the new version.

---

## Quick reference — all Makefile targets

| Command | What it does |
|---|---|
| `make build-linux` | Build Linux amd64 binary → `dist/` |
| `make build-mac-arm` | Build macOS arm64 binary → `dist/` |
| `make build-mac-intel` | Build macOS amd64 binary → `dist/` |
| `make build-all` | Build all three platforms |
| `make install` | Build + install on this machine (client) |
| `make install-server` | Build + install + configure server on this machine |
| `make deploy VM=user@host` | Build + deploy + configure **server** on remote VM |
| `make deploy-client VM=user@host` | Build + deploy **client** on remote VM |
| `make release` | Build all + create GitHub release (uploads binaries + install.sh) |
| `make clean` | Remove `dist/` |

---

## Who does what — at a glance

| Role | Command | When |
|---|---|---|
| DevOps | `make release` | Shipping a new version |
| DevOps | `curl ... \| sudo bash -s -- --server` | Once per VM that has containers |
| DevOps | `make deploy-client VM=user@host` | Once per additional VM (alternative) |
| Dev / QA | `curl ... \| bash` then `tj-vpn connect staging` | Once per laptop |
| DevOps | `tj-vpn server tokens add --name x` | Onboarding new team member |
| DevOps | `tj-vpn server tokens revoke --name x` | Offboarding |
