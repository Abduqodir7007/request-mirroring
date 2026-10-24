---
name: request-mirroring
description: Language-agnostic shadow-traffic (request mirroring) rollout for replacing any HTTP backend — every live request is answered by the current (primary) service while a copy goes to the new (shadow) service, both responses are compared, and parity %, differing responses, latency and RAM/CPU of both are shown in Grafana. Works for any pair of HTTP services in any language or framework (Python→Python, Python→Go, Node→Go, Java→Kotlin, PHP→Python, Django 4→5, monolith→microservice, v1→v2 of the same code, new DB engine, new server like gunicorn→uvicorn). Use when the user wants to mirror / shadow / tee production traffic, compare old and new implementations on real requests, prove parity before a cutover, or asks how to switch backends safely. The comparing proxy and metrics exporter are a prebuilt Go tool (tool/) — the services themselves can be anything.
---

# Request mirroring (shadow traffic)

Goal: run the new implementation against **real production traffic** without letting it affect a single user, and get hard numbers in **Grafana** — response parity %, every differing response side by side, p50/p95/p99, requests/s, RAM/CPU — before switching.

```
client ─► nginx ─► shadow proxy :8019 ──► PRIMARY (old)  → its answer goes to the client
             │                    └─copy (async, timeout) ─► SHADOW (new) → answer only compared
             └─ proxy down → nginx "backup" upstream → PRIMARY directly
SHADOW uses its own DB copy, own cache, and a fake for every external API (messaging, SMS, AI, payments, webhooks…)
proxy :9187/metrics + /diffs.json ─► Prometheus ─► Grafana dashboard (generated)
```

## Language and stack independence

- The proxy only speaks **HTTP**. Primary and shadow can be written in anything and run on anything that listens on a port (Docker container, systemd service, another host).
- Only the tooling is Go: `proxy` (mirror + compare + Prometheus metrics), `fakeapi` (stub external APIs), `cgstats` (container RAM/CPU). They run as their own containers; nobody needs Go installed in the apps. Images build from `tool/` with Docker alone.
- Responses are compared as JSON when both sides are JSON (key order and `1` vs `1.0` don't matter), otherwise byte-for-byte. HTML/text APIs work too, with `IGNORE_KEYS` not applying.
- Typical scenarios and what changes:

| scenario | shadow is | notes |
|---|---|---|
| rewrite in another language (Python→Go, Node→Rust…) | new codebase | own migrations; compare RAM/CPU too |
| same language, new version (Python v1 → Python v2) | same repo, new branch/tag | migrations of v2 run on the shadow copy only |
| framework / runtime upgrade (Django 4→5, Node 18→22, gunicorn→uvicorn) | same code, new image | usually no schema change; watch latency and errors |
| monolith → service | new service for a path prefix | mirror only that `location` |
| new database / ORM / query layer | same API, different data access | shadow DB may be a different engine — load it with your own converter |

Read before acting:
- `references/architecture.md` — how the proxy works, what is compared, metrics, limits.
- `references/rollout-runbook.md` — the step-by-step procedure (phases 0–7) with commands, migrations per stack.
- `references/shadow-data.md` — shadow DB/cache/network design and periodic refresh.
- `references/grafana.md` — getting the results into Grafana (existing Grafana or the bundled stack).
- `references/pitfalls.md` — mistakes that actually happened in production. **Read this one every time.**

Try it first: `example/` is a complete local demo (Python v1 vs Python v2 + proxy + Prometheus + Grafana): `cd example && docker compose up -d --build`, open http://127.0.0.1:3000 (admin/admin).

Ready-made pieces:
| Path | What |
|---|---|
| `tool/` | Go source (stdlib only): `proxy`, `fakeapi` (fake Telegram Bot API + `/ai/`, `/sink/`, stub endpoints), `cgstats`. `docker build --target proxy` etc. |
| `templates/docker-compose.shadow.yml` | Shadow stack: proxy, fake APIs, shadow Postgres, shadow Redis on the app's external Docker network |
| `templates/app/docker-compose.shadow.override.yml` | Run the new service with `.env.shadow` instead of its prod env, same image/compose |
| `templates/env.shadow.checklist.md` | Every variable the shadow env must override |
| `templates/nginx/host-mirror.conf` | Host nginx: upstream proxy + `backup` primary |
| `templates/nginx/docker-gateway-mirror.conf` | nginx running in a container: `resolver 127.0.0.11` + `resolve` upstreams |
| `templates/scripts/mirror-switch.sh`, `mirror-switch-docker.sh` | `on|off|status` — swap config, `nginx -t`, reload, auto-rollback |
| `templates/scripts/mirror-check.sh` | One-screen health check |
| `templates/scripts/shadow-db-refresh.sh` | Cron: stop shadow app → re-dump prod read-only → run the shadow's migrate command → start |
| `templates/monitoring/` | Prometheus + Grafana compose with provisioned datasources and dashboard folder (for hosts without monitoring) |
| `templates/prometheus/scrape.yml` | Scrape jobs for an existing Prometheus |
| `templates/grafana/gen_dashboard.py`, `import.sh` | Generate the dashboard (custom names like "Python v1"/"Python v2"), import into an existing Grafana |

## Non-negotiable rules

1. **The client always gets the primary's response.** The shadow is fire-and-forget with a timeout and an in-flight cap; when the cap is full the copy is dropped, never queued.
2. **The shadow never touches production state.** Own database (a copy), own cache, own buckets, fake external APIs. Before turning traffic on, grep the shadow env for every host name and URL and prove each one points to a shadow/fake target (pitfalls #1).
3. **Fallback is automatic.** nginx upstream = proxy + primary as `backup`, `proxy_next_upstream error timeout`, short connect timeout. If the proxy dies, users don't notice.
4. **One switch, reversible in seconds.** `mirror-switch on|off|status`; it validates with `nginx -t` and restores the previous file on failure. Keep `direct.conf` (original) and `mirror.conf` side by side.
5. **Don't mirror** WebSocket, SSE/streams, uploads above the size limit, admin/docs/static. The proxy skips WS/SSE automatically; use `SKIP_PATHS` for the rest.
6. **Results go to Grafana.** The rollout isn't done until the dashboard shows both services and the differences table loads (verify it, don't assume).
7. Ask the user before every production change (nginx, compose, cron, Prometheus/Grafana config). Do read-only discovery first.

## Procedure (summary — details in the runbook)

0. **Discover** (read-only): how traffic reaches the primary (host nginx vs nginx container, upstream, ports), its stack and how it is deployed, the Docker network / host its DB and cache are on, every external call the service makes, which routes are WS/SSE/uploads, whether Prometheus/Grafana already exist.
1. **Build** the new service image (its own Dockerfile, any language) and the tool images (`proxy`, `fakeapi`).
2. **Shadow data**: start shadow DB/cache on the same network; load a copy of prod (read-only dump); run the new service's migrations **only** on the copy (command depends on the stack — see the runbook table).
3. **Shadow env**: copy prod env → `.env.shadow`, override DB/cache/bucket/every external URL/token, disable schedulers that send messages until fakes are in place. Start the new service with the override compose file.
4. **Proxy**: one proxy per service (`PRIMARY_URL`, `SHADOW_URL`, `SERVICE`, `SKIP_PATHS`, `ADMIN_ALLOW`). Test it by hand on its loopback port.
5. **Grafana**: scrape `:9187/metrics`, generate the dashboard with readable names (`primary_label`/`shadow_label`), install the Infinity datasource for the differences table, check every panel has data (`references/grafana.md`).
6. **Switch on** with the switch script; run the check script; watch the primary's error log for a few minutes.
7. **Triage diffs** in Grafana (or `/diffs`): classify every diff as real bug in the new code / stale shadow data / intentionally faked feature / ordering or formatting noise. Fix real bugs, add volatile keys to `IGNORE_KEYS`, refresh data periodically. Cutover only when parity is stable (e.g. ≥ 99.5 % with every remaining diff explained).

Cutover = point the new service at the prod env/DB (same compose, swap `.env.shadow` → `.env.prod`), switch nginx to it directly, keep the old one stopped but intact for rollback.

## Reading the results

| result | meaning | usual cause |
|---|---|---|
| `match` | same status and same body (after ignoring volatile keys) | — |
| `body_diff` | same status, different body | real bug, stale shadow data, ordering of equal rows, a field only one side returns, number/string type change |
| `status_diff` | different status code | missing row in shadow DB (404), auth/token not valid on shadow, different validation, real bug |
| `shadow_error` / `shadow_timeout` | shadow didn't answer | shadow restarting (e.g. during DB refresh), crash, slow query |
| `skipped` | not compared | stream, body over `MAX_COMPARE_KB` |

Every non-match is kept (last `DIFFS_KEEP`) at `/diffs` and in the Grafana "Differences" table with both bodies side by side and the differing JSON paths, secrets masked.
