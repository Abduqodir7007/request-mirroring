#!/bin/sh
# mirror-check — one-screen health of a running mirror. Edit the variables.
ADMIN=${ADMIN:-127.0.0.1:9187}
LOG=${NGINX_ACCESS_LOG:-/var/log/nginx/access.log}
PREFIX=${MIRRORED_PREFIX:-/api/}
PRIMARY=${PRIMARY_CONTAINER:-app_old}
SHADOW=${SHADOW_CONTAINER:-app_new}
PROXY=${PROXY_CONTAINER:-shadow_proxy_core}

echo "== $(date -u +%T) UTC  $(mirror-switch status 2>/dev/null)"

since=$(date -u -d '-2 min' '+%d/%b/%Y:%H:%M')
awk -v s="$since" -v p="$PREFIX" '$4 >= "["s && index($7, p) == 1 {c[$9]++; n++}
  END {printf "  nginx (2 min): %d requests;", n; for (k in c) printf " %s=%d", k, c[k]; print ""}' "$LOG"

curl -s "$ADMIN/metrics" | awk '
  /^shadow_compare_total/ {split($0,a,"result=\""); split(a[2],b,"\""); r[b[1]]+=$NF}
  /^shadow_dropped_total/ {d+=$NF}
  END {printf "  compare:"; for (k in r) printf " %s=%d", k, r[k]; printf "  dropped=%d\n", d}'

echo "  errors (2 min): primary=$(docker logs --since 2m "$PRIMARY" 2>&1 | grep -ciE 'error|traceback')  shadow=$(docker logs --since 2m "$SHADOW" 2>&1 | grep -ciE 'error|panic')"
docker ps --format '{{.Names}} {{.Status}}' | grep -E "^($PRIMARY|$SHADOW|$PROXY) "
