BINARY  = auth-vpn
REPO    = adishM98/auth-vpn
VERSION ?= 1.1.0
DIST    = dist
LDFLAGS = -ldflags="-s -w -X main.Version=v$(VERSION)"

.PHONY: build-linux build-mac-intel build-mac-arm build-windows build-all \
        install install-server deploy deploy-client \
        release clean

# ── build ─────────────────────────────────────────────────────────────────────

build-linux:
	@mkdir -p $(DIST)
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 \
	  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-amd64  ./cmd

build-mac-intel:
	@mkdir -p $(DIST)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
	  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-amd64 ./cmd

build-mac-arm:
	@mkdir -p $(DIST)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
	  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-arm64 ./cmd

build-windows:
	@mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
	  go build $(LDFLAGS) -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd

build-all: build-linux build-mac-intel build-mac-arm build-windows

# ── local install (current machine) ──────────────────────────────────────────

## Install client binary on this machine (Mac or Linux)
install:
	@bash install.sh

## Install + configure server on this machine (run as root)
install-server:
	@sudo bash install.sh --server

# ── remote deploy (DevOps: deploy to a VM in one command) ─────────────────────
#
# Usage:
#   make deploy VM=azureuser@20.98.154.174
#   make deploy VM=azureuser@20.98.154.174 PORT=8888
#   make deploy VM=azureuser@20.98.154.174 SSH_KEY=~/.ssh/my_key

VM      ?=
PORT    ?= 7777
SSH_KEY ?=

SSH_OPTS = $(if $(SSH_KEY),-i $(SSH_KEY),)

## Deploy + configure as SERVER on a remote Linux VM (any VM with containers)
deploy: build-linux
ifndef VM
	$(error VM is required. Usage: make deploy VM=user@host)
endif
	@echo "→ Copying binary + installer to $(VM)..."
	ssh $(SSH_OPTS) $(VM) "mkdir -p ~/auth-vpn-install"
	scp $(SSH_OPTS) $(DIST)/$(BINARY)-linux-amd64 install.sh $(VM):~/auth-vpn-install/
	@echo "→ Running server install on $(VM)..."
	ssh $(SSH_OPTS) $(VM) "cd ~/auth-vpn-install && sudo bash install.sh --server --port=$(PORT)"
	@echo "✓ Done. tj-vpn server is running on $(VM):$(PORT)"

# ── GitHub release ────────────────────────────────────────────────────────────
#
# Usage:
#   make release           → create v0.1.0 release with all binaries + install.sh
#   make release VERSION=v1.2.3
#
# Requires: gh CLI authenticated (gh auth login)

release: build-all
	@command -v gh >/dev/null 2>&1 || { echo "Error: gh CLI not found. Install from https://cli.github.com"; exit 1; }
	gh release create v$(VERSION) \
	  --title "auth-vpn v$(VERSION)" \
	  --notes "## Installation\n\n**Server (on VM with containers):**\n\`\`\`bash\ncurl -fsSL https://github.com/$(REPO)/releases/latest/download/install.sh | sudo bash -s -- --server\n\`\`\`\n\n**Client (dev/QA laptop or another VM):**\n\`\`\`bash\ncurl -fsSL https://github.com/$(REPO)/releases/latest/download/install.sh | bash\n\`\`\`\n\nSee [INSTALL.md](https://github.com/$(REPO)/blob/main/INSTALL.md) for full setup guide." \
	  $(DIST)/$(BINARY)-linux-amd64 \
	  $(DIST)/$(BINARY)-darwin-amd64 \
	  $(DIST)/$(BINARY)-darwin-arm64 \
	  $(DIST)/$(BINARY)-windows-amd64.exe \
	  install.sh
	@echo "✓ Released v$(VERSION)"
	@echo "  GitHub → https://github.com/$(REPO)/releases/tag/v$(VERSION)"

## Deploy as CLIENT on a remote Linux VM (connects to a server, does not become one)
deploy-client: build-linux
ifndef VM
	$(error VM is required. Usage: make deploy-client VM=user@host)
endif
	@echo "→ Copying binary + installer to $(VM)..."
	ssh $(SSH_OPTS) $(VM) "mkdir -p ~/auth-vpn-install"
	scp $(SSH_OPTS) $(DIST)/$(BINARY)-linux-amd64 install.sh $(VM):~/auth-vpn-install/
	@echo "→ Running client install on $(VM)..."
	ssh $(SSH_OPTS) $(VM) "cd ~/auth-vpn-install && bash install.sh"
	@echo "✓ Done. Now SSH in and run: tj-vpn connect <host>:<port> --token <token>"

# ── clean ─────────────────────────────────────────────────────────────────────

clean:
	rm -rf $(DIST)
