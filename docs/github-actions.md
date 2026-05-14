# GitHub Actions

auth-vpn ships a ready-made GitHub Action. Add two steps to any job — connect and disconnect — and every service on the server VM becomes reachable at `10.8.0.1` during the job.

Tokens are fully automatic: a unique ephemeral token is created per job run and revoked when the job ends. No manual token management, no conflicts between parallel matrix jobs.

---

## Setup

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

The `if: always()` on disconnect ensures the ephemeral token is revoked even if earlier steps fail.

---

## Parallel matrix jobs

The same two secrets work for any number of parallel jobs — each job gets its own token automatically:

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

---

## Docker containers in the workflow

If your job runs `docker-compose up`, those containers can also reach `10.8.0.1` with no changes to your `docker-compose.yaml`. Docker routes container traffic through the host network stack, so the VPN tunnel applies automatically.

---

## Action inputs

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `server` | yes | — | VPN server address, e.g. `203.0.113.10:7777` |
| `api-key` | yes | — | Server API key for ephemeral token generation |
| `api-url` | no | `http://<host>:9100` | Override the API endpoint (use `https://` if the server has TLS certs) |
| `routes` | no | — | Extra CIDRs to route via VPN, comma-separated (e.g. `10.20.0.0/16`) |
| `mode` | no | `tun` | `tun` (full OS routing, needs sudo) or `proxy` (explicit port-forwards, no root) |
| `forwards` | no | — | Proxy mode only: `"5432:10.8.0.1:5432 6379:10.8.0.1:6379"` |
| `version` | no | `latest` | Binary version to download, e.g. `v2.0.2` |

---

## Proxy mode (no sudo)

If your runner doesn't allow sudo, use `mode: proxy` with explicit port forwards:

```yaml
- name: Connect to VPN
  uses: adishM98/auth-vpn@v2
  with:
    server: ${{ secrets.VPN_SERVER }}
    api-key: ${{ secrets.VPN_API_KEY }}
    mode: proxy
    forwards: "5432:10.8.0.1:5432 6379:10.8.0.1:6379"
```

Your app connects to `127.0.0.1:5432` instead of `10.8.0.1:5432`. No TUN device, no root required.

---

## Non-GitHub CI (GitLab, Bitbucket, etc.)

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

## Troubleshooting

**`Error: failed to mint ephemeral token`**
The `api-key` secret doesn't match the `api_key` in `/etc/auth-vpn/server.yaml`. Re-copy the key from the server and update the secret.

**Tunnel connects but `10.8.0.1` is unreachable**
Check that the service is bound to `0.0.0.0` on the VM, not `127.0.0.1`. In Docker Compose:
```yaml
ports:
  - "5432:5432"       # ✅
  # not: "127.0.0.1:5432:5432"
```

**Parallel jobs fail with token conflict**
Each job should use `--github-action` (or the Action, which does this automatically) — not a shared static token. Shared tokens can only be active in one place at a time.
