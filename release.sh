#!/usr/bin/env bash
set -euo pipefail

# Usage: ./release.sh <version>   e.g. ./release.sh 1.1.1
VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: ./release.sh <version>"
  echo "  e.g. ./release.sh 1.1.1"
  exit 1
fi

# Strip leading 'v' if provided
VERSION="${VERSION#v}"

BINARY="auth-vpn"
REPO="adishM98/auth-vpn"
DIST="dist"
LDFLAGS="-ldflags=-s -w -X main.Version=v${VERSION}"

echo "→ Releasing auth-vpn v${VERSION}"

# ── 1. Bump VERSION in Makefile ───────────────────────────────────────────────
sed -i.bak "s/^VERSION ?= .*/VERSION ?= ${VERSION}/" Makefile && rm -f Makefile.bak
echo "  ✓ Makefile VERSION → ${VERSION}"

# ── 2. Build all platforms ────────────────────────────────────────────────────
mkdir -p "$DIST"

echo "  Building linux/amd64..."
GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v${VERSION}" -o "${DIST}/${BINARY}-linux-amd64"   ./cmd

echo "  Building darwin/amd64..."
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v${VERSION}" -o "${DIST}/${BINARY}-darwin-amd64"  ./cmd

echo "  Building darwin/arm64..."
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v${VERSION}" -o "${DIST}/${BINARY}-darwin-arm64"  ./cmd

echo "  Building windows/amd64..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v${VERSION}" -o "${DIST}/${BINARY}-windows-amd64.exe" ./cmd

echo "  ✓ All binaries built"

# ── 3. Publish GitHub release ─────────────────────────────────────────────────
if ! command -v gh &>/dev/null; then
  echo "Error: gh CLI not found. Install from https://cli.github.com"
  exit 1
fi

gh release create "v${VERSION}" \
  --title "auth-vpn v${VERSION}" \
  --notes "## Installation

**Server (on VM with containers):**
\`\`\`bash
curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sudo bash -s -- --server
\`\`\`

**Client (dev/QA laptop or another VM):**
\`\`\`bash
curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sudo bash
\`\`\`

See [INSTALL.md](https://github.com/${REPO}/blob/main/INSTALL.md) for full setup guide." \
  "${DIST}/${BINARY}-linux-amd64" \
  "${DIST}/${BINARY}-darwin-amd64" \
  "${DIST}/${BINARY}-darwin-arm64" \
  "${DIST}/${BINARY}-windows-amd64.exe" \
  install.sh

echo ""
echo "✓ Released v${VERSION}"
echo "  GitHub → https://github.com/${REPO}/releases/tag/v${VERSION}"
