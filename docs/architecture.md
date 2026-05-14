# Architecture

## Component map

```
┌─────────────────────────────────────────────┐
│  VM                                         │
│                                             │
│  ┌──────────────┐    ┌───────────────────┐  │
│  │  auth-vpn    │    │  Docker containers│  │
│  │  server      │    │                   │  │
│  │  :<port>(TLS)│    │  postgres :5432   │  │
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

## Packet flow

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

## Wire protocol

```
[ 4 bytes: payload length ][ 1 byte: message type ][ payload ]

Types: Auth(0x01) AuthOK(0x02) AuthFail(0x03) IPPacket(0x04)
       Ping(0x05) Pong(0x06) Disconnect(0x07)
```

## Key design decisions

- **Single binary** — server and client are the same binary, mode selected by subcommand
- **TLS 1.3 only** — no TLS 1.2, no plaintext fallback
- **Split-tunnel** — only `10.8.0.0/24` (and any `--route` CIDRs) goes through the tunnel; normal internet traffic is unaffected
- **Statically linked** — the released binary has no runtime dependencies; the Docker image adds `iproute2` and `iptables` only for the TUN/iptables setup
- **Token hashing** — tokens are SHA-256 hashed before storage; the raw token is never written to disk on the server
- **TOFU cert** — self-signed TLS cert; client pins on first connection, rejects on mismatch (trust-on-first-use)
