#!/bin/bash
# shadow-db-refresh — re-copy production databases into the shadow Postgres.
# Prod is only READ (pg_dump). Shadow apps are stopped during the copy (~20 s for ~250 MB).
#
# cron:  */30 * * * * root /usr/local/bin/shadow-db-refresh >> /var/log/shadow-db-refresh.log 2>&1
#
# SHADOW_ENV must define SHADOW_PG_USER and SHADOW_PG_PASSWORD (chmod 600).
# SERVICES: "name|prod_db_container|prod_db_user|prod_db_name|shadow_app_container|migrate_image"
set -euo pipefail

SHADOW_ENV=${SHADOW_ENV:-/srv/shadow/shadow.env}
SHADOW_PG=${SHADOW_PG:-shadow_pg}
NETWORK=${NETWORK:-shared_net}
SERVICES=(
  "core|core_db|core_user|core|core_new_app|core_new_migrate:latest"
  # "auth|auth_db|auth_user|auth|auth_new_app|auth_new_migrate:latest"
)

exec 9>/run/lock/shadow-db-refresh.lock
flock -n 9 || { echo "$(date -u +%FT%TZ) another refresh is running"; exit 0; }
. "$SHADOW_ENV"

apps=()
for s in "${SERVICES[@]}"; do IFS="|" read -r _ _ _ _ app _ <<<"$s"; apps+=("$app"); done

start=$(date +%s)
docker stop -t 10 "${apps[@]}" >/dev/null
trap 'docker start "${apps[@]}" >/dev/null 2>&1 || true' EXIT

for s in "${SERVICES[@]}"; do
  IFS="|" read -r name db user dbname app migrate <<<"$s"
  target="app_$name"
  docker exec "$SHADOW_PG" psql -X -q -U "$SHADOW_PG_USER" -d postgres \
    -c "DROP DATABASE IF EXISTS $target WITH (FORCE)" -c "CREATE DATABASE $target"
  docker exec "$db" pg_dump -U "$user" -d "$dbname" --no-owner --no-privileges \
    | docker exec -i "$SHADOW_PG" psql -X -q -v ON_ERROR_STOP=1 -U "$SHADOW_PG_USER" -d "$target" >/dev/null
  # migrations of the NEW service, applied to the shadow copy only
  docker run --rm --network "$NETWORK" \
    -e DATABASE_URL="postgres://$SHADOW_PG_USER:$SHADOW_PG_PASSWORD@$SHADOW_PG:5432/$target?sslmode=disable" \
    "$migrate" up >/dev/null 2>&1
done

docker start "${apps[@]}" >/dev/null
trap - EXIT
echo "$(date -u +%FT%TZ) ok $(( $(date +%s) - start ))s"
