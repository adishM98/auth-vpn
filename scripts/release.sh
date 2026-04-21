#!/usr/bin/env bash
set -euo pipefail

# ── helpers ───────────────────────────────────────────────────────────────────

red()   { printf "\033[0;31m%s\033[0m\n" "$*"; }
green() { printf "\033[0;32m%s\033[0m\n" "$*"; }
bold()  { printf "\033[1m%s\033[0m\n" "$*"; }

# ── repo root ─────────────────────────────────────────────────────────────────

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ── pre-flight checks ─────────────────────────────────────────────────────────

if ! git diff --quiet || ! git diff --cached --quiet; then
  red "Working tree has uncommitted changes. Commit or stash them first."
  exit 1
fi

#── detect current version ────────────────────────────────────────────────────

CURRENT=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)
if [[ -z "$CURRENT" ]]; then
  CURRENT="v0.0.0"
  echo "No existing tags found — starting from $CURRENT"
else
  echo "Current version: $(bold "$CURRENT")"
fi

# Strip leading 'v'
VERSION="${CURRENT#v}"
MAJOR=$(echo "$VERSION" | cut -d. -f1)
MINOR=$(echo "$VERSION" | cut -d. -f2)
PATCH=$(echo "$VERSION" | cut -d. -f3)

# ── choose bump type ──────────────────────────────────────────────────────────

echo ""
echo "Bump type:"
echo "  1) patch  → v$MAJOR.$MINOR.$((PATCH+1))"
echo "  2) minor  → v$MAJOR.$((MINOR+1)).0"
echo "  3) major  → v$((MAJOR+1)).0.0"
echo "  4) custom"
echo ""
read -rp "Choose [1]: " CHOICE
CHOICE="${CHOICE:-1}"

case "$CHOICE" in
  1) NEW_VERSION="$MAJOR.$MINOR.$((PATCH+1))" ;;
  2) NEW_VERSION="$MAJOR.$((MINOR+1)).0" ;;
  3) NEW_VERSION="$((MAJOR+1)).0.0" ;;
  4)
    read -rp "Enter version (without v): " NEW_VERSION
    if ! echo "$NEW_VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
      red "Invalid version format. Use X.Y.Z"
      exit 1
    fi
    ;;
  *)
    red "Invalid choice"
    exit 1
    ;;
esac

NEW_TAG="v$NEW_VERSION"

echo ""
bold "Releasing $NEW_TAG"
echo ""
read -rp "Continue? [y/N]: " CONFIRM
if [[ "$(echo "$CONFIRM" | tr '[:upper:]' '[:lower:]')" != "y" ]]; then
  echo "Aborted."
  exit 0
fi

# ── update Makefile version ───────────────────────────────────────────────────

sed -i.bak "s/^VERSION ?= .*/VERSION ?= $NEW_VERSION/" Makefile
rm -f Makefile.bak

# ── commit + tag + push ───────────────────────────────────────────────────────

git add Makefile
if ! git diff --cached --quiet; then
  git commit -m "chore: release $NEW_TAG"
fi

git tag -a "$NEW_TAG" -m "Release $NEW_TAG"

BRANCH=$(git rev-parse --abbrev-ref HEAD)
git push origin "$BRANCH"
git push origin "$NEW_TAG"

# ── done ──────────────────────────────────────────────────────────────────────

echo ""
green "✓ Tagged and pushed $NEW_TAG"
echo ""
bold "Next steps:"
echo ""
echo "  1. Build all binaries locally:"
echo ""
echo "       make build-all"
echo ""
echo "  2. Go to GitHub and create the release:"
echo ""
echo "       https://github.com/adishM98/auth-vpn/releases/new?tag=$NEW_TAG"
echo ""
echo "  3. Upload these files from dist/:"
echo "       - auth-vpn-linux-amd64"
echo "       - auth-vpn-darwin-amd64"
echo "       - auth-vpn-darwin-arm64"
echo "       - auth-vpn-windows-amd64.exe"
echo "       - install.sh"
echo ""
echo "  4. Click Publish release."
echo ""
echo "  5. Clean up build artifacts:"
echo ""
echo "       make clean"
echo ""
