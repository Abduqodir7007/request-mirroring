#!/bin/sh
# mirror-switch on|off|status — host nginx.
# D holds direct.conf (original) and mirror.conf (with the app_mirror upstream).
set -eu
F=${MIRROR_SITE_CONF:-/etc/nginx/sites-available/app.conf}
D=${MIRROR_DIR:-/root/nginx-mirror}
UPSTREAM=${MIRROR_UPSTREAM:-app_mirror}

apply() {
  cp -p "$F" "$D/last-before-switch.conf"
  cp "$D/$1.conf" "$F"
  if nginx -t 2>/dev/null; then
    nginx -s reload
    echo "mirror-switch: $1 (nginx reloaded)"
  else
    cp -p "$D/last-before-switch.conf" "$F"
    echo "mirror-switch: nginx -t failed, previous config restored" >&2
    exit 1
  fi
}

case "${1:-status}" in
  on)  apply mirror ;;
  off) apply direct ;;
  status) if grep -q "$UPSTREAM" "$F"; then echo "mirror ON"; else echo "mirror OFF (direct to primary)"; fi ;;
  *) echo "usage: mirror-switch on|off|status" >&2; exit 2 ;;
esac
