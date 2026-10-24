# Shadow data: database, Redis, network

## Why no DB container in the new service's compose

The new service's compose is written for the **cutover**: on that day it uses the existing production database, so it must not start one of its own. Shadow mode only swaps the env file.

Containers from different compose projects talk through one **external Docker network**:

```yaml
# new service compose            # shadow compose
networks:                         networks:
  app_net:                          app_net:
    name: shared_net                  name: shared_net
    external: true                    external: true
```

Docker's embedded DNS (`127.0.0.11`) resolves container names on that network, so the env decides everything:

| mode | `DATABASE_URL` host | `REDIS_URL` host |
|---|---|---|
| shadow | `shadow_pg` | `shadow_redis` |
| cutover | the primary's DB container | the primary's Redis |

Same image, same network, one env file different. Consequence: a wrong host name in `.env.shadow` reaches the **real** database just as easily — check every host.

If two app stacks live on different networks (e.g. a second service), attach the shadow PG/Redis/fakes to both networks instead of exposing ports.

## Shadow Postgres

- Separate container, own volume, small limits (`shared_buffers` 64–128MB, `synchronous_commit=off`, `mem_limit`).
- One database per mirrored service (`app_auth`, `app_core`…) in the same container is fine.
- Same major version as prod (or newer) so `pg_dump | psql` restores cleanly.
- Large tables: dump schema + small tables fully, big append-only tables only for the last N days.

## Shadow Redis

`redis-server --save "" --appendonly no --maxmemory 64mb --maxmemory-policy allkeys-lru` — pure cache, never persisted. Clear keys the new service caches from external calls when you change fakes (e.g. cached AI answers).

## Object storage

Create shadow buckets (`media-shadow`) on the same MinIO/S3 and point the shadow there. Response bodies will contain the shadow bucket name → `SHADOW_BODY_REPLACE=media-shadow=media` in the proxy.

## Keeping the copy fresh

Both services create rows independently, so the copies drift: the shadow assigns different IDs, a row created by the primary 5 minutes ago doesn't exist on the shadow, follow-up requests (`PATCH /orders/123`) return 404 on the shadow → `status_diff` noise.

`templates/scripts/shadow-db-refresh.sh` (cron, every 30 min worked well for ~250 MB total):

1. `flock` so two runs never overlap;
2. stop the shadow app(s) (trap restarts them on any error);
3. `DROP DATABASE … WITH (FORCE)` + `CREATE DATABASE` on the shadow PG;
4. `pg_dump` from prod (read-only) piped into the shadow;
5. run the new service's migrations against the shadow only;
6. start the shadow app(s); log counts.

During the ~20 s window the proxy records `shadow_error` — expected, ignore it in triage.

## Isolation checklist

- [ ] no prod DB/Redis host in `.env.shadow`
- [ ] message broker (RabbitMQ/Kafka) disabled or a separate vhost
- [ ] gRPC/HTTP calls to other internal services: either to their shadow or disabled
- [ ] every third-party URL → fake (`/sink/`, `/ai/`, Bot API, stub)
- [ ] payment / SMS / e-mail providers → fake or disabled
- [ ] scheduled jobs that send something → fake targets confirmed before enabling
- [ ] webhook registration (`setWebhook` etc.) never reaches the real provider
