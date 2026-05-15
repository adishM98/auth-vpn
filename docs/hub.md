# Hub Dashboard

The hub is a single dashboard that manages multiple auth-vpn servers from one place. Instead of switching between each server's dashboard to add users, revoke tokens, or configure forwards, you register all your servers in the hub and manage them through one UI.

Every action you take in the hub (create token, kick client, add forward, etc.) is proxied directly to the selected server's REST API. The hub holds no copy of your managed data — it is a pure reverse proxy backed by a small config file listing your servers.

---

## Quick start

```bash
auth-vpn hub serve
```

Opens `http://127.0.0.1:9200` by default. Add servers from the UI — no config file editing required.

```bash
# Custom port or bind address
auth-vpn hub serve --port 9300
auth-vpn hub serve --port 9300 --bind 0.0.0.0
```

Config is stored at `~/.auth-vpn/hub.yaml` (permissions `0600`).

---

## Adding a server

Click **+ Add Server** in the sidebar. The two-step modal walks you through it:

**Step 1 — Connection details**

| Field | What to enter |
|-------|--------------|
| Name | A short label shown in the sidebar (e.g. `prod-db`, `staging`) |
| URL | Full URL of the server's API, e.g. `https://10.0.0.1:9100` or `http://10.0.0.2:9100` |
| API Key | The `api_key` from that server's `server.yaml` (optional if the server has no key set) |

**Step 2 — TLS fingerprint (HTTPS only)**

For HTTPS servers the hub fetches the server's TLS certificate fingerprint and shows it to you before saving. This is Trust On First Use (TOFU) — the same model as SSH's `known_hosts`. Once you confirm, the fingerprint is stored and all future connections to that server are verified against it instead of relying on a CA chain. Self-signed certificates work fine.

For HTTP servers this step is skipped.

---

## Switching between servers

Click any server name in the left sidebar. The main panel switches to that server's data — the same five management tabs as the per-server dashboard (Clients, Tokens, Whitelist, Forwards, SSH Keys). Any change you make applies to that server.

A status dot next to each server name shows live health:

- Green dot — online, last poll within 30 seconds
- Red dot — unreachable (last error shown on hover)

The hub polls every registered server's `/health` endpoint every **30 seconds** in the background.

---

## Opening a server's native dashboard

Each server entry has a `↗` button that opens that server's own dashboard in a new tab, already authenticated. The API key is injected server-side — it does not appear in your browser's URL bar.

---

## Refresh

The hub auto-refreshes server list status every 30 seconds and the active tab's data every 5 seconds. Use the **Refresh** button in the server view header to fetch immediately — useful after making changes directly on a server's native dashboard.

---

## Hub config file

`~/.auth-vpn/hub.yaml` is written automatically when you add or remove servers through the UI. You can also edit it directly:

```yaml
hub_key: ""          # optional — Bearer token required to access the hub UI/API
servers:
  - name: prod-db
    url: https://10.0.0.1:9100
    api_key: abc123
    tls_fingerprint: SHA256:...   # set after TOFU confirmation; empty for HTTP
  - name: staging
    url: http://10.0.0.2:9100
    api_key: xyz789
```

The file is `0600` — only readable by the current user.

---

## Protecting the hub itself

By default `hub serve` binds to `127.0.0.1` and requires no authentication — safe for local use. To protect it when binding to a non-loopback address, set `hub_key` in the config file:

```yaml
hub_key: your-secret-key
```

With a key set, all requests must include either:
- `Authorization: Bearer your-secret-key` header
- `?key=your-secret-key` query parameter (used by the UI automatically)

---

## Hub API reference

The hub exposes its own API alongside the proxied server APIs.

### Hub management

```
GET  /api/hub/servers           — list registered servers with live status
POST /api/hub/servers           — register a new server
DEL  /api/hub/servers/<name>    — remove a server
POST /api/hub/servers/probe     — fetch TLS fingerprint (used by Add Server modal)
GET  /api/hub/overview          — aggregate stats (total servers, online count, total clients)
GET  /hub/open/<name>           — redirect to that server's native dashboard (authenticated)
```

### Proxied server API

All calls to `/proxy/<server-name>/<path>` are forwarded to the corresponding server:

```
/proxy/prod-db/api/clients      → https://10.0.0.1:9100/api/clients
/proxy/staging/api/tokens       → http://10.0.0.2:9100/api/tokens
```

The hub injects the server's API key into the `Authorization` header automatically. Your browser never handles the server API keys directly.

### Example

```bash
# List servers
curl http://127.0.0.1:9200/api/hub/servers

# List clients on prod-db via the proxy
curl http://127.0.0.1:9200/proxy/prod-db/api/clients

# Revoke a token on staging
curl -X DELETE http://127.0.0.1:9200/proxy/staging/api/tokens/alice
```

---

## Connection lifecycle

- Hub → server connections use TLS 1.3 minimum.
- For servers with a stored `tls_fingerprint`, the hub verifies the certificate fingerprint on every connection (TOFU pinning) instead of CA chain validation. This works correctly with self-signed certificates.
- Per-server `http.Client` instances are cached in memory and reused.
- Health polling uses a context tied to the hub process lifetime — all in-flight polls are cancelled cleanly when the hub stops.

---

## Idle connections

The hub itself has no idle timeout. The per-server HTTP clients have a **15-second request timeout**. Health polling runs on a **30-second interval**.

The underlying auth-vpn tunnel connections (client → server) are managed entirely by the individual servers, not the hub. See the server's idle timeout behaviour: the server reaps connections that have not sent any frame in 90 seconds; the client sends a keepalive ping every 30 seconds, so a healthy connected client is never reaped.
