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
auth-vpn connect 20.98.154.174:7777 --token abc123xyz

# Background + auto-reconnect
auth-vpn connect 20.98.154.174:7777 --token abc123xyz --background --reconnect

auth-vpn status       # check tunnel
auth-vpn disconnect   # disconnect
```

Save a profile so you never type the token again:

```bash
auth-vpn profile save staging \
  --host 20.98.154.174:7777 \
  --token abc123xyz

auth-vpn connect staging --background --reconnect
```

> **Sharing with your team?** [docs/dev-qa-guide.md](docs/dev-qa-guide.md) is a standalone onboarding doc for developers and QA — covers install, connect, save a profile, and troubleshooting. Send it to anyone who needs access.

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

Full command reference: [docs/cli-reference.md](docs/cli-reference.md)

Key commands at a glance:

```bash
auth-vpn connect <host:port> --token <token>   # connect
auth-vpn connect <host:port> --token <token> --background --reconnect
auth-vpn status / disconnect
auth-vpn server tokens add --name alice        # token management
auth-vpn server tokens revoke --name alice
auth-vpn profile save <name> --host <h> --token <t>
```

---

## Web dashboard

After `server install`, a dashboard is available at `http://localhost:9100/ui`:

- Live stats, connected clients, token management, IP whitelist, SSH keys, and direct forwards
- Access remotely via `ssh -L 9100:localhost:9100 user@<vm-ip>`

---

## Server features

Detailed docs for each server-side feature: [docs/server-features.md](docs/server-features.md)

| Feature | What it does |
|---------|-------------|
| **IP whitelist** | Static IPs/CIDRs connect without a token; managed from dashboard or API |
| **Direct forwards** | Expose backend ports to whitelisted IPs — no auth-vpn client on the other side |
| **SSH tunnel** | Embedded SSH server on port 2222 — any SSH-capable tool can reach backend services |
| **ACL rules** | Per-device allow/deny lists enforced at the packet level |
| **HTTP API** | Full REST API at `:9100/api/` for tokens, clients, whitelist, forwards, and SSH keys |

---

## Prometheus metrics

```bash
curl http://localhost:9100/metrics
```

```
auth_vpn_uptime_seconds        auth_vpn_active_connections
auth_vpn_connections_total     auth_vpn_auth_failures_total
auth_vpn_bytes_in_total        auth_vpn_bytes_out_total
auth_vpn_dropped_packets_total
```

---

## Installation options

### Single curl command (recommended)

```bash
# Server — prompts for tunnel port (default 7777)
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server

# Server on a specific port (skips the prompt)
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --server --port=8888

# Client
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash

# Pin to a specific version
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh \
  | sudo bash -s -- --version=v1.2.3
```

> **Non-interactive installs** (piped curl, CI scripts): the port prompt is automatically skipped when stdin is not a terminal. Pass `--port=<n>` explicitly, or set the `TJ_VPN_PORT` environment variable.

### Deploy from your Mac (VM needs nothing)

```bash
git clone https://github.com/adishM98/auth-vpn && cd auth-vpn

make deploy VM=azureuser@<vm-ip>               # server
make deploy-client VM=azureuser@<vm-ip>        # client
make deploy VM=azureuser@<vm-ip> PORT=8888     # custom port
```

### From source

```bash
git clone https://github.com/adishM98/auth-vpn && cd auth-vpn
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

## Kubernetes

Run auth-vpn as a pod inside your cluster so your laptop (or CI) can reach every ClusterIP service directly — databases, dashboards, internal APIs — without a public LoadBalancer IP for each one.

```
Your laptop  ──TLS──►  auth-vpn LoadBalancer  ──►  ClusterIP services
                        (one public IP)              (stay private)
```

**Quick start:**

```bash
# 1. Build your image from the included Dockerfile and push to your registry
docker build -t <your-registry>/auth-vpn:latest .
docker push <your-registry>/auth-vpn:latest

# 2. Set your image in k8s/deployment.yaml (the only line that requires a real value)
#    Replace: image: <your-registry>/auth-vpn:latest
#    With:    image: myacr.azurecr.io/auth-vpn:latest  (or your actual tag)

# 3. Set your namespace across all three manifests (default: "default")
sed -i '' 's/namespace: default/namespace: your-namespace/g' k8s/*.yaml
# Linux: sed -i 's/namespace: default/namespace: your-namespace/g' k8s/*.yaml

# 4. Apply
kubectl apply -f k8s/pvc.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml

# 5. Get the admin token from first-boot logs
kubectl logs -n your-namespace deploy/auth-vpn

# 6. Connect, routing all cluster traffic through the tunnel
auth-vpn connect <LB-IP>:7777 --token <token> --route <service-cidr>
```

> See [docs/k8s-deployment.md](docs/k8s-deployment.md) for the full guide — namespace setup, image registry options, connecting from a laptop or CI, token management, and troubleshooting.

---

## Use in GitHub Actions

auth-vpn ships a ready-made GitHub Action. Add two steps to any job and every service on the server VM becomes reachable at `10.8.0.1`. Tokens are created and revoked automatically per job run.

```yaml
steps:
  - name: Connect to VPN
    uses: adishM98/auth-vpn@v2
    with:
      server: ${{ secrets.VPN_SERVER }}
      api-key: ${{ secrets.VPN_API_KEY }}

  # use 10.8.0.1 as the host for any service on the VM

  - name: Disconnect VPN
    if: always()
    uses: adishM98/auth-vpn/disconnect@v2
```

Secrets to add in **Settings → Secrets and variables → Actions**:

| Secret | Value |
|--------|-------|
| `VPN_SERVER` | `<vm-public-ip>:7777` |
| `VPN_API_KEY` | The `api_key` from `/etc/auth-vpn/server.yaml` |

> See [docs/github-actions.md](docs/github-actions.md) for parallel matrix jobs, proxy mode, action inputs reference, non-GitHub CI, and troubleshooting.

---

## Building from source

Requires Go 1.22+.

```bash
git clone https://github.com/adishM98/auth-vpn && cd auth-vpn

go build -o auth-vpn ./cmd    # current platform

make build-linux              # Linux amd64
make build-mac-arm            # macOS Apple Silicon
make build-mac-intel          # macOS Intel
make build-windows            # Windows amd64 (.exe)
make build-all                # all four platforms
```

---

## Verify your connection

```bash
auth-vpn status               # tunnel status
ping 10.8.0.1                 # ping the VM through the tunnel

psql      -h 10.8.0.1 -p 5432
mysql     -h 10.8.0.1 -P 3306 -u root -p
redis-cli -h 10.8.0.1 -p 6379
curl         http://10.8.0.1:8080/health
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

```bash
sudo auth-vpn update   # server VM or client laptop
```

Checks the latest GitHub release, downloads the right binary for your platform, atomically replaces the running binary, and restarts the systemd service if active. No config files are touched.

Re-running the installer on an already-configured server is also safe — preserves `server.yaml`, skips TLS cert regeneration, keeps all tokens.

### Changing the tunnel port

```bash
sudo auth-vpn server change-port
```

Updates `server.yaml` and the systemd service, then restarts. Remember to update your firewall/NSG rules and any saved client profiles.

```bash
auth-vpn version   # check current version
```

---

## Uninstalling

### Client

```bash
auth-vpn disconnect
sudo rm /usr/local/bin/auth-vpn
rm -rf ~/.auth-vpn
```

### Server

```bash
sudo systemctl stop auth-vpn && sudo systemctl disable auth-vpn
sudo rm /usr/local/bin/auth-vpn
sudo rm -rf /etc/auth-vpn
sudo rm -f /etc/systemd/system/auth-vpn.service
sudo systemctl daemon-reload
sudo rm -f /var/run/auth-vpn.sock
```

---

## Architecture

See [docs/architecture.md](docs/architecture.md) for component map, packet flow, wire protocol, and key design decisions.
