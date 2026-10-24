---
name: request-mirroring
description: Shadow-traffic (request mirroring) rollout for rewriting or replacing a backend service — every live request is answered by the current (primary) service while a copy goes to the new (shadow) service, and the two responses are compared and graphed. Use when the user wants to mirror / shadow / tee production traffic, compare an old and a new implementation (e.g. Python → Go, monolith → service, v1 → v2) on real requests, prove parity before a cutover, measure latency/RAM of both side by side, or asks "how do we safely switch backends". Covers the comparing proxy (Go source in tool/), nginx wiring with automatic fallback, isolated shadow database/Redis/fake external APIs, refresh of shadow data, Prometheus/Grafana, on/off switches and the cutover.
---

# Request mirroring (shadow traffic)

Goal: run the new implementation against **real production traffic** without letting it affect a single user, and get hard numbers — response parity %, list of differing responses, p50/p95/p99, RAM/CPU — before switching.

```
client ─► nginx ─► shadow proxy :8019 ──► PRIMARY (old)  → its answer goes to the client
             │                    └─copy (async, timeout) ─► SHADOW (new) → answer only compared
             └─ proxy down → nginx "backup" upstream → PRIMARY directly
SHADOW uses its own DB copy, own Redis, and a fake for every external API (Telegram, SMS, AI, webhooks…)
```

Read before acting:
- `references/architecture.md` — how the proxy works, what is compared, metrics, limits.
- `references/rollout-runbook.md` — the step-by-step procedure (phases 0–7) with commands.
- `references/shadow-data.md` — shadow DB/Redis/network design and periodic refresh.
- `references/pitfalls.md` — mistakes that actually happened in production. **Read this one every time.**

Ready-made pieces:
| Path | What |
|---|---|
| `tool/` | Go source (stdlib only): `proxy` (mirror+compare), `faketg` (fake Telegram Bot API + AI/sink/stub endpoints), `cgstats` (container RAM/CPU exporter when cAdvisor fails). `docker build --target proxy` etc. |
| `templates/docker-compose.shadow.yml` | Shadow stack: proxy, fake APIs, shadow Postgres, shadow Redis on the app's external Docker network |
| `templates/app/docker-compose.shadow.override.yml` | Run the new service with `.env.shadow` instead of its prod env, same image/compose |
| `templates/env.shadow.checklist.md` | Every variable the shadow env must override |
| `templates/nginx/host-mirror.conf` | Host nginx: upstream proxy + `backup` primary |
| `templates/nginx/docker-gateway-mirror.conf` | nginx running in a container: `resolver 127.0.0.11` + `resolve` upstreams |
| `templates/scripts/mirror-switch.sh`, `mirror-switch-docker.sh` | `on|off|status` — swap config, `nginx -t`, reload, auto-rollback |
| `templates/scripts/mirror-check.sh` | One-screen health check |
| `templates/scripts/shadow-db-refresh.sh` | Cron: stop shadow app → re-dump prod read-only → migrate → start |
| `templates/prometheus/scrape.yml` | Scrape jobs |
| `templates/grafana/gen_dashboard.py` | Generates the comparison dashboard JSON |

## Non-negotiable rules

1. **The client always gets the primary's response.** The shadow is fire-and-forget with a timeout and an in-flight cap; when the cap is full the copy is dropped, never queued.
2. **The shadow never touches production state.** Own database (a copy), own Redis, own buckets, fake external APIs. Before turning traffic on, grep the shadow env for every host name and URL and prove each one points to a shadow/fake target (see pitfalls #1).
3. **Fallback is automatic.** nginx upstream = proxy + primary as `backup`, `proxy_next_upstream error timeout`, short connect timeout. If the proxy dies, users don't notice.
4. **One switch, reversible in seconds.** `mirror-switch on|off|status`; it validates with `nginx -t` and restores the previous file on failure. Keep `direct.conf` (original) and `mirror.conf` side by side.
5. **Don't mirror** WebSocket, SSE/streams, uploads above the size limit, admin/docs/static. The proxy skips WS/SSE automatically; use `SKIP_PATHS` for the rest.
6. Ask the user before every production change (nginx, compose, cron). Do read-only discovery first.

## Procedure (summary — details in the runbook)

0. **Discover** (read-only): how traffic reaches the primary (host nginx vs nginx container, upstream, ports), the Docker network the primary and its DB/Redis are on, every external call the service makes, which routes are WS/SSE/uploads.
1. **Build** the new service image and the tool images (`proxy`, fake APIs).
2. **Shadow data**: start shadow Postgres/Redis on the same external network; load a copy of prod (read-only dump); run the new service's migrations **only** on the copy.
3. **Shadow env**: copy prod env → `.env.shadow`, override DB/Redis/bucket/every external URL/token, disable schedulers that send messages until fakes are in place. Start the new service with the override compose file.
4. **Proxy**: start one proxy per service (`PRIMARY_URL`, `SHADOW_URL`, `SERVICE`, `SKIP_PATHS`, `ADMIN_ALLOW`). Test it by hand on its loopback port.
5. **Monitoring**: Prometheus scrapes `:9187/metrics`; import the dashboard; restrict the admin port to the monitoring host.
6. **Switch on** with the switch script; run the check script; watch error logs of the primary for a few minutes.
7. **Triage diffs** in `/diffs` (or the Grafana table): classify every diff as real bug in the new code / stale shadow data / intentionally disabled feature / ordering or formatting noise. Fix real bugs, add noisy keys to `IGNORE_KEYS`, refresh data periodically. Cutover only when parity is stable (e.g. ≥ 99.5 % with every remaining diff explained).

Cutover itself = point the new service at the prod env/DB (same compose, swap `.env.shadow` → `.env.prod`), switch nginx to it directly, keep the old one stopped but intact for rollback.

## Reading the results

| result | meaning | usual cause |
|---|---|---|
| `match` | same status and same JSON (after ignoring volatile keys) | — |
| `body_diff` | same status, different body | real bug, stale shadow data, ordering of equal rows, a field only one side returns |
| `status_diff` | different status code | missing row in shadow DB (404), auth/token not valid on shadow, real bug |
| `shadow_error` / `shadow_timeout` | shadow didn't answer | shadow restarting (e.g. during DB refresh), crash, slow query |
| `skipped` | not compared | stream, body over `MAX_COMPARE_KB` |

Every non-match is kept (last `DIFFS_KEEP`) at `/diffs` with both bodies side by side, secrets masked.
