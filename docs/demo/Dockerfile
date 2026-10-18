# Image for the demo recordings. One image plays every part: the stage and
# prod servers, and the "laptop", which also runs VHS to record the terminal.
# Built by docs/demo/env.sh up.
FROM debian:bookworm-slim
ARG TARGETARCH
ARG VHS_VERSION=0.12.1
ARG TTYD_VERSION=1.7.7
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \
      iproute2 iptables curl ca-certificates procps sudo tmux postgresql-client \
      ffmpeg chromium fonts-jetbrains-mono fonts-dejavu >/dev/null \
 && rm -rf /var/lib/apt/lists/*
RUN arch=$([ "$TARGETARCH" = arm64 ] && echo arm64 || echo amd64) \
 && curl -fsSL -o /tmp/vhs.deb "https://github.com/charmbracelet/vhs/releases/download/v${VHS_VERSION}/vhs_${VHS_VERSION}_${arch}.deb" \
 && dpkg -i /tmp/vhs.deb && rm /tmp/vhs.deb \
 && tarch=$([ "$TARGETARCH" = arm64 ] && echo aarch64 || echo x86_64) \
 && curl -fsSL -o /usr/local/bin/ttyd "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.${tarch}" \
 && chmod +x /usr/local/bin/ttyd
# VHS drives a headless browser; point it at Debian's Chromium.
# tmux replaces every non-ASCII character with "_" without a UTF-8 locale.
ENV ROD_BROWSER_BIN=/usr/bin/chromium VHS_NO_SANDBOX=true LANG=C.UTF-8
COPY auth-vpn /usr/local/bin/auth-vpn
