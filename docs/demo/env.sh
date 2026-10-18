#!/usr/bin/env bash
# Throwaway environment for recording the README demos.
#
#   stage server 203.0.113.10   private network 10.20.0.0/24 (Postgres at .5)
#   prod  server 198.51.100.7   private network 10.30.0.0/24 (Postgres at .5)
#   client container "demo" reaches the private networks only through auth-vpn.
#
# Every address is from a documentation range and every token is minted fresh,
# so nothing real ends up in a recording.
#
#   docs/demo/env.sh up     build and start everything, write docs/demo/.env
#   docs/demo/env.sh down   remove it all
#   docs/demo/env.sh tape   record media/terminal.gif (needs up)
#   docs/demo/env.sh dash   connect the laptop to stage with some traffic, for the dashboard tour
set -euo pipefail
cd "$(dirname "$0")"
P=avdemo

down() {
  docker rm -f $P-stage $P-prod $P-pg-stage $P-pg-prod $P-client >/dev/null 2>&1 || true
  for n in pub-stage pub-prod priv-stage priv-prod; do docker network rm $P-$n >/dev/null 2>&1 || true; done
}

up() {
  down
  arch=$(docker info -f '{{.Architecture}}'); case $arch in aarch64|arm64) ga=arm64 ;; *) ga=amd64 ;; esac
  (cd ../.. && CGO_ENABLED=0 GOOS=linux GOARCH=$ga go build -o docs/demo/auth-vpn ./cmd)
  docker build -q -t $P . >/dev/null

  docker network create --subnet 203.0.113.0/24 $P-pub-stage >/dev/null
  docker network create --subnet 198.51.100.0/24 $P-pub-prod >/dev/null
  docker network create --internal --subnet 10.20.0.0/24 $P-priv-stage >/dev/null
  docker network create --internal --subnet 10.30.0.0/24 $P-priv-prod >/dev/null

  for env in stage prod; do
    net=$([ $env = stage ] && echo 10.20.0 || echo 10.30.0)
    docker run -d --name $P-pg-$env --network $P-priv-$env --ip $net.5 \
      -e POSTGRES_USER=app -e POSTGRES_PASSWORD=app -e POSTGRES_DB=orders postgres:16-alpine >/dev/null
  done

  # stage publishes its dashboard on localhost:19100 for the browser recording
  docker run -d --name $P-stage --hostname stage --cap-add NET_ADMIN --device /dev/net/tun \
    --network $P-pub-stage --ip 203.0.113.10 -p 127.0.0.1:19100:9100 \
    -e AUTH_VPN_PUSH_ROUTES=10.20.0.0/24 $P sleep infinity >/dev/null
  docker network connect --ip 10.20.0.2 $P-priv-stage $P-stage
  docker run -d --name $P-prod --hostname prod --cap-add NET_ADMIN --device /dev/net/tun \
    --network $P-pub-prod --ip 198.51.100.7 \
    -e AUTH_VPN_PUSH_ROUTES=10.30.0.0/24 $P sleep infinity >/dev/null
  docker network connect --ip 10.30.0.2 $P-priv-prod $P-prod

  for env in stage prod; do
    docker exec $P-$env auth-vpn server install --port 7777 >/dev/null
    docker exec $P-$env sh -c 'iptables -t nat -A POSTROUTING -s 10.8.0.0/24 ! -d 10.8.0.0/24 -j MASQUERADE
      (auth-vpn server start > /var/log/auth-vpn.log 2>&1 &)'
  done

  # the "laptop" also records the terminal demo, so it mounts this folder
  docker run -d --name $P-client --hostname laptop --cap-add NET_ADMIN --device /dev/net/tun \
    --network $P-pub-stage --ip 203.0.113.50 -v "$PWD":/demo $P sleep infinity >/dev/null
  docker network connect --ip 198.51.100.50 $P-pub-prod $P-client
  docker exec $P-client useradd -m -s /bin/bash dev
  docker exec $P-client sh -c 'echo "dev ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/dev'
  docker exec $P-client sh -c 'echo "*:*:*:app:app" > /home/dev/.pgpass; chmod 600 /home/dev/.pgpass; chown dev /home/dev/.pgpass'
  docker exec -u dev $P-client sh -c 'printf "set -g status off\nset -g pane-border-style fg=colour240\nset -g pane-active-border-style fg=colour39\n" > ~/.tmux.conf'
  docker exec -u dev $P-client sh -c 'echo "PS1=\"\[\e[1;32m\]dev@laptop\[\e[0m\]:\[\e[1;34m\]~\[\e[0m\]\$ \"" >> ~/.bashrc'

  sleep 4
  for env in stage prod; do
    rows=$([ $env = stage ] && echo "(1,'test-customer',9.99),(2,'qa-bot',0)" || echo "(1,'acme',1200),(2,'globex',875.5),(3,'initech',420)")
    docker exec $P-pg-$env psql -q -U app orders -c \
      "create table orders(id int, customer text, total numeric); insert into orders values $rows;" >/dev/null
  done

  key() { docker exec $P-$1 sed -n 's/^api_key: //p' /etc/auth-vpn/server.yaml; }
  tok() { docker exec $P-$1 curl -sk -H "Authorization: Bearer $(key $1)" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$2\"}" https://127.0.0.1:9100/api/tokens | sed 's/.*"token":"\([^"]*\)".*/\1/'; }
  cat > .env <<ENV
STAGE_KEY=$(key stage)
STAGE_TOKEN=$(tok stage dev-laptop)
PROD_TOKEN=$(tok prod dev-laptop)
ENV
  echo "up: stage 203.0.113.10, prod 198.51.100.7, dashboard https://localhost:19100/ui"
}

dash() {
  docker exec $P-client sh -c 'tmux kill-server 2>/dev/null; pkill -x auth-vpn; sleep 2
    SUDO_USER=dev auth-vpn connect stage --reconnect=false </dev/null >/tmp/dash.log 2>&1 &
    sleep 3; while true; do PGPASSWORD=app psql -q -h 10.20.0.5 -U app orders -c "select * from orders" >/dev/null 2>&1; sleep 1; done' >/dev/null 2>&1 &
  sleep 6
  echo "laptop connected to stage; open https://localhost:19100/ui?key=\$STAGE_KEY"
}

tape() { mkdir -p media; docker exec -w /demo $P-client vhs terminal.tape; }

case "${1:-}" in up) up ;; down) down ;; tape) tape ;; dash) dash ;; *) echo "usage: $0 up|down|tape|dash" >&2; exit 2 ;; esac
