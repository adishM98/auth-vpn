# Server Features

## Web dashboard

After `server install`, the dashboard is available at `http://localhost:9100/ui` on the server:

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

VMs, PaaS services, and CI runners with static public IPs can be whitelisted so they connect **without a token**. Changes take effect instantly — no server restart needed.

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
|-----|-----|
| Cloud VMs (Azure, AWS, GCP) | Static public IP — whitelist the IP |
| Render / Railway / PaaS | Whitelist the published outbound CIDR |
| Office with static IP | Whitelist the office public IP |
| Dev/QA laptops | Use tokens — home IPs change too often |

Whitelisted IPs can also connect via the auth-vpn client without `--token`:
```bash
auth-vpn connect <server-ip>:7777   # no --token needed
```

---

## Direct forwards (no auth-vpn client required)

Expose backend service ports directly to whitelisted IPs. The external machine connects with a plain TCP connection — **no auth-vpn binary needed on their side**.

```
Dashboard → Direct Forwards → add listen port + target
```

Example: whitelist `my-vm` (IP `20.x.x.x`), then add a forward:

```
Listen port  →  Target
5432         →  127.0.0.1:5432   (postgres container)
3306         →  127.0.0.1:3306   (mysql container)
```

Your app connects directly:
```
Host: <server-ip>   ← auth-vpn server public IP
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

> Open the forwarded ports in your firewall/NSG for the whitelisted IPs. All other source IPs are dropped immediately.

---

## SSH tunnel

auth-vpn runs an embedded SSH server on port **2222** that supports standard SSH local port forwarding. Any tool that speaks SSH can reach backend services this way — **no auth-vpn binary needed on the client side**.

### Auth methods

| Method | How |
|--------|-----|
| Private key | Generate a keypair from the dashboard → SSH Keys → Generate, or register an existing public key |
| Password | Use any auth-vpn token as the SSH password (any username) |
| System key | Any key in `~/.ssh/authorized_keys` on the server VM automatically works |

### Generate a keypair

1. Open `http://localhost:9100/ui` → **SSH Keys** tab
2. Click **Generate Keypair**, enter a name
3. Copy the private key — shown once, never stored server-side
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

## ACL rules

Create `/etc/auth-vpn/acl.yaml` to restrict what each device can reach at the packet level:

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
GET  /api/status                  — server health + active client count
GET  /api/clients                 — list of connected devices
GET  /api/probe?host=IP&port=N    — verify a host:port is reachable via VPN
GET  /api/whitelist               — list whitelisted IPs
POST /api/whitelist               — add an IP or CIDR
DEL  /api/whitelist/<name>        — remove a whitelisted entry
GET  /api/forwards                — list direct forwards
POST /api/forwards                — add a direct forward
DEL  /api/forwards/<port>         — remove a direct forward
GET  /api/ssh-keys                — list registered SSH keys
POST /api/ssh-keys                — register a public key
POST /api/ssh-keys/generate       — generate a keypair
DEL  /api/ssh-keys/<name>         — remove a key
```

### Clients

```bash
# List connected clients
curl http://localhost:9100/api/clients

# Force-disconnect a client by name (frees the token immediately)
curl -X DELETE http://localhost:9100/api/clients/dev-alice
```

### Authentication

Protect all API endpoints with an API key (set in `server.yaml` or via `--api-key` flag):

```bash
curl http://localhost:9100/api/clients \
  -H 'Authorization: Bearer <api-key>'
```
