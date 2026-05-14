#!/bin/sh
set -e

PORT="${AUTH_VPN_PORT:-7777}"

# First-time setup: generate TLS cert, initial admin token, server.yaml.
# On restart the PVC already has these files, so this block is skipped.
if [ ! -f /etc/auth-vpn/server.yaml ]; then
    echo "=== First run: initializing auth-vpn server ==="
    auth-vpn server install --port "$PORT"
    echo ""
    echo ">>> Copy the token above — you need it to connect from your laptop <<<"
    echo ""
fi

# MASQUERADE: rewrite VPN client source IPs (10.8.0.0/24) to the pod IP so
# that replies from ClusterIP services find their way back through the tunnel.
# The -C check makes this idempotent across container restarts.
# Use iptables-legacy when available — more compatible with k8s node configurations
# that use seccomp profiles which block nftables syscalls.
IPT="iptables"
command -v iptables-legacy > /dev/null 2>&1 && IPT="iptables-legacy"

$IPT -t nat -C POSTROUTING -s 10.8.0.0/24 ! -d 10.8.0.0/24 -j MASQUERADE 2>/dev/null || \
    $IPT -t nat -A POSTROUTING -s 10.8.0.0/24 ! -d 10.8.0.0/24 -j MASQUERADE

exec auth-vpn server start
