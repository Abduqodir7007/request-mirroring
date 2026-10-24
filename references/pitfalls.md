# Pitfalls (all of these happened in a real rollout)

1. **Empty env value = hard-coded production default.** The new service treated an empty `WEBHOOK_URL=` as "use the default", and the default was the real production webhook. It POSTed ~85 times to production before it was noticed. Rule: in `.env.shadow` no external URL may be empty — point it at the fake's `/sink/<name>`. Grep the new service's config code for defaults that contain real hosts.

2. **AI / LLM calls are external writes too.** They cost money and can create data. Point the AI base URL at the fake `/ai/` (returns `[]`), use a dummy key, and flush any AI response cache in the shadow Redis.

3. **The migrate step used the prod env.** `docker compose run migrate` (or `manage.py migrate` inside a container started from the prod compose) reads `.env.prod` — new migrations land on the production database while the old service still runs on it. Run migrations with `docker run --rm --network NET --env-file .env.shadow <new-image> <migrate cmd>`.

4. **CRLF builds.** Images built from a Windows checkout (`git archive` / `core.autocrlf=true`) had CRLF in embedded data files (a common-passwords list), so a validation silently stopped working and showed up as a diff. Build on Linux or with LF.

5. **nginx inside a container regenerates its config.** A gateway image that renders `conf.d/*.conf` from a template at start-up drops the mirror config on restart → mirror silently off. Re-apply after restarts; `status` in the switch script greps for the mirror upstream.

6. **Docker DNS + nginx upstreams.** Container IPs change on recreate. Inside a containerised nginx use `resolver 127.0.0.11 valid=10s;` and `server name:port resolve;` in a `zone`d upstream (nginx ≥ 1.27.3), or `set $u http://name:port; proxy_pass $u;`.

7. **cAdvisor on Docker 29 / containerd snapshotter.** Old cAdvisor (v0.49) fails with "client version 1.41 is too old"; any version may fail with "failed to identify the read-write layer ID". Use v0.53+ or the `cgstats` exporter from `tool/`.

8. **Stale shadow data looks like bugs.** 404s on update/delete routes and "user not found" are usually rows created after the last dump. Refresh periodically and classify before fixing.

9. **Order of equal rows.** `ORDER BY name` with duplicate names returns rows in any order — not a bug unless clients depend on it; add a tiebreaker (`, id`) in the new code if the API should be deterministic.

10. **The mirror finds real behaviour differences** — that's the point. Examples found: a limit counted "active" rows in the new code but "all non-deleted" rows in the old; a list returned "last 48 hours" instead of "deleted today". Match the old behaviour first, change it later deliberately.

11. **Secrets in shells.** `source .env` of a prod env can print secrets on a syntax error; read single keys with `grep '^KEY=' file | cut -d= -f2-`.

12. **Two writers, one database.** Never run old and new against the same prod DB with different schemas/migrations — the shadow must have its own copy; the cutover is a hard switch.

13. **Grafana plugin version.** The newest Infinity plugin (4.x) installs on Grafana 11.3 without error, but the panel shows "Datasource … was not found" (browser console: `react/jsx-runtime` 404). Pin a version whose `grafanaDependency` matches your Grafana (3.7.1 for 11.3).

14. **One mislabelled metric hides a whole series.** A leftover backend label (`go` instead of `shadow`) made every shadow latency panel say "No data" while request counts looked fine. After any change to the proxy, check `/metrics` shows both `backend="primary"` and `backend="shadow"` for every metric, and look at the dashboard, not only at the API.

15. **Monitoring ports.** The proxy admin port and exporters expose route names and response bodies. `ADMIN_ALLOW` = monitoring IP only, plus a firewall rule.
