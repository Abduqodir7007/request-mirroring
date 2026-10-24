# Rollout runbook

Placeholders used below — replace with the real names found in phase 0:

| placeholder | example |
|---|---|
| `APP_OLD` | container of the current service, e.g. `shop_app` (listens on 8000 inside) |
| `APP_NEW` | container of the new service, e.g. `shop_go_app` |
| `NET` | external Docker network both are on, e.g. `shared_net` |
| `SVC` | short service name for labels, e.g. `core` |
| `MON_IP` | monitoring server (Prometheus) IP |
| `SHADOW_DIR` | e.g. `/srv/shadow` — holds the shadow compose + env files (chmod 600 env) |

## Phase 0 — discovery (read-only)

```bash
# who serves the domain?
grep -rn "server_name" /etc/nginx/ 2>/dev/null          # host nginx
docker ps --format '{{.Names}}\t{{.Ports}}' | grep -i nginx   # or nginx in a container
# upstream / proxy_pass of the routes you will mirror
grep -nE 'upstream|location|proxy_pass' /etc/nginx/sites-enabled/<site>.conf
# networks of the primary, its DB, Redis
for c in APP_OLD <db> <redis>; do docker inspect $c --format '{{.Name}} {{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'; done
# compose project of each container
docker inspect APP_OLD --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}'
```

Write down:
- every outbound integration of the service (Telegram, SMS, payment, AI, S3 buckets, other internal services, webhooks) → each needs a fake or a shadow copy;
- WebSocket / SSE / upload routes → `SKIP_PATHS` or separate `location` blocks that keep going straight to the primary;
- background schedulers in the new service that write or send → keep disabled until fakes are ready;
- how big the DB is and whether a full dump is acceptable (otherwise schema + recent rows only).

## Phase 1 — images

```bash
cd tool
docker build --target proxy  -t shadow_proxy:$(git rev-parse --short HEAD) .
docker build --target faketg -t shadow_faketg:$(git rev-parse --short HEAD) .
docker build --target cgstats -t shadow_cgstats:$(git rev-parse --short HEAD) .   # only if cAdvisor fails
```

No registry? `docker save img | ssh host docker load`. Build Linux images on Linux or with LF line endings — a Windows checkout with CRLF can silently corrupt embedded data files (pitfalls #4).

## Phase 2 — shadow data

```bash
mkdir -p SHADOW_DIR && cd SHADOW_DIR
cp <skill>/templates/docker-compose.shadow.yml docker-compose.yml   # edit names/networks
printf 'POSTGRES_USER=shadow\nPOSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 16)" > pg.env && chmod 600 pg.env
docker compose up -d pg redis
```

Load a copy (read-only on prod):

```bash
docker exec SHADOW_PG psql -U shadow -d postgres -c "CREATE DATABASE app_shadow"
docker exec <prod_db> pg_dump -U <user> -d <db> --no-owner --no-privileges \
  | docker exec -i SHADOW_PG psql -v ON_ERROR_STOP=1 -U shadow -d app_shadow
```

Run the **new** service's migrations against the copy only — with `docker run -e DATABASE_URL=<shadow>`; never via the compose file whose env points at prod (pitfalls #3).

Then install `templates/scripts/shadow-db-refresh.sh` as a cron (every 30–60 min) so the copy doesn't drift (see `shadow-data.md`).

## Phase 3 — shadow env and the new service

1. `cp .env.prod .env.shadow && chmod 600 .env.shadow`.
2. Go through `templates/env.shadow.checklist.md` line by line. Every URL/host must point to a shadow target or the fake. **No empty values for external URLs** — an empty value may fall back to a hard-coded production default.
3. Add `templates/app/docker-compose.shadow.override.yml` next to the service's compose file and start:

```bash
docker compose --env-file .env.shadow -f docker-compose.prod.yml -f docker-compose.shadow.yml up -d --no-build app
```

4. Smoke-test on its loopback port, then check its outbound connections (logs, fake's `calls.jsonl`, `ss -tnp` inside the netns) — nothing may reach a production host.

## Phase 4 — proxy

Add one `proxy-SVC` service to the shadow compose (see template), `docker compose up -d proxy-SVC`, then by hand:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: <domain>' http://127.0.0.1:8019/<a GET route>
curl -s 127.0.0.1:9187/metrics | grep shadow_compare_total
curl -s 127.0.0.1:9187/diffs.json | head
```

## Phase 5 — monitoring

- Prometheus: add jobs from `templates/prometheus/scrape.yml`; open 9187 (and 8080 for cAdvisor/cgstats) only to `MON_IP` (`ADMIN_ALLOW` + firewall).
- Grafana: `python3 templates/grafana/gen_dashboard.py config.json dashboard.json` and import. For the diff table install the Infinity datasource plugin and point it at `http://<host>:9187/diffs.json`.

## Phase 6 — switch on

```bash
cp /etc/nginx/sites-available/<site>.conf SWITCH_DIR/direct.conf     # original, untouched
cp direct.conf mirror.conf   # edit: add upstream + change proxy_pass (templates/nginx/*)
install -m 755 templates/scripts/mirror-switch.sh /usr/local/bin/mirror-switch
mirror-switch on
mirror-check            # templates/scripts/mirror-check.sh
```

For nginx inside a container use `mirror-switch-docker.sh` and the gateway template. If the gateway container rebuilds its config from a template on restart, the mirror silently turns off — re-run `on` after any gateway restart (or bake it into the template).

Watch for 5–10 minutes: client-visible status codes unchanged, primary error log unchanged, `shadow_dropped_total` ~0, shadow outbound = fakes only.

## Phase 7 — triage and iterate

For each route with non-match, open `/diffs`, and classify:

| class | action |
|---|---|
| real bug in the new service | fix, rebuild, redeploy the shadow, watch the route go green |
| shadow data stale (404 on a row created after the dump, IDs diverged) | refresh data; accept as explained |
| feature intentionally faked (AI, OTP, identity provider) | accept as explained |
| equal rows in different order, float formatting | fix ordering in the new code if the API contract implies it, else add to ignore |
| volatile field (timestamps, tokens) | add to `IGNORE_KEYS` |

Exit criteria for cutover: stable parity (e.g. ≥ 99.5 % for a full business day/week), every remaining diff class explained, shadow latency and RAM known, rollback rehearsed.

## Cutover

1. Stop shadow writes: `mirror-switch off`.
2. Start the new service with the prod env (same image/compose; swap `.env.shadow` → `.env.prod`), run its migrations on prod **only if the old service won't run against that DB anymore** (two writers with different schemas break each other).
3. Point nginx straight at the new service; keep the old containers stopped, not removed.
4. Rollback = start old, nginx back to `direct.conf`.
