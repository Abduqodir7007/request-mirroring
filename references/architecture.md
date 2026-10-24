# Architecture

## Request path

```
            ┌──────────────────────── shadow proxy (tool/cmd/proxy) ─────────────────────────┐
client ──►  │ 1. wantShadow? (not WS/SSE, not SKIP_PATHS, method allowed, body ≤ MAX_BODY_MB)│
nginx       │ 2. read body into memory once                                                   │
            │ 3. take a slot (SHADOW_MAX_INFLIGHT); no slot → drop copy, count "busy"         │
            │ 4. goroutine: send copy to SHADOW_URL (+ X-Shadow-Request: 1), SHADOW_TIMEOUT    │
            │ 5. reverse-proxy to PRIMARY_URL, stream response to client, tee ≤ MAX_COMPARE_KB  │
            │ 6. after the client is answered: wait for shadow result, compare, record         │
            └───────────────────────────────────────────────────────────────────────────────┘
```

- The shadow copy starts **before** the primary call, so both see the request at about the same moment (important when the response depends on "now" or on rows the request itself creates).
- The client never waits for the shadow: comparison runs in a goroutine after the primary response has been written.
- Original `Host`, `X-Forwarded-*` headers are kept on both sides. Hop-by-hop headers are removed on the shadow copy.
- Redirects are not followed on the shadow side (compared as-is).

## Comparison (`internal/proxy/compare.go`)

1. Status differs → `status_diff`.
2. `SHADOW_BODY_REPLACE` (`from=to,from2=to2`) is applied to the shadow body (e.g. bucket name `media-shadow` → `media`).
3. Both bodies parse as JSON → keys listed in `IGNORE_KEYS` are dropped at every depth, numbers are normalised (`1` == `1.0`), then deep-equal. Differences are reported as JSON paths (`$.results[3].price`, `$.x: only in shadow`, `$.items: length 10 != 9`), max 20 per request.
4. Not JSON → byte-equal after trimming whitespace.

Default `IGNORE_KEYS`: `created_at,updated_at,date,timestamp,request_id,access,refresh,access_token,refresh_token,token,expires_at,exp,iat,jti` — anything that is legitimately different on each call.

Default `MASK_KEYS` (regex, applied to stored diff bodies and query strings): `token|password|passwd|secret|access|refresh|otp|sms_code|verify_code|pinfl|passport|authorization|cookie|hash`.

## Route grouping

Paths are normalised for metric labels: numeric segment → `{id}`, UUID → `{uuid}`, long alnum token containing a digit → `{key}`. At most 500 distinct routes; the rest are `other`. This keeps Prometheus cardinality bounded.

## Metrics (`ADMIN_ADDR`, default `:9187`)

| metric | labels |
|---|---|
| `shadow_requests_total` | service, backend (`primary`/`shadow`), route, method, code |
| `shadow_request_duration_seconds` (histogram) | service, backend, route, method |
| `shadow_compare_total` | service, route, result |
| `shadow_status_diff_total` | service, route, primary_code, shadow_code |
| `shadow_dropped_total` | service, reason (`busy`, `body_too_large`) |

Admin endpoints: `/metrics`, `/diffs` (HTML, newest first), `/diffs.json` (for Grafana Infinity), `/healthz`. All guarded by `ADMIN_ALLOW` (CIDR list) — open it only to the monitoring server.

## Proxy settings

| env | default | note |
|---|---|---|
| `SERVICE` | `bot` | label value; one proxy per service |
| `PRIMARY_URL` | required | e.g. `http://app_old:8000` (Docker DNS name) |
| `SHADOW_URL` | empty | empty = plain pass-through, nothing mirrored |
| `ADDR` / `ADMIN_ADDR` | `:8019` / `:9187` | |
| `ADMIN_ALLOW` | `127.0.0.1/32,::1/128` | |
| `SHADOW_TIMEOUT` | `5s` | |
| `SHADOW_MAX_INFLIGHT` | `64` | above it copies are dropped |
| `SHADOW_METHODS` | all | e.g. `GET,HEAD` for a read-only first phase |
| `SKIP_PATHS` | none | regex, e.g. `^/api/(admin|swagger|docs|static|media)(/|$)` |
| `MAX_BODY_MB` | `10` | larger request bodies are not mirrored |
| `MAX_COMPARE_KB` | `2048` | larger responses → `skipped` |
| `IGNORE_KEYS`, `MASK_KEYS`, `SHADOW_BODY_REPLACE`, `DIFFS_KEEP` (1000) | | see above |

## Fake external APIs (`tool/cmd/fakeapi`)

The shadow must not send SMS, Telegram messages, AI requests, webhooks or payments. Point every such URL of the shadow service at the fake:

| path | behaviour |
|---|---|
| `/bot<token>/<method>` (Telegram Bot API) | write methods (`send*`, `edit*`, `delete*`, `copyMessage`, `setWebhook`, `getUpdates`…) are **never forwarded**; a realistic fake response is returned (per-chat `message_id` from 900000001) and recorded to `calls.jsonl`. Read methods (`getMe`, `getChat`, `getChatMember`…) are fetched from the real API and cached (`PASSTHROUGH_READS=false` to disable) |
| `/ai/...` | OpenAI-compatible chat completion that always returns `"[]"` |
| `/sink/<name>` | accepts anything, returns `200 {"ok":true}` — use for outgoing webhooks |
| `/myid/...` | 503 — pattern for an identity provider you must not call |

Optionally it posts a digest of what the shadow *would* have sent to a test chat every `MIRROR_INTERVAL` (`MIRROR_TG_BOT_TOKEN`, `MIRROR_TG_CHAT_ID`). Add more stubs in `internal/fakeapi/server.go` for other providers (SMS, payments) the same way.

## Resource comparison

- cAdvisor (`ghcr.io/google/cadvisor` ≥ v0.53 for Docker 29) gives `container_memory_working_set_bytes`, `container_cpu_usage_seconds_total`.
- When cAdvisor can't read the containerd snapshotter ("failed to identify the read-write layer ID"), run `tool/cmd/cgstats` instead: same metric names from the Docker API + cgroup v2 (`docker.sock` and `/sys/fs/cgroup` read-only, host network, `ALLOW` = monitoring IP).
- Compare **primary app + its workers** (celery, sidekiq, rq, queue consumers, cron containers…) vs **the new service + its workers**; leave DB/cache out — they stay after cutover.
