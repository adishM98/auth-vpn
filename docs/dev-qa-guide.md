# auth-vpn — Dev & QA Guide

![auth-vpn: outside, the doorman, and your private network](diagrams/avpn-big-picture.svg)

> **What is this?** auth-vpn lets you securely access private databases and services running on a company VM — from your laptop, without exposing anything to the internet.
>
> **Your internet speed is not affected.** Only traffic to internal services (10.8.0.1) goes through the tunnel. Everything else — browsing, Slack, downloads — uses your normal connection.

---

## Before you start

You need two things from your infra/admin team:

| What | Example | Where to get it |
|------|---------|-----------------|
| **Server address** | `203.0.113.10:7777` | Ask your admin |
| **Your token** | `a1b2c3d4e5f6...` | Ask your admin (see below) |

> **How to request a token** — Ping your infra team with your name and ask them to run:
> ```bash
> auth-vpn server tokens add --name "your-name"
> ```
> They'll share the token with you. Keep it private — it's yours alone. Anyone holding it can connect as you (even at the same time), so tell the admin straight away if it leaks and they'll revoke it.

---

## Step 1 — Install

```bash
curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash
```

---

## Step 2 — Connect

```bash
sudo auth-vpn connect <server-address> --token <your-token>
```

Example:
```bash
sudo auth-vpn connect 203.0.113.10:7777 --token a1b2c3d4e5f6
```

> **Tip:** Add `--background` to keep it running after you close the terminal, and `--reconnect` to auto-reconnect if it drops:
> ```bash
> sudo auth-vpn connect 203.0.113.10:7777 --token a1b2c3d4e5f6 --background --reconnect
> ```

---

## Step 3 — Save a profile (do this once)

So you never have to type the token again. The first time you connect with the token (Step 2), auth-vpn asks:

```
Save 203.0.113.10:7777 as a profile, so next time it's just `auth-vpn connect <name>`?
Profile name (Enter to skip): work
```

Type a name. It's saved as soon as the tunnel is up. Missed it? Save it by hand:

```bash
auth-vpn profile add work 203.0.113.10:7777 \
  --token a1b2c3d4e5f6
```

From now on, just run:
```bash
sudo auth-vpn connect work --background --reconnect
```

---

## Step 4 — Access internal services

Once connected, everything on the VM is available at `10.8.0.1`:

| Service | Host | Port |
|---------|------|------|
| PostgreSQL | `10.8.0.1` | `5432` |
| MySQL | `10.8.0.1` | `3306` |
| Redis | `10.8.0.1` | `6379` |
| Any other service | `10.8.0.1` | the port it runs on |

Test it:
```bash
psql -h 10.8.0.1 -U postgres -d your_database
```

---

## Check tunnel status

```bash
auth-vpn status
```

When connected, you'll see:
```
status: connected
  Server       : 203.0.113.10:7777
  Tunnel IP    : 10.8.0.2
  Connected at : 2026-04-30 09:30:00
  Uptime       : 2h15m30s
```

If it says `status: not connected` — run the connect command again.

---

## Disconnect

```bash
# If running in background:
auth-vpn disconnect

# If running in foreground (terminal still open):
Ctrl+C
```

---

## Troubleshooting

**"connection refused" or "token invalid"**
Your token may have expired or been revoked. Request a new one from your admin.

**"already connected" error**
You have another active session using the same token. Disconnect first:
```bash
auth-vpn disconnect
```

**Can't reach 10.8.0.1 after connecting**
Run `auth-vpn status` to confirm the tunnel is up. If it is, check with your admin that the service you're trying to reach is actually running on the VM.

**macOS asks for password on install**
Normal — the installer needs sudo to set up the tunnel interface. Enter your Mac login password.

---

## Quick reference

| Command | What it does |
|---------|--------------|
| `sudo auth-vpn connect <host> --token <tok>` | Connect to VPN |
| `sudo auth-vpn connect <profile>` | Connect using saved profile (switches from any running one) |
| `sudo auth-vpn connect` | Pick a saved profile from a menu |
| `auth-vpn disconnect` | Disconnect |
| `auth-vpn status` | Check if connected |
| `auth-vpn profile add <name> <host> --token <t>` | Save a profile |
| `auth-vpn profile list` | List saved profiles |
| `auth-vpn profile rm <name>` | Delete a profile |
