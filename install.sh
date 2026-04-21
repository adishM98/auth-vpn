#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# auth-vpn installer
#
# Usage (one-liner, no Git or Go needed):
#   Server:  curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash -s -- --server
#   Client:  curl -fsSL https://github.com/adishM98/auth-vpn/releases/latest/download/install.sh | sudo bash
#
# Usage (from cloned repo):
#   Dev / QA laptop:       sudo ./install.sh
#   VM / server (root):    sudo ./install.sh --server
#   Custom port:           sudo ./install.sh --server --port=8888
# ─────────────────────────────────────────────────────────────────────────────

BINARY="auth-vpn"
REPO="adishM98/auth-vpn"
INSTALL_DIR="/usr/local/bin"
SERVER_PORT="${TJ_VPN_PORT:-7777}"
MODE="client"

# Allow pinning a specific version: VERSION=v1.2.3 ./install.sh
VERSION="${VERSION:-latest}"

for arg in "$@"; do
  case $arg in
    --server)   MODE="server" ;;
    --port=*)   SERVER_PORT="${arg#*=}" ;;
    --version=*) VERSION="${arg#*=}" ;;
  esac
done

# ── colours ───────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; RESET='\033[0m'
ok()   { echo -e "  ${GREEN}✓${RESET} $*"; }
info() { echo -e "  ${YELLOW}→${RESET} $*"; }
warn() { echo -e "  ${YELLOW}⚠${RESET}  $*"; }
fail() { echo -e "\n  ${RED}✗ $*${RESET}\n" >&2; exit 1; }
bold() { echo -e "${BOLD}$*${RESET}"; }
line() { echo "  ────────────────────────────────────────────"; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"

# ── detect OS + arch ─────────────────────────────────────────────────────────
detect_platform() {
  local os arch
  case "$(uname -s)" in
    Linux)  os="linux"  ;;
    Darwin) os="darwin" ;;
    *)      fail "Unsupported OS: $(uname -s). Only Linux and macOS are supported." ;;
  esac
  case "$(uname -m)" in
    x86_64)        arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)             fail "Unsupported arch: $(uname -m)." ;;
  esac
  echo "${os}-${arch}"
}

# ── download from GitHub releases ────────────────────────────────────────────
download_from_github() {
  local platform="$1"
  local asset="${BINARY}-${platform}"
  local tmp
  tmp="$(mktemp)"

  # Build the download URL
  local url
  if [[ "$VERSION" == "latest" ]]; then
    url="https://github.com/${REPO}/releases/latest/download/${asset}"
  else
    url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  fi

  info "Downloading ${asset} from GitHub releases..." >&2

  if command -v curl &>/dev/null; then
    curl -fsSL --retry 3 "$url" -o "$tmp" \
      || fail "Download failed. Is the version '${VERSION}' published?\n    Check: https://github.com/${REPO}/releases"
  elif command -v wget &>/dev/null; then
    wget -qO "$tmp" "$url" \
      || fail "Download failed. Is the version '${VERSION}' published?\n    Check: https://github.com/${REPO}/releases"
  else
    fail "Neither curl nor wget found. Install one and re-run."
  fi

  chmod +x "$tmp"
  ok "Downloaded ${asset}" >&2
  echo "$tmp"
}

# ── auto-install Go on Linux (fallback for local builds) ─────────────────────
GO_VERSION="1.22.4"

install_go_linux() {
  local arch
  case "$(uname -m)" in
    x86_64)        arch="amd64"  ;;
    arm64|aarch64) arch="arm64"  ;;
    *)             fail "Cannot auto-install Go for arch $(uname -m). Install manually: https://go.dev/dl/" ;;
  esac

  local tarball="go${GO_VERSION}.linux-${arch}.tar.gz"
  local url="https://go.dev/dl/${tarball}"
  local tmp="/tmp/${tarball}"

  info "Downloading Go ${GO_VERSION}..."
  curl -fsSL "$url" -o "$tmp"

  info "Installing Go to /usr/local/go..."
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmp"
  rm -f "$tmp"

  export PATH="/usr/local/go/bin:$PATH"

  local profile_line='export PATH="/usr/local/go/bin:$PATH"'
  for rc in /etc/profile.d/go.sh "$HOME/.bashrc" "$HOME/.profile"; do
    if [[ -f "$rc" || "$rc" == /etc/profile.d/go.sh ]]; then
      grep -qF "$profile_line" "$rc" 2>/dev/null || echo "$profile_line" >> "$rc"
      break
    fi
  done

  ok "Go ${GO_VERSION} installed → $(go version)"
}

ensure_go() {
  if command -v go &>/dev/null; then
    ok "Go found: $(go version | awk '{print $3}')"
    return
  fi
  for go_bin in /usr/local/go/bin/go /usr/bin/go; do
    if [[ -x "$go_bin" ]]; then
      export PATH="$(dirname "$go_bin"):$PATH"
      ok "Go found: $(go version | awk '{print $3}')"
      return
    fi
  done
  if [[ "$(uname -s)" == "Linux" ]]; then
    warn "Go not found — installing Go ${GO_VERSION} automatically..."
    [[ $EUID -ne 0 ]] && fail "Auto-installing Go requires root. Re-run with sudo."
    install_go_linux
    return
  fi
  fail "Go is not installed. Install it with one of:
    brew install go
    or download from https://go.dev/dl/
  Then re-run: ./install.sh"
}

# ── build from source (fallback when run from cloned repo) ───────────────────
build_from_source() {
  local platform="$1"
  info "Building auth-vpn from source..." >&2

  if ! command -v git &>/dev/null; then
    warn "git not found — attempting to install..." >&2
    if command -v apt-get &>/dev/null; then
      apt-get install -y -q git
    elif command -v yum &>/dev/null; then
      yum install -y -q git
    elif command -v dnf &>/dev/null; then
      dnf install -y -q git
    else
      fail "Cannot install git automatically. Install it manually and re-run."
    fi
    ok "git installed" >&2
  fi

  mkdir -p "${SCRIPT_DIR}/dist"

  local goos="${platform%-*}"
  local goarch="${platform#*-}"

  (
    cd "$SCRIPT_DIR"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
      go build -ldflags="-s -w -X main.Version=${VERSION}" \
      -o "dist/${BINARY}-${platform}" \
      ./cmd
  )

  ok "Build complete: dist/${BINARY}-${platform}" >&2
  echo "${SCRIPT_DIR}/dist/${BINARY}-${platform}"
}

# ── resolve binary ────────────────────────────────────────────────────────────
#
# Priority order:
#   1. Pre-built binary sitting next to install.sh (put there by make deploy)
#   2. Download from GitHub releases            ← primary path for curl installs
#   3. Build from source (cloned repo + Go)     ← developer fallback
#
resolve_binary() {
  local platform="$1"

  # 1. Pre-built binary already on disk (make deploy copies it here)
  local candidates=(
    "${SCRIPT_DIR}/${BINARY}-${platform}"
    "${SCRIPT_DIR}/${BINARY}"
    "${SCRIPT_DIR}/dist/${BINARY}-${platform}"
    "${SCRIPT_DIR}/dist/${BINARY}"
  )
  for f in "${candidates[@]}"; do
    if [[ -f "$f" && -x "$f" ]]; then
      ok "Using pre-built binary: $(basename "$f")" >&2
      echo "$f"
      return
    fi
  done

  # 2. Download from GitHub releases
  #    Works whether running via curl or from a cloned repo with no binary yet.
  if download_binary="$(download_from_github "$platform" 2>/dev/null)"; then
    echo "$download_binary"
    return
  fi

  # 3. Fall back to building from source (needs Go — auto-installed on Linux)
  warn "GitHub download failed — falling back to build from source" >&2
  ensure_go
  build_from_source "$platform"
}

# ── install binary to /usr/local/bin ─────────────────────────────────────────
install_binary() {
  local src="$1"
  local dest="${INSTALL_DIR}/${BINARY}"

  if [[ -w "$INSTALL_DIR" ]]; then
    cp "$src" "$dest"
    chmod +x "$dest"
  elif command -v sudo &>/dev/null; then
    # Not writable as current user — try sudo (prompts via /dev/tty, works in pipes).
    sudo cp "$src" "$dest" \
      || fail "sudo cp failed. Cannot install to ${INSTALL_DIR}."
    sudo chmod +x "$dest"
  else
    fail "Cannot write to ${INSTALL_DIR} and sudo is not available.\n    Install manually: cp $src /usr/local/bin/auth-vpn"
  fi

  ok "Installed → ${dest}"
}

# ── server setup ──────────────────────────────────────────────────────────────
setup_server() {
  [[ $EUID -ne 0 ]] && fail "Server setup requires root. Run:\n    curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sudo bash -s -- --server"

  echo ""
  bold "Configuring auth-vpn server..."
  echo ""

  "${INSTALL_DIR}/${BINARY}" server install --port "$SERVER_PORT"

  if command -v systemctl &>/dev/null; then
    echo ""
    info "Enabling systemd service..."
    systemctl daemon-reload
    systemctl enable auth-vpn
    systemctl start auth-vpn
    ok "Service started — auth-vpn running on port ${SERVER_PORT}"
  else
    echo ""
    warn "systemd not found. Start the server manually:"
    echo ""
    echo "    sudo auth-vpn server start --port ${SERVER_PORT}"
  fi
}

# ── client usage hint ─────────────────────────────────────────────────────────
print_client_usage() {
  echo ""
  bold "auth-vpn installed!"
  line
  echo ""
  echo "  Connect to a server:"
  echo "    auth-vpn connect <host>:${SERVER_PORT} --token <token>"
  echo ""
  echo "  Save a profile (run once, never type token again):"
  echo "    auth-vpn profile save staging \\"
  echo "      --host <host>:${SERVER_PORT} --token <token>"
  echo "    auth-vpn connect staging"
  echo ""
  echo "  Background mode (for VMs / CI):"
  echo "    auth-vpn connect staging --background --wait"
  echo ""
  echo "  Disconnect:"
  echo "    auth-vpn disconnect"
  echo ""
  line
}

# ── main ──────────────────────────────────────────────────────────────────────
main() {
  echo ""
  bold "  auth-vpn installer"
  line
  echo ""

  # Server mode always requires root; fail early with the correct command.
  if [[ "$MODE" == "server" && $EUID -ne 0 ]]; then
    fail "Server install requires root. Run:\n    curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sudo bash -s -- --server"
  fi

  local platform
  platform="$(detect_platform)"
  ok "Platform: ${platform}"

  local src
  src="$(resolve_binary "$platform")"

  install_binary "$src"

  if [[ "$MODE" == "server" ]]; then
    setup_server
  else
    print_client_usage
  fi
}

main "$@"
