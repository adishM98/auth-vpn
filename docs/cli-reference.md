# CLI Reference

## Server

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

## Client

```bash
# Connect interactively (Ctrl+C to disconnect)
auth-vpn connect <server-ip>:7777 --token <token>

# Connect using a saved profile
auth-vpn connect staging

# Background mode (survives terminal close)
auth-vpn connect staging --background

# Background + auto-reconnect on unexpected drop (exponential backoff, max 2 min)
auth-vpn connect staging --background --reconnect

# Wait until server is reachable before connecting (useful in CI)
auth-vpn connect staging --background --wait

# Route additional CIDRs through the tunnel (e.g. k8s service subnet)
auth-vpn connect staging --route 10.0.0.0/16

# GitHub Actions: auto-mint a unique ephemeral token per job (reads AUTH_VPN_API_KEY env var)
# Run with & so the step doesn't block; use `auth-vpn disconnect` at the end to revoke the token
auth-vpn connect $VPN_HOST --github-action --forward 5432:localhost:5432 &

# Check tunnel status
auth-vpn status

# Disconnect background tunnel
auth-vpn disconnect
```

## Proxy mode (no TUN device, no root required)

Use `--forward` instead of a TUN tunnel. auth-vpn binds a local port and forwards all TCP traffic through the encrypted tunnel to the remote host:port. Works anywhere — Docker containers, Render, Railway, Cloud Run, CI runners.

```bash
# Forward local 5432 → postgres on the VM, local 6379 → redis on the VM
auth-vpn connect <server-ip>:7777 --token <token> \
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

Point your app at `127.0.0.1:<localPort>` — it connects as if the service is local. No routing table changes, no kernel privileges needed.

> **Common mistake — port already in use locally**
> If you run the same service on your machine (e.g. a local Postgres on `5432`), binding `--forward 5432:...` will fail because that port is taken. Use a different local port instead:
> ```bash
> --forward 15432:10.8.0.1:5432   # local 15432 → remote 5432
> ```
> Then point your app at `127.0.0.1:15432`.

## Profiles

```bash
auth-vpn profile save staging --host <server-ip>:7777 --token <token>
auth-vpn profile list
```

## Hub

```bash
# Start the hub dashboard (default: http://127.0.0.1:9200)
auth-vpn hub serve

# Custom port or bind address
auth-vpn hub serve --port 9300
auth-vpn hub serve --port 9300 --bind 0.0.0.0
```

Config is stored at `~/.auth-vpn/hub.yaml`. Add and remove servers from the UI — no manual YAML editing needed. See [hub.md](hub.md) for full documentation.

## Other

```bash
# Check installed version
auth-vpn version

# Update binary in-place (restarts systemd service on server)
sudo auth-vpn update
```
