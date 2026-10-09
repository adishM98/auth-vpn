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
- `auth-vpn` client on your laptop, **v2.6.0 or newer**. Older clients connect but ignore the routes and DNS the server pushes. Already installed? Run `auth-vpn update`.
  ```bash
  curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash
  ```

No image build needed: the manifests use the published image `docker.io/adishm98/auth-vpn:latest` (linux/amd64). Dashboard-over-VPN in labeled mode needs a server image of v2.8.0 or newer.

---

## Step 1 — Deploy

```bash
kubectl apply -k "github.com/adishM98/auth-vpn/k8s?ref=main"
```

This creates the `auth-vpn` namespace, a ServiceAccount with read-only access to Services (used by labeled expose mode, below), the PVC, the Deployment and the LoadBalancer Service. From a clone: `kubectl apply -k k8s/`. For another namespace, write a small overlay that sets `namespace:` and points at this directory.

The PVC (1 GiB) persists the TLS cert, tokens and `server.yaml` across pod restarts — your laptop won't be asked to re-trust the server and tokens stay valid.

> **Building your own image?** From the repo root, always pass `--platform`. A plain `docker build` on an Apple Silicon Mac produces an arm64 image, which crashes on amd64 nodes with `exec format error`:
> ```bash
> docker buildx build -f docker/Dockerfile --platform linux/amd64 \
>   --build-arg VERSION=v2.9.0 -t <registry>/auth-vpn:v2.9.0 --push .
> # ARM node pools too: --platform linux/amd64,linux/arm64
> ```
> Then point the Deployment at it with an `images:` entry in your overlay (see [Recommended: a small overlay](#recommended-a-small-overlay)).

---

## Step 2 — Get the admin token and LoadBalancer IP

```bash
kubectl logs -n auth-vpn deploy/auth-vpn     # look for: auth-vpn connect <IP>:7777 --token <TOKEN>
kubectl get svc -n auth-vpn auth-vpn          # wait for EXTERNAL-IP (30–90 s)
```

The admin token is printed **only on the first boot**. Store it in a password manager, and don't paste it into chats or tickets. For each teammate, create a personal token in the dashboard (see [Web dashboard](#web-dashboard)), then revoke `admin`.

Lost it? Create a new one from the dashboard. Alternatively, run `kubectl exec -n auth-vpn deploy/auth-vpn -- auth-vpn server tokens add --name laptop` followed by `kubectl rollout restart -n auth-vpn deploy/auth-vpn`: the CLI edits the token file, which the running server only re-reads on restart.

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
| auth-vpn's own dashboard | `http://10.8.0.1:9100/ui` over the VPN | still reachable at `http://10.8.0.1:9100/ui` over the VPN (port 9100 only; API key still required) |

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

Once the tunnel is confirmed working, convert public-facing services to ClusterIP to remove their Azure/GCP/AWS public IPs. The `nodePort` has to be removed in the same patch, or the API server rejects the change:

```bash
kubectl patch svc <service-name> -n <namespace> --type=json --dry-run=server -p '[
  {"op":"replace","path":"/spec/type","value":"ClusterIP"},
  {"op":"remove","path":"/spec/ports/0/nodePort"}]'
# looks right → run again without --dry-run=server
```

After this, those services are only reachable through the auth-vpn tunnel. The ClusterIP and DNS name don't change.

> - **Verify the tunnel works before removing public IPs.**
> - **Check who uses the old public IP** (bookmarks, alert links, external monitors). Unless it was reserved, you won't get the same IP back.
> - **Update the Service's source manifest too.** If it was created with `kubectl apply -f`, re-applying an unchanged file makes the Service public again with a new IP.

---

## Token management

The dashboard (below) creates and revokes tokens **live**. The CLI commands edit the token file in the pod, so the running server only picks them up after `kubectl rollout restart -n auth-vpn deploy/auth-vpn`.

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

The dashboard shows live connected clients, traffic, tokens, IP whitelist, SSH keys and direct forwards. Port `9100` speaks **both `http://` and `https://`**. Plain http is served only over private paths (localhost and the VPN). From anywhere else, http redirects to https, so the API key never crosses the internet unencrypted.

| How you reach it | URL | Needs |
|---|---|---|
| **Over the VPN** (recommended) | `http://10.8.0.1:9100/ui` | connected client. Works in labeled mode too (server v2.8.0+) |
| Port-forward | `kubectl port-forward -n auth-vpn deploy/auth-vpn 9100:9100` → `http://localhost:9100/ui` | cluster access |
| Public LoadBalancer port | `https://<LB-IP>:9100/ui` | port 9100 left on the Service (the default). Self-signed cert warning |

The stock `k8s/service.yaml` publishes 9100 on the LoadBalancer. Since the dashboard is reachable over the VPN, we recommend removing it so only the tunnel port is public (see the overlay below).

Paste the API key into the page when it asks, rather than adding `?key=` to the URL: URLs end up in browser history and screenshots. Get the key with:
```bash
kubectl exec -n auth-vpn deploy/auth-vpn -- grep api_key /etc/auth-vpn/server.yaml
```

---

## Recommended: a small overlay

Keep cluster-specific settings in your own kustomization instead of editing this repo's manifests. This one:
- pins the manifests and image to a release
- turns labeled mode on from the first pod, so nothing is reachable until you label it
- keeps the dashboard off the public LoadBalancer

```yaml
# ~/auth-vpn-<cluster>/kustomization.yaml
resources:
  - github.com/adishM98/auth-vpn/k8s?ref=v2.9.0
images:
  - name: docker.io/adishm98/auth-vpn
    newTag: v2.9.0            # tag must exist in the registry
patches:
  - target: {kind: Deployment, name: auth-vpn}
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/env/-
        value: {name: AUTH_VPN_EXPOSE, value: labeled}
  - target: {kind: Service, name: auth-vpn}
    patch: |-
      - op: test
        path: /spec/ports/1/name
        value: dashboard
      - op: remove
        path: /spec/ports/1
```

```bash
kubectl kustomize ~/auth-vpn-<cluster> | less              # preview
kubectl apply -k ~/auth-vpn-<cluster> --dry-run=server     # validate, nothing saved
kubectl apply -k ~/auth-vpn-<cluster>
```

---

## Upgrading

```bash
# bump ?ref= and newTag in your overlay, then:
kubectl apply -k ~/auth-vpn-<cluster>
# or, when tracking :latest:
kubectl rollout restart -n auth-vpn deploy/auth-vpn
kubectl rollout status -n auth-vpn deploy/auth-vpn
```

- **Downtime:** the Deployment uses `strategy: Recreate`, because its disk can only attach to one node at a time. Expect a 10–20 s gap. Connected clients reconnect on their own.
- **Kept across upgrades:** tokens, the TLS cert and the API key live on the PVC, so nobody re-trusts the server.
- **Install output only appears on first boot:** first-boot output (token, connect hint) only shows on a fresh PVC.
- Prefer a pinned tag over `latest`. With `imagePullPolicy: Always`, any pod restart pulls whatever `latest` currently is.

---

## Security notes

- **Capabilities** — the pod needs `NET_ADMIN` and `NET_RAW` to create the TUN interface and set iptables rules. These are the minimum required; do not add `privileged: true` unless troubleshooting.
- **TUN device** — `/dev/net/tun` is mounted from the host node. AKS/GKE/EKS nodes have the `tun` module loaded by default.
- **Single public IP** — only port `7777` (the tunnel) needs to be reachable from outside. Keep `9100` (dashboard) off the LoadBalancer and use `http://10.8.0.1:9100/ui` over the VPN.
- **Labeled mode is the access boundary when NetworkPolicy isn't enforced.** Many clusters (e.g. AKS with `networkPolicy: none`) store NetworkPolicies but don't enforce them. Labeled mode limits tunnel users inside the auth-vpn pod itself.
- **Policy engines (Gatekeeper / Azure Policy / Kyverno):** auth-vpn needs things restrictive baselines flag: `NET_ADMIN`/`NET_RAW`, the `/dev/net/tun` host path, running as root, and its ServiceAccount token (labeled mode lists Services with it).
  - In audit (`dryrun`) mode it just shows as non-compliant.
  - In `deny` mode the pod is rejected. Exclude the `auth-vpn` namespace in the policy assignment.
  - Check before deploying: `kubectl get constraints` (Gatekeeper) shows each policy's `ENFORCEMENT-ACTION`.
- **Tokens** — one per person. Revoke `admin` after handing out personal tokens. Revoked tokens stop working immediately (dashboard), and **Kick** drops an active session.
- **PVC** — the 1 GiB volume holds certs and tokens. Back it up or treat it as ephemeral (tokens can be regenerated; certs will be re-trusted on next connect).

---

## Troubleshooting

**Quick reference**

| Symptom | Cause | Fix |
|---|---|---|
| Pod `CrashLoopBackOff`, log says `exec format error` | arm64 image on amd64 nodes (built on Apple Silicon without `--platform`) | rebuild with `--platform linux/amd64` |
| Pod `ImagePullBackOff` | image/tag doesn't exist in the registry | `docker buildx imagetools inspect <image:tag>`; create or push the tag |
| Rollout stuck, new pod `ContainerCreating`, `Multi-Attach error` | RollingUpdate with the RWO disk (manifests before #7) | use the current manifests (`strategy: Recreate`) |
| Connected, but no `Route … (pushed by server)` / `DNS` lines in the output | client older than v2.6.0 | `auth-vpn update` |
| A labelled Service is unreachable | client connected before it was labelled | reconnect (routes are pushed at connect time) |
| An unlabelled Service is reachable | labeled mode isn't on (`expose=labeled` missing from the log) | set `AUTH_VPN_EXPOSE=labeled` |
| Log: `expose: list services: 403 Forbidden` | RBAC not applied | `kubectl apply -k` the full kustomization (includes `rbac.yaml`) |
| `http://10.8.0.1:9100/ui` hangs while connected | server older than v2.8.0 in labeled mode | upgrade the image |
| Browser says "Server Not Found" for `*.cluster.local`, but `curl` works | browser cached a failed lookup | Firefox: `about:networking#dns` → Clear DNS Cache, or use the ClusterIP |
| `dig` / `nslookup` say NXDOMAIN, but apps work | they bypass macOS `/etc/resolver` files | test with `dscacheutil -q host -a name <svc>.<ns>.svc.cluster.local` |
| `Trust this server?` again, or `fingerprint mismatch` | PVC was recreated, so there's a new cert | remove the entry from `~/.auth-vpn/known_hosts.yaml`, reconnect |

**Pod stuck in `Pending`**

The PVC may not have bound. Check:
```bash
kubectl describe pvc auth-vpn-data -n auth-vpn
kubectl describe pod -n auth-vpn -l app=auth-vpn
```

If `FailedMount`, your storage class may differ from the default. Check available classes and set `storageClassName` in `k8s/pvc.yaml`:
```bash
kubectl get storageclass
```

**Pod in `CrashLoopBackOff`**
```bash
kubectl logs -n auth-vpn deploy/auth-vpn --previous
```

Common causes:
- `/dev/net/tun` unavailable on the node — check cluster compatibility table above
- iptables permission denied — your namespace PodSecurity profile may be blocking `NET_ADMIN`; check with `kubectl get ns auth-vpn -o jsonpath='{.metadata.labels}'`
- Pod rejected at admission — a policy engine in `deny` mode (see Security notes)
- Port conflict — if `7777` is already in use on the node, set `AUTH_VPN_PORT` in the deployment env and update the Service port to match

**`auth-vpn connect` times out**

- Check the pod is running: `kubectl get pods -n auth-vpn -l app=auth-vpn`
- Confirm the tunnel port is open in the node Network Security Group / firewall rules (default `7777`)

**Tunnel connects but cluster services are unreachable**

1. Check the connect output lists the pushed routes. If it doesn't, see the client version row above. To route manually, pass `--route <service-cidr>`.
2. In labeled mode, check the Service is labelled: `kubectl get svc -A -l auth-vpn.io/expose=true`
3. Run a probe from inside the pod:
   ```bash
   kubectl exec -n auth-vpn deploy/auth-vpn -- wget -qO- http://<cluster-ip>:<port>/health
   ```
   If the pod itself can't reach the service, check NetworkPolicy rules in your cluster.

**Disconnect**
```bash
auth-vpn disconnect          # background mode
Ctrl+C                       # foreground mode
```
