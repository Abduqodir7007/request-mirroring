#!/bin/bash
# shadow-db-refresh — re-copy production Postgres databases into the shadow Postgres, then run
# the NEW service's own migrate command against the copy. Works for any stack: the migrate
# command runs in the shadow app's image with its .env.shadow (Django, Alembic, Rails, Prisma,
# goose, Flyway… — see references/rollout-runbook.md).
# Prod is only READ (pg_dump). Shadow apps are stopped during the copy (~20 s for ~250 MB).
#
# cron:  */30 * * * * root /usr/local/bin/shadow-db-refresh >> /var/log/shadow-db-refresh.log 2>&1
#
# SHADOW_ENV must define SHADOW_PG_USER (owner of the shadow databases, chmod 600 file).
# SERVICES, one line per mirrored service, "|"-separated:
#   prod_db_container | prod_db_user | prod_db_name | shadow_db_name | shadow_app_container | shadow_env_file | migrate command
# Leave the migrate command empty when the new service has no schema changes.
set -euo pipefail

SHADOW_ENV=${SHADOW_ENV:-/srv/shadow/shadow.env}
SHADOW_PG=${SHADOW_PG:-shadow_pg}
NETWORK=${NETWORK:-shared_net}
SERVICES=(
  "core_db|core_user|core|core_shadow|core_v2_app|/srv/core-v2/.env.shadow|python manage.py migrate --noinput"
  # "auth_db|auth_user|auth|auth_shadow|auth_go_app|/srv/auth-go/.env.shadow|/app/migrate up"
)

exec 9>/run/lock/shadow-db-refresh.lock
flock -n 9 || { echo "$(date -u +%FT%TZ) another refresh is running"; exit 0; }
. "$SHADOW_ENV"

apps=()
for s in "${SERVICES[@]}"; do IFS="|" read -r _ _ _ _ app _ _ <<<"$s"; apps+=("$app"); done

start=$(date +%s)
docker stop -t 10 "${apps[@]}" >/dev/null
trap 'docker start "${apps[@]}" >/dev/null 2>&1 || true' EXIT

for s in "${SERVICES[@]}"; do
  IFS="|" read -r db user dbname target app envfile migrate <<<"$s"
  docker exec "$SHADOW_PG" psql -X -q -U "$SHADOW_PG_USER" -d postgres \
    -c "DROP DATABASE IF EXISTS $target WITH (FORCE)" -c "CREATE DATABASE $target"
  docker exec "$db" pg_dump -U "$user" -d "$dbname" --no-owner --no-privileges \
    | docker exec -i "$SHADOW_PG" psql -X -q -v ON_ERROR_STOP=1 -U "$SHADOW_PG_USER" -d "$target" >/dev/null
  if [ -n "$migrate" ]; then
    image=$(docker inspect -f '{{.Config.Image}}' "$app")
    # shellcheck disable=SC2086
    docker run --rm --network "$NETWORK" --env-file "$envfile" "$image" $migrate >/dev/null
  fi
done

docker start "${apps[@]}" >/dev/null
trap - EXIT
echo "$(date -u +%FT%TZ) ok $(( $(date +%s) - start ))s"
