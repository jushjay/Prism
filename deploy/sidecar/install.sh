#!/usr/bin/env bash
# Build and install sub2api-cursor-sidecar as a systemd service.
#
#   sudo ./deploy/sidecar/install.sh               # binary + config + systemd
#   sudo ./deploy/sidecar/install.sh --with-nginx  # also install the nginx site
#
# Existing /etc/sub2api-cursor-sidecar/{sidecar.env,sidecar.yaml} are never overwritten.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SRC_DIR="$ROOT_DIR/deploy/sidecar"
NAME="sub2api-cursor-sidecar"
BIN_DST="/usr/local/bin/$NAME"
CONF_DIR="/etc/$NAME"
UNIT_DST="/etc/systemd/system/$NAME.service"
NGINX_DST="/etc/nginx/conf.d/$NAME.conf"
WITH_NGINX=0

for arg in "$@"; do
  case "$arg" in
    --with-nginx) WITH_NGINX=1 ;;
    *) echo "unknown argument: $arg" >&2; exit 1 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  echo "install.sh must be run as root" >&2
  exit 1
fi

echo "==> Building $NAME"
(cd "$ROOT_DIR/server" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$ROOT_DIR/bin/$NAME" ./cmd/prism)
install -D -m 0755 "$ROOT_DIR/bin/$NAME" "$BIN_DST"

echo "==> Installing config into $CONF_DIR"
install -d -m 0755 "$CONF_DIR"
[[ -e "$CONF_DIR/sidecar.env" ]] || install -m 0600 "$SRC_DIR/sidecar.env.example" "$CONF_DIR/sidecar.env"
[[ -e "$CONF_DIR/sidecar.yaml" ]] || install -m 0644 "$ROOT_DIR/server/configs/sidecar.yaml" "$CONF_DIR/sidecar.yaml"

echo "==> Installing systemd unit"
install -D -m 0644 "$SRC_DIR/$NAME.service" "$UNIT_DST"
systemctl daemon-reload
systemctl enable "$NAME" >/dev/null
systemctl restart "$NAME"

if [[ "$WITH_NGINX" -eq 1 ]]; then
  echo "==> Installing nginx site"
  install -D -m 0644 "$SRC_DIR/nginx.conf" "$NGINX_DST"
  nginx -t
  systemctl reload nginx
fi

set -a
# shellcheck disable=SC1091
source "$CONF_DIR/sidecar.env"
set +a
HEALTH_URL="http://${HOST:-127.0.0.1}:${PORT:-8090}/_sidecar/health"
echo "==> Waiting for $HEALTH_URL"
for _ in {1..30}; do
  if curl -fsS --max-time 2 "$HEALTH_URL"; then
    echo
    echo "$NAME is up"
    exit 0
  fi
  sleep 1
done
echo "health check failed: $HEALTH_URL" >&2
journalctl -u "$NAME" -n 50 --no-pager >&2 || true
exit 1
