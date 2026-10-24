# Results in Grafana

The proxy exposes everything Grafana needs:
- `GET :9187/metrics` — Prometheus metrics (`shadow_*`, see `architecture.md`);
- `GET :9187/diffs.json` — the last `DIFFS_KEEP` differing responses, read by the Infinity datasource.

## A. You already have Prometheus + Grafana

1. Prometheus: add the jobs from `templates/prometheus/scrape.yml` (one target per proxy; cAdvisor/cgstats per app host for RAM/CPU). Firewall 9187/8080 so only the Prometheus host can reach them, and put that IP in the proxy's `ADMIN_ALLOW`.
2. Infinity datasource (for the differences table):
   - install `yesoreyeram-infinity-datasource` **in a version that supports your Grafana** — check `https://grafana.com/api/plugins/yesoreyeram-infinity-datasource/versions` (`grafanaDependency`). Example: Infinity 4.x needs Grafana ≥ 11.6.11; with Grafana 11.3 use 3.7.1. A wrong version installs fine but the panel says *"Datasource … was not found"* and the browser console shows `react/jsx-runtime` 404;
   - create a datasource of that type with a fixed uid (e.g. `shadow-infinity`). Grafana fetches `/diffs.json` server-side, so the **Grafana host** must be allowed by the proxy's `ADMIN_ALLOW`.
3. Dashboard:
   ```bash
   cp templates/grafana/config.example.json config.json   # edit
   python3 templates/grafana/gen_dashboard.py config.json dashboard.json
   GRAFANA_URL=https://grafana.example.com GRAFANA_TOKEN=<Editor service-account token> templates/grafana/import.sh dashboard.json
   ```

`config.json`:

| key | meaning |
|---|---|
| `title`, `uid` | dashboard title / uid |
| `primary_label`, `shadow_label` | names shown everywhere instead of primary/shadow, e.g. `"Python v1"` / `"Python v2"`, `"Django"` / `"Go"` |
| `services` | `SERVICE` values of the proxies (one dashboard can hold several services; a selector filters them) |
| `diffs_urls` | `http://<proxy-host>:9187/diffs.json` for each proxy, as reachable **from the Grafana server** |
| `infinity_uid` | uid of the Infinity datasource |
| `containers` | optional, per service: regex of primary containers (app + workers) and shadow containers, for the RAM/CPU section |

## B. No monitoring yet — bundled stack

`templates/monitoring/` = Prometheus + Grafana with provisioned datasources (Prometheus uid `prometheus`, Infinity uid `shadow-infinity`) and a dashboards folder:

```bash
cp -r templates/monitoring /srv/mirror-monitoring && cd /srv/mirror-monitoring
# prometheus.yml: proxy targets; datasources.yml: nothing to change if the proxies are on the same Docker network
python3 /path/to/skill/templates/grafana/gen_dashboard.py config.json grafana/dashboards/mirror.json
GRAFANA_ADMIN_PASSWORD=$(openssl rand -hex 12) docker compose -f docker-compose.monitoring.yml up -d
ssh -L 3000:127.0.0.1:3000 <host>    # then open http://127.0.0.1:3000
```

Ports are bound to 127.0.0.1 — reach Grafana through an SSH tunnel or put it behind your existing reverse proxy with auth.

## Verify (don't skip)

- Prometheus `up{job="shadow"} == 1` for every proxy.
- Grafana API: `curl -u admin:… $GRAFANA/api/datasources` lists both datasources; `/api/search?query=<title>` finds the dashboard.
- Open the dashboard: all panels have data, **including both `p95 <primary>` and `p95 <shadow>` and the Differences table** (no red triangle on any panel). Hover a red triangle to read the error.

## Panels

| section | panels |
|---|---|
| Overview | Parity %, compared requests, differences, shadow errors/timeouts, p95 of both |
| Latency | p50/p95/p99 of both over time, requests/s of both |
| Per endpoint | requests, p95 of both, parity % — sorted worst first |
| Comparison | result over time (stacked), status code pairs (e.g. 400 → 401 on `/api/login`) |
| Differences | latest differing responses: endpoint, method, both statuses/times, differing JSON paths, both bodies (click a cell to see it whole) |
| Resources (optional) | RAM now per service, total RAM of both, RAM saved, RAM over time, CPU per container |
