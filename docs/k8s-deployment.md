# Deploying auth-vpn on Kubernetes

Run auth-vpn as a pod inside your cluster so any laptop (or CI runner) can reach every ClusterIP service without exposing public LoadBalancer IPs for each one.

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
- Docker (to build and push the image)
- A container registry your cluster can pull from (ACR, ECR, GCR, Docker Hub, ghcr.io)
- `auth-vpn` client on your laptop:
  ```bash
  curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash
  ```

---

## Step 1 — Build your Docker image

The repo includes a `Dockerfile` at the root. It uses a two-stage build:

- **Build stage** — compiles the Go binary inside `golang:latest` (statically linked, no CGO)
- **Runtime stage** — copies the binary into `debian:bookworm-slim` alongside `iproute2` and `iptables`, which are required for the TUN interface and MASQUERADE rule

Build and push to your registry:

```bash
# Build
docker build -t <your-registry>/auth-vpn:latest .

# (Optional) tag a specific version alongside latest
docker build -t <your-registry>/auth-vpn:v2.2.0 -t <your-registry>/auth-vpn:latest .

# Push
docker push <your-registry>/auth-vpn:latest
```

| Registry | Example tag |
|----------|-------------|
| Azure Container Registry | `myacr.azurecr.io/auth-vpn:latest` |
| Amazon ECR | `123456789.dkr.ecr.us-east-1.amazonaws.com/auth-vpn:latest` |
| Google Artifact Registry | `us-docker.pkg.dev/my-project/my-repo/auth-vpn:latest` |
| Docker Hub | `docker.io/youruser/auth-vpn:latest` |
| GitHub Container Registry | `ghcr.io/youruser/auth-vpn:latest` |

**ACR on AKS** — grant the cluster pull access once:
```bash
az aks update -n <cluster-name> -g <resource-group> --attach-acr <acr-name>
```

---

## Step 2 — Put your image in the deployment manifest

Open `k8s/deployment.yaml` and set the `image:` field to the tag you just pushed:

```yaml
# k8s/deployment.yaml  (line 18)
image: myacr.azurecr.io/auth-vpn:latest
```

The placeholder in the file is:
```
image: <your-registry>/auth-vpn:latest
```

Replace `<your-registry>/auth-vpn:latest` with your actual registry path. This is the only line in the manifests that requires a real value before you can deploy.

---

## Step 3 — Set your namespace

All three manifests default to the `default` namespace. Change this before applying:

```bash
# macOS
sed -i '' 's/namespace: default/namespace: your-namespace/g' k8s/*.yaml

# Linux
sed -i 's/namespace: default/namespace: your-namespace/g' k8s/*.yaml
```

Or edit each file manually — `namespace:` appears in the `metadata` block of each manifest.

---

## Step 4 — Apply the manifests

```bash
kubectl apply -f k8s/pvc.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

The PVC creates a 1 GiB volume that persists across pod restarts:
- TLS certificate and key (`/etc/auth-vpn/tls/`)
- Token store (`/etc/auth-vpn/tokens.yaml`)
- Server config (`/etc/auth-vpn/server.yaml`)

After a pod restart your laptop will not be asked to re-trust the server cert, and all tokens remain valid.

---

## Step 5 — Get the admin token

On first boot auth-vpn generates a self-signed TLS cert and an `admin` token. Retrieve it from the pod logs:

```bash
kubectl logs -n <namespace> deploy/auth-vpn
```

Look for:
```
auth-vpn connect <IP>:7777 --token <TOKEN>
```

**Copy this token.** It is shown once. If you lose it:
```bash
kubectl exec -n <namespace> deploy/auth-vpn -- auth-vpn server tokens add --name laptop
```

---

## Step 6 — Get the LoadBalancer IP

```bash
kubectl get svc auth-vpn -n <namespace>
```

Wait until `EXTERNAL-IP` is populated (typically 30–90 seconds):

```
NAME       TYPE           CLUSTER-IP    EXTERNAL-IP   PORT(S)
auth-vpn   LoadBalancer   10.0.x.x      20.x.x.x      7777:.../TCP, 9100:.../TCP
```

---

## Step 7 — Connect from your laptop

You can route the entire cluster or just the services in a specific namespace. Start with namespace-scoped routing and widen only if needed.

---

**Option A — Namespace-scoped routing (recommended)**

Route only the ClusterIP addresses of services in your namespace. This is the least permissive option — your laptop can only reach what you explicitly list.

Get the ClusterIPs for your namespace:
```bash
kubectl get svc -n <namespace>
```

Then pass each IP as a `/32` route:
```bash
auth-vpn connect <LB-IP>:7777 --token <token> \
  --route 10.0.x.x/32 \
  --route 10.0.y.y/32 \
  --background --reconnect
```

Add or remove `--route` flags as services change. No other namespace is reachable.

---

**Option B — Whole cluster routing**

Routes the entire service CIDR, giving access to every ClusterIP in every namespace. Convenient if you work across many namespaces.

Find your cluster's service CIDR:
```bash
# AKS
az aks show -n <cluster-name> -g <resource-group> --query networkProfile.serviceCidr

# GKE
gcloud container clusters describe <cluster-name> --format='value(servicesIpv4Cidr)'

# EKS / generic
kubectl cluster-info dump | grep -m1 service-cluster-ip-range
```

Then connect:
```bash
auth-vpn connect <LB-IP>:7777 --token <token> --route <service-cidr> --background --reconnect
```

---

**Save as a profile**
```bash
auth-vpn profile save k8s-staging \
  --host <LB-IP>:7777 \
  --token <token>

# Namespace-scoped
auth-vpn connect k8s-staging --route 10.0.x.x/32 --route 10.0.y.y/32 --background --reconnect

# Whole cluster
auth-vpn connect k8s-staging --route <service-cidr> --background --reconnect
```

---

## Step 8 — Access services

Once connected, ClusterIP addresses are reachable directly:

```bash
# Postgres at ClusterIP 10.0.x.x:5432
psql -h 10.0.x.x -p 5432 -U postgres

# Redis
redis-cli -h 10.0.x.x -p 6379

# Any HTTP service
curl http://10.0.x.x:8080/health
```

To find ClusterIP addresses:
```bash
kubectl get svc -n <namespace>
```

---

## Step 9 — Remove public LoadBalancer IPs (optional)

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
kubectl exec -n <namespace> deploy/auth-vpn -- auth-vpn server tokens add --name alice

# One-time token (auto-revokes after first use)
kubectl exec -n <namespace> deploy/auth-vpn -- auth-vpn server tokens add --name alice --one-time

# Expiring token
kubectl exec -n <namespace> deploy/auth-vpn -- auth-vpn server tokens add --name ci --expires 24h

# List active tokens
kubectl exec -n <namespace> deploy/auth-vpn -- auth-vpn server tokens list

# Revoke
kubectl exec -n <namespace> deploy/auth-vpn -- auth-vpn server tokens revoke --name alice
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
kubectl port-forward -n <namespace> deploy/auth-vpn 9100:9100
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

Common cause: `/dev/net/tun` unavailable. Try adding `privileged: true` to `securityContext` as a temporary test.

**`auth-vpn connect` times out**

- Check the pod is running: `kubectl get pods -n <namespace> -l app=auth-vpn`
- Confirm port `7777` is open in the node Network Security Group / firewall rules

**Tunnel connects but cluster services are unreachable**

1. Confirm you passed `--route <service-cidr>` when connecting
2. Run a probe from inside the pod:
   ```bash
   kubectl exec -n <namespace> deploy/auth-vpn -- wget -qO- http://<cluster-ip>:<port>/health
   ```
   If the pod itself can't reach the service, check NetworkPolicy rules in your cluster.

**Disconnect**
```bash
auth-vpn disconnect          # background mode
Ctrl+C                       # foreground mode
```
