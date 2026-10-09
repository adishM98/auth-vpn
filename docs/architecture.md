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
│  │  :9100 (API) │    │  mysql    :3306   │  │
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
       ProxyDial(0x08) ProxyOK(0x09) ProxyFail(0x0A) ProxyData(0x0B) ProxyClose(0x0C)
```

`AuthOK` is JSON: `{"client_ip","server_ip","subnet","routes"?,"dns"?}`. `routes` and `dns` are optional, so older clients ignore them and older servers just don't send them. No protocol version bump is needed.

## Kubernetes: pushed routes and labeled mode

```
auth-vpn pod startup
  ├─ KUBERNETES_SERVICE_HOST (e.g. 10.0.0.1) → guess service CIDR (/16, or /20 for non-private ranges)
  └─ /etc/resolv.conf → kube-dns IP + cluster domain (from the "svc.<domain>" search entry)

client AUTH → server AUTH_OK + routes + dns
  └─ client filters them (IPv4 only, ≥ /8, never covering the server's own IP, multi-label domains only)
     ├─ adds routes via the TUN (+ a /32 for the DNS server)
     └─ split DNS: macOS /etc/resolver/<domain>, Linux resolvectl ~<domain>

labeled mode (AUTH_VPN_EXPOSE=labeled)
  ├─ every 30 s: list Services with label auth-vpn.io/expose=true (ServiceAccount, list-only RBAC)
  ├─ pushed routes = one /32 per labelled ClusterIP
  ├─ every client packet: allowed only to labelled ClusterIP:port, kube-dns:53, or the server's own API port
  ├─ proxy dials: name resolved in the pod; the checked IP is the one dialed
  └─ API unreachable → keep last list; no list yet → only DNS (fail closed)
```

Precedence for what's pushed: `server.yaml` (`push_routes` / `push_dns`) → `AUTH_VPN_PUSH_ROUTES` env → auto-detection. `no_push: true` disables all of it.

## HTTP API port

`:9100` serves `https://` and `http://` on the same port. The first byte of each connection picks the protocol: `0x16` is a TLS handshake. Plain http is served only to localhost and the VPN subnet. Anything else gets a 308 redirect to https.

## Key design decisions

- **Single binary** — server and client are the same binary, mode selected by subcommand
- **TLS 1.3 only** for the tunnel — no TLS 1.2, no plaintext fallback (the API port additionally accepts plain http from localhost/VPN only)
- **Split-tunnel** — only `10.8.0.0/24`, any `--route` CIDRs and server-pushed routes go through the tunnel; normal internet traffic is unaffected
- **Enforcement in the server, not the cluster** — labeled mode filters packets inside auth-vpn, so it works where NetworkPolicy isn't enforced
- **Statically linked** — the released binary has no runtime dependencies; the Docker image adds `iproute2` and `iptables` only for the TUN/iptables setup
- **Token hashing** — tokens are SHA-256 hashed before storage; the raw token is never written to disk on the server
- **TOFU cert** — self-signed TLS cert; client pins on first connection, rejects on mismatch (trust-on-first-use)
