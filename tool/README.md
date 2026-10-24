# tool — shadow proxy, fake external APIs, container stats

Three small services, Go standard library only.

| binary | what it does |
|---|---|
| `proxy` | Sits between nginx and the primary service. The client **always gets the primary's response**; a copy of the request goes to the shadow in parallel (`SHADOW_TIMEOUT`, at most `SHADOW_MAX_INFLIGHT`, extra copies are dropped — the client never waits). Status + JSON body are compared (`IGNORE_KEYS` skipped). WebSocket, SSE and bodies over `MAX_BODY_MB` go to the primary only. |
| `faketg` | Fake Telegram Bot API + `/ai/` (OpenAI-compatible, always `[]`) + `/sink/` (200 for outgoing webhooks) + `/myid/` (503 stub). Write calls never reach Telegram; read calls are cached from the real API. Optional digest of what the shadow would have sent, posted to a test chat. |
| `cgstats` | `container_memory_working_set_bytes` / `container_cpu_usage_seconds_total` from the Docker API + cgroup v2, for hosts where cAdvisor can't read containers. |

## Build

```bash
docker build --target proxy   -t shadow_proxy:$(git rev-parse --short HEAD) .
docker build --target faketg  -t shadow_faketg:$(git rev-parse --short HEAD) .
docker build --target cgstats -t shadow_cgstats:$(git rev-parse --short HEAD) .
go test -race ./...
```

## Metrics (`ADMIN_ADDR`, default `:9187/metrics`)

- `shadow_requests_total{service,backend,route,method,code}` — backend = `primary` | `shadow`
- `shadow_request_duration_seconds_bucket{service,backend,route,method,le}`
- `shadow_compare_total{service,route,result}` — `match`, `body_diff`, `status_diff`, `shadow_error`, `shadow_timeout`, `skipped`
- `shadow_status_diff_total{service,route,primary_code,shadow_code}`
- `shadow_dropped_total{service,reason}`
- `faketg_calls_total{bot,method,mode}`, `faketg_mirror_messages_total{result}`

`/diffs` — last `DIFFS_KEEP` differences (both responses side by side, `MASK_KEYS` fields shown as `***`). `/diffs.json` for Grafana. The admin port answers only IPs in `ADMIN_ALLOW`.

## Settings

`proxy`: `SERVICE`, `PRIMARY_URL`, `SHADOW_URL` (empty = pass-through), `ADDR` (:8019), `ADMIN_ADDR` (:9187), `ADMIN_ALLOW`, `SHADOW_TIMEOUT` (5s), `SHADOW_MAX_INFLIGHT` (64), `SHADOW_METHODS` (empty = all), `SKIP_PATHS` (regex), `MAX_BODY_MB` (10), `MAX_COMPARE_KB` (2048), `IGNORE_KEYS`, `MASK_KEYS`, `SHADOW_BODY_REPLACE` (`from=to,...`), `DIFFS_KEEP` (1000).

`faketg`: `ADDR` (:8081), `REAL_TELEGRAM_API_URL`, `PASSTHROUGH_READS` (true), `PASSTHROUGH_TTL` (10m), `RECORD_PATH` (/data/calls.jsonl), `RECORD_MAX_MB` (100), `MIRROR_TG_BOT_TOKEN`, `MIRROR_TG_CHAT_ID`, `MIRROR_INTERVAL` (1m), `MIRROR_EXAMPLES` (4).

`cgstats`: `ADDR` (:8080), `ALLOW`, `DOCKER_SOCKET`, `CGROUP_ROOT`.

Deploy example: `deploy/docker-compose.example.yml`, full stack template: `../templates/docker-compose.shadow.yml`.
