#!/bin/sh
set -e

if [ -z "$VPN_HOST" ]; then
  echo "Error: VPN_HOST is required (e.g. 20.98.154.174:7777)"
  exit 1
fi

if [ -z "$VPN_TOKEN" ]; then
  echo "Error: VPN_TOKEN is required"
  exit 1
fi

# Build --forward args from VPN_FORWARDS (comma-separated)
# e.g. VPN_FORWARDS=5432:10.8.0.1:5432,3306:10.8.0.1:3306
FORWARD_ARGS=""
if [ -n "$VPN_FORWARDS" ]; then
  for rule in $(echo "$VPN_FORWARDS" | tr ',' ' '); do
    FORWARD_ARGS="$FORWARD_ARGS --forward $rule"
  done
fi

EXTRA_ARGS=""
if [ "${VPN_INSECURE:-false}" = "true" ]; then
  EXTRA_ARGS="$EXTRA_ARGS --insecure"
fi

echo "auth-vpn connecting to $VPN_HOST ..."
exec auth-vpn connect "$VPN_HOST" --token "$VPN_TOKEN" $FORWARD_ARGS --reconnect $EXTRA_ARGS
