# Deploying auth-vpn on Kubernetes

Run auth-vpn as a pod inside your cluster so any laptop (or CI runner) can reach every ClusterIP service without exposing public LoadBalancer IPs for each one.

---

## Cluster compatibility

| Cluster type | Supported | Notes |
|---|---|---|
| AKS (standard node pools) | ✅ | Fully supported |
| GKE Standard | ✅ | Fully supported |
| EKS (managed nodes) | ✅ | Fully supported |
| Self-managed (kubeadm etc.) | ✅ | Fully supported |
| AWS Fargate | ❌ | No `/dev/net/tun`, no hostPath volumes |
| GKE Autopilot | ❌ | Capabilities (`NET_ADMIN`) and hostPath blocked |
| Azure Container Instances | ❌ | No TUN device support |

**PodSecurity profile requirement**: auth-vpn needs `NET_ADMIN` and `NET_RAW` capabilities and a hostPath volume for `/dev/net/tun`. This is compatible with the `baseline` and `privileged` PodSecurity profiles, but **not** `restricted`. If your namespace enforces `restricted`, either relax the policy for this namespace or run auth-vpn in a dedicated namespace.

```bash
# Check what PodSecurity policy your namespace enforces
kubectl get ns <namespace> -o jsonpath='{.metadata.labels}'
```

---

## How it works

```
Your Laptop / CI
  │
  │  TLS on port 7777
  ▼
auth-vpn LoadBalancer IP
  │
  │  auth-vpn pod (your namespace)
  │  Pod IP: <Azure/GKE/EKS CNI IP>
  │  TUN interface: 10.8.0.1/24
  │  iptables MASQUERADE active
  │
  ├──► service-a   ClusterIP 10.0.x.x:port
  ├──► service-b   ClusterIP 10.0.x.x:port
  └──► any other ClusterIP service
```

**Why iptables MASQUERADE is needed**

When your laptop sends a packet to a ClusterIP (e.g. `10.0.101.88`), the source IP is your VPN IP (`10.8.0.2`). Kubernetes has no route back to `10.8.0.0/24`, so the reply is dropped.

MASQUERADE rewrites the source IP to the pod's real CNI IP before the packet leaves the pod. The target service replies to the pod, the pod un-NATs it, and the reply travels back through the tunnel to your laptop — transparently.

---

## Prerequisites

- `kubectl` configured for your cluster
- `auth-vpn` client on your laptop:
  ```bash
  curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash
  ```

No image build needed — the manifests use the published image `docker.io/adishm98/auth-vpn:latest` (amd64 + arm64).

---

## Step 1 — Deploy

```bash
kubectl apply -k "github.com/adishM98/auth-vpn/k8s?ref=main"
```

This creates the `auth-vpn` namespace, a ServiceAccount with read-only access to Services (used by labeled expose mode, below), the PVC, the Deployment and the LoadBalancer Service. From a clone: `kubectl apply -k k8s/`. For another namespace, write a small overlay that sets `namespace:` and points at this directory.

The PVC (1 GiB) persists the TLS cert, tokens and `server.yaml` across pod restarts — your laptop won't be asked to re-trust the server and tokens stay valid.

> Building your own image instead? From the repo root: `docker build -f docker/Dockerfile -t <registry>/auth-vpn:tag .`, push it, and set `image:` in `k8s/deployment.yaml`.

---

## Step 2 — Get the admin token and LoadBalancer IP

```bash
kubectl logs -n auth-vpn deploy/auth-vpn     # look for: auth-vpn connect <IP>:7777 --token <TOKEN>
kubectl get svc -n auth-vpn auth-vpn          # wait for EXTERNAL-IP (30–90 s)
```

Lost the token? `kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens add --name laptop`

---

## Step 3 — Connect

```bash
sudo auth-vpn connect <LB-IP>:7777 --token <TOKEN> --background
```

That's it. On connect the server **pushes** two things, auto-detected from inside the pod:

| Pushed | Detected from | Effect on your laptop |
|---|---|---|
| Service CIDR route | `KUBERNETES_SERVICE_HOST` (guess: /16 around it, /20 for non-private ranges like GKE's) | every ClusterIP is routed through the tunnel |
| Split DNS | the pod's `/etc/resolv.conf` (kube-dns IP + cluster domain) | `*.cluster.local` resolves via kube-dns; all other DNS untouched |

The connect output lists what was applied:
```
  Route     : 10.0.0.0/16 → VPN (pushed by server)
  DNS       : *.cluster.local → 10.0.0.10
```

**If the service CIDR guess is wrong** (check the server log line `kubernetes: guessed service CIDR ...` against `kubectl cluster-info dump | grep -m1 service-cluster-ip-range`), set it on the Deployment — comma-separated, can include extra VNet/pod CIDRs:
```bash
kubectl set env -n auth-vpn deploy/auth-vpn AUTH_VPN_PUSH_ROUTES=10.0.0.0/16,10.224.0.0/12
```

**Opt out** — per client with `--no-push-routes` / `--no-push-dns`, or server-wide with `no_push: true` in `/etc/auth-vpn/server.yaml`. Then route manually as before: `--route <cidr>` (repeatable).

**Platform notes**
- macOS writes `/etc/resolver/cluster.local` (removed on disconnect). `dig`/`nslookup` bypass it — test with `dscacheutil -q host -a name <svc>.<ns>.svc.cluster.local`.
- Linux needs systemd-resolved (`resolvectl`); without it you get a warning and IPs still work.
- Proxy mode (`--forward`) resolves names server-side, so `--forward 5432:postgres.myns.svc.cluster.local:5432` works without any of this.

---

## Put only some services behind auth-vpn (labeled mode)

By default, everything the pod can reach is reachable through the tunnel. To expose **only chosen Services**, for example Grafana, while every other Service stays exactly as it is, switch to labeled mode:

```bash
# 1. turn it on (server-wide)
kubectl set env -n auth-vpn deploy/auth-vpn AUTH_VPN_EXPOSE=labeled

# 2. opt Services in, in any namespace
kubectl label svc grafana-lb -n default auth-vpn.io/expose=true
```

What changes:

| | Default (`all`) | `labeled` |
|---|---|---|
| Routes pushed to clients | the whole service CIDR (guessed) | one `/32` per labelled Service |
| What the server forwards (TUN) | anything | only TCP/UDP to a labelled Service's ClusterIP **and** port, plus cluster DNS on 53 |
| Proxy mode (`--forward`) | dials anything | only labelled Services. Names are resolved in the pod, and the checked IP is the one dialed |
| Unlabelled Services | reachable | dropped, even if a client adds `--route` |

- **Enforced by auth-vpn itself.** No NetworkPolicy or policy engine is needed, so it works on any cluster, including ones where `networkPolicy` is `none`.
- **Kept up to date.** The pod re-lists labelled Services every 30 s (via the `auth-vpn-read-services` ClusterRole in `k8s/rbac.yaml`). New labels are enforced within 30 s. Clients get new routes on their next reconnect.
- **Fail closed.** If the API is unreachable, the last good list is kept. Before the first successful list, only cluster DNS is allowed.
- **Not covered:**
  - Headless Services (no ClusterIP)
  - ICMP and non-first IP fragments, which are dropped
  - Direct forwards and the SSH server, which the admin configures separately

Labelling doesn't change the Service itself. If it should no longer be public, make it ClusterIP-only yourself (it keeps the same ClusterIP):

```bash
kubectl patch svc grafana-lb -n default --type=json -p '[
  {"op":"replace","path":"/spec/type","value":"ClusterIP"},
  {"op":"remove","path":"/spec/ports/0/nodePort"}]'
```

Add `--dry-run=server` first to preview it. Then connect and open `http://grafana-lb.default.svc.cluster.local`.

---

## Step 4 — Use services by name

```bash
psql -h postgres.myns.svc.cluster.local -U postgres
redis-cli -h redis.myns.svc.cluster.local
curl http://api.myns.svc.cluster.local:8080/health
```

---

## Step 5 — Remove public LoadBalancer IPs (optional)

Once the tunnel is confirmed working, convert public-facing services to ClusterIP to remove their Azure/GCP/AWS public IPs:

```bash
kubectl patch svc <service-name> -n <namespace> -p '{"spec": {"type": "ClusterIP"}}'
```

After this, those services are only reachable through the auth-vpn tunnel.

> Verify the tunnel works before removing public IPs.

---

## Token management

```bash
# Create a token for a teammate
kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens add --name alice

# One-time token (auto-revokes after first use)
kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens add --name alice --one-time

# Expiring token
kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens add --name ci --expires 24h

# List active tokens
kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens list

# Revoke
kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens revoke --name alice
```

---

## Web dashboard

The dashboard is exposed on port `9100` of the LoadBalancer. Accessible at:
```
http://<LB-IP>:9100/ui
```

It shows live connected clients, traffic counters, token management, and direct forward config.

To avoid exposing port `9100` publicly, remove it from the Service and access it via `kubectl port-forward` instead:
```bash
kubectl port-forward -n auth-vpn deploy/auth-vpn 9100:9100
# then open http://localhost:9100/ui
```

---

## Security notes

- **Capabilities** — the pod needs `NET_ADMIN` and `NET_RAW` to create the TUN interface and set iptables rules. These are the minimum required; do not add `privileged: true` unless troubleshooting.
- **TUN device** — `/dev/net/tun` is mounted from the host node. AKS/GKE/EKS nodes have the `tun` module loaded by default.
- **Single public IP** — only port `7777` (the tunnel) needs to be reachable from outside. Port `9100` (dashboard) should be kept internal or protected by an API key.
- **PVC** — the 1 GiB volume holds certs and tokens. Back it up or treat it as ephemeral (tokens can be regenerated; certs will be re-trusted on next connect).

---

## Troubleshooting

**Pod stuck in `Pending`**

The PVC may not have bound. Check:
```bash
kubectl describe pvc auth-vpn-data -n <namespace>
kubectl describe pod -n <namespace> -l app=auth-vpn
```

If `FailedMount`, your storage class may differ from the default. Check available classes and set `storageClassName` in `k8s/pvc.yaml`:
```bash
kubectl get storageclass
```

**Pod in `CrashLoopBackOff`**
```bash
kubectl logs -n <namespace> deploy/auth-vpn --previous
```

Common causes:
- `/dev/net/tun` unavailable on the node — check cluster compatibility table above
- iptables permission denied — your namespace PodSecurity profile may be blocking `NET_ADMIN`; check with `kubectl get ns <namespace> -o jsonpath='{.metadata.labels}'`
- Port conflict — if `7777` is already in use on the node, set `AUTH_VPN_PORT` in the deployment env and update the Service port to match

**`auth-vpn connect` times out**

- Check the pod is running: `kubectl get pods -n <namespace> -l app=auth-vpn`
- Confirm the tunnel port is open in the node Network Security Group / firewall rules (default `7777`)

**Tunnel connects but cluster services are unreachable**

1. Confirm you passed `--route <service-cidr>` when connecting
2. Run a probe from inside the pod:
   ```bash
   kubectl exec -n auth-vpn deploy/auth-vpn -- wget -qO- http://<cluster-ip>:<port>/health
   ```
   If the pod itself can't reach the service, check NetworkPolicy rules in your cluster.

**Disconnect**
```bash
auth-vpn disconnect          # background mode
Ctrl+C                       # foreground mode
```
