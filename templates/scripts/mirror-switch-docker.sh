#!/bin/sh
# mirror-switch-docker on|off|status — nginx running inside a container.
# NOTE: if the gateway image renders its config from a template on start, a container
# restart silently turns the mirror off. Run `status` after any gateway restart.
set -eu
G=${MIRROR_DIR:-/srv/shadow/gw}
C=${GATEWAY_CONTAINER:-gateway_nginx}
F=${GATEWAY_CONF:-/etc/nginx/conf.d/backend.conf}
UPSTREAM=${MIRROR_UPSTREAM:-core_mirror}

apply() {
  docker cp "$C:$F" "$G/last-before-switch.conf"
  docker cp "$G/$1.conf" "$C:$F"
  if docker exec "$C" nginx -t >/dev/null 2>&1; then
    docker exec "$C" nginx -s reload
    echo "mirror-switch-docker: $1 (gateway reloaded)"
  else
    docker cp "$G/last-before-switch.conf" "$C:$F"
    echo "mirror-switch-docker: nginx -t failed, previous config restored" >&2
    exit 1
  fi
}

case "${1:-status}" in
  on)  apply mirror ;;
  off) apply direct ;;
  status) if docker exec "$C" grep -q "$UPSTREAM" "$F"; then echo "gateway mirror ON"; else echo "gateway mirror OFF (direct to primary)"; fi ;;
  *) echo "usage: mirror-switch-docker on|off|status" >&2; exit 2 ;;
esac
