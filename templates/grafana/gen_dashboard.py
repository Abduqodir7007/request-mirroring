"""Generate the "primary vs shadow" Grafana dashboard.

    python3 gen_dashboard.py config.json dashboard.json

config.json (see config.example.json):
    title, uid            dashboard title / uid
    services              label values used by the proxies (SERVICE env)
    diffs_urls            list of http://host:9187/diffs.json for the Infinity table (optional)
    infinity_uid          uid of the Infinity datasource (optional)
    containers            [{"service": "core", "primary": "regex", "shadow": "regex"}]  (optional, needs cAdvisor/cgstats)
    primary_label         display name of the old service, e.g. "Python v1" (default "primary")
    shadow_label          display name of the new service, e.g. "Python v2" (default "shadow")
"""
import json
import re
import sys

cfg = json.load(open(sys.argv[1], encoding="utf-8"))
DS = {"type": "prometheus", "uid": "${ds}"}
F = 'service=~"$service", route=~"$route"'
PRIMARY, SHADOW = "#3274D9", "#00ADD8"
PN = cfg.get("primary_label", "primary")
SN = cfg.get("shadow_label", "shadow")
_id = 0


def nid():
    global _id
    _id += 1
    return _id


def q(expr):
    return expr.replace("$F", F)


def target(expr, legend="", ref="A", instant=False, fmt=None):
    t = {"datasource": DS, "expr": expr, "legendFormat": legend, "refId": ref}
    if instant:
        t.update(instant=True, range=False)
    if fmt:
        t["format"] = fmt
    return t


def color_overrides():
    return [
        {"matcher": {"id": "byRegexp", "options": f"^{re.escape(PN)}( .*)?$|.* · {re.escape(PN)}$"},
         "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": PRIMARY}}]},
        {"matcher": {"id": "byRegexp", "options": f"^{re.escape(SN)}( .*)?$|.* · {re.escape(SN)}$"},
         "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": SHADOW}}]},
    ]


def ts(title, targets, x, y, w=12, h=8, unit="s", desc="", stack=False):
    return {"id": nid(), "type": "timeseries", "title": title, "description": desc, "datasource": DS,
            "gridPos": {"x": x, "y": y, "w": w, "h": h}, "targets": targets,
            "fieldConfig": {"defaults": {"unit": unit, "custom": {"lineWidth": 2, "fillOpacity": 10,
                                                                  "stacking": {"mode": "normal" if stack else "none"}}},
                            "overrides": color_overrides()},
            "options": {"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["mean", "max", "lastNotNull"]},
                        "tooltip": {"mode": "multi"}}}


def stat(title, expr, x, y, w=4, h=4, unit="none", desc="", steps=None, decimals=None):
    d = {"unit": unit}
    if decimals is not None:
        d["decimals"] = decimals
    if steps:
        d["thresholds"] = {"mode": "absolute", "steps": steps}
        d["color"] = {"mode": "thresholds"}
    return {"id": nid(), "type": "stat", "title": title, "description": desc, "datasource": DS,
            "gridPos": {"x": x, "y": y, "w": w, "h": h}, "targets": [target(expr, instant=True)],
            "fieldConfig": {"defaults": d, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"]}, "colorMode": "background", "graphMode": "none"}}


def row(title, y):
    return {"id": nid(), "type": "row", "title": title, "collapsed": False,
            "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []}


PARITY = [{"color": "red", "value": None}, {"color": "orange", "value": 98}, {"color": "green", "value": 99.5}]
panels, y = [], 0

panels.append(row("Overview ($service)", y)); y += 1
panels += [
    stat("Parity", q('100 * (sum(increase(shadow_compare_total{$F, result="match"}[$__range])) or vector(0)) / clamp_min(sum(increase(shadow_compare_total{$F, result!="skipped"}[$__range])), 1)'),
         0, y, unit="percent", decimals=2, steps=PARITY, desc="Share of compared requests where primary and shadow answered the same. Cutover target: >= 99.5 %"),
    stat("Compared", q('sum(increase(shadow_compare_total{$F, result!="skipped"}[$__range]))'), 4, y, decimals=0),
    stat("Differences", q('sum(increase(shadow_compare_total{$F, result=~"body_diff|status_diff"}[$__range])) or vector(0)'), 8, y, decimals=0,
         steps=[{"color": "green", "value": None}, {"color": "orange", "value": 1}]),
    stat("Shadow error / timeout", q('sum(increase(shadow_compare_total{$F, result=~"shadow_error|shadow_timeout"}[$__range])) or vector(0)'), 12, y, decimals=0,
         steps=[{"color": "green", "value": None}, {"color": "red", "value": 1}]),
    stat("p95 primary", q('histogram_quantile(0.95, sum by (le) (rate(shadow_request_duration_seconds_bucket{$F, backend="primary"}[$__range])))'), 16, y, unit="s"),
    stat("p95 shadow", q('histogram_quantile(0.95, sum by (le) (rate(shadow_request_duration_seconds_bucket{$F, backend="shadow"}[$__range])))'), 20, y, unit="s"),
]
y += 4

panels.append(row("Latency: primary vs shadow", y)); y += 1
lat = []
for quant, name in (("0.5", "p50"), ("0.95", "p95"), ("0.99", "p99")):
    for b in ("primary", "shadow"):
        lat.append(target(q(f'histogram_quantile({quant}, sum by (le) (rate(shadow_request_duration_seconds_bucket{{$F, backend="{b}"}}[$__rate_interval])))'),
                          f"{b} {name}", f"{name}{b}"))
panels.append(ts("Response time p50 / p95 / p99", lat, 0, y, w=16, h=9, desc="Same request, measured by the proxy for both backends."))
panels.append(ts("Requests / second", [
    target(q('sum(rate(shadow_requests_total{$F, backend="primary"}[$__rate_interval]))'), "primary", "A"),
    target(q('sum(rate(shadow_requests_total{$F, backend="shadow"}[$__rate_interval]))'), "shadow", "B")],
    16, y, w=8, h=9, unit="reqps"))
y += 9

panels.append({
    "id": nid(), "type": "table", "title": "Per endpoint: latency and parity", "datasource": DS,
    "gridPos": {"x": 0, "y": y, "w": 24, "h": 10},
    "targets": [
        target(q('sum by (route) (increase(shadow_requests_total{$F, backend="primary"}[$__range]))'), ref="A", instant=True, fmt="table"),
        target(q('histogram_quantile(0.95, sum by (le, route) (rate(shadow_request_duration_seconds_bucket{$F, backend="primary"}[$__range])))'), ref="B", instant=True, fmt="table"),
        target(q('histogram_quantile(0.95, sum by (le, route) (rate(shadow_request_duration_seconds_bucket{$F, backend="shadow"}[$__range])))'), ref="C", instant=True, fmt="table"),
        target(q('100 * (sum by (route) (increase(shadow_compare_total{$F, result="match"}[$__range])) or 0 * sum by (route) (increase(shadow_compare_total{$F, result!="skipped"}[$__range]))) / clamp_min(sum by (route) (increase(shadow_compare_total{$F, result!="skipped"}[$__range])), 1)'), ref="D", instant=True, fmt="table")],
    "transformations": [{"id": "merge"}, {"id": "organize", "options": {"excludeByName": {"Time": True}, "renameByName": {
        "route": "Endpoint", "Value #A": "Requests", "Value #B": "p95 primary", "Value #C": "p95 shadow", "Value #D": "Parity %"}}}],
    "fieldConfig": {"defaults": {}, "overrides": [
        {"matcher": {"id": "byRegexp", "options": "p95.*"}, "properties": [{"id": "unit", "value": "s"}]},
        {"matcher": {"id": "byName", "options": "Requests"}, "properties": [{"id": "decimals", "value": 0}]},
        {"matcher": {"id": "byName", "options": "Parity %"}, "properties": [
            {"id": "unit", "value": "percent"}, {"id": "decimals", "value": 2},
            {"id": "custom.cellOptions", "value": {"type": "color-background"}},
            {"id": "thresholds", "value": {"mode": "absolute", "steps": PARITY}}]}]},
    "options": {"sortBy": [{"displayName": "Parity %", "desc": False}]}})
y += 10

panels.append(row("Comparison", y)); y += 1
panels.append(ts("Result over time", [target(q('sum by (result) (rate(shadow_compare_total{$F}[$__rate_interval]))'), "{{result}}")],
                 0, y, unit="reqps", stack=True,
                 desc="match | body_diff | status_diff | shadow_error | shadow_timeout | skipped"))
panels.append({
    "id": nid(), "type": "table", "title": "Status code differences (primary → shadow)", "datasource": DS,
    "gridPos": {"x": 12, "y": y, "w": 12, "h": 8},
    "targets": [target(q('sum by (route, primary_code, shadow_code) (increase(shadow_status_diff_total{$F}[$__range])) > 0'), instant=True, fmt="table")],
    "transformations": [{"id": "organize", "options": {"excludeByName": {"Time": True},
                                                       "renameByName": {"route": "Endpoint", "primary_code": "primary", "shadow_code": "shadow", "Value": "Count"}}}],
    "fieldConfig": {"defaults": {"decimals": 0}, "overrides": []}})
y += 8

if cfg.get("diffs_urls"):
    INF = {"type": "yesoreyeram-infinity-datasource", "uid": cfg.get("infinity_uid", "shadow-infinity")}
    cols = [("time", "Time", "timestamp"), ("service", "Service", "string"), ("result", "Result", "string"),
            ("method", "Method", "string"), ("route", "Endpoint", "string"),
            ("primary_status", "primary status", "number"), ("shadow_status", "shadow status", "number"),
            ("primary_ms", "primary ms", "number"), ("shadow_ms", "shadow ms", "number"),
            ("paths", "Differing paths", "string"), ("shadow_error", "Shadow error", "string"),
            ("primary_body", "primary body", "string"), ("shadow_body", "shadow body", "string"), ("path", "Full path", "string")]
    panels.append({
        "id": nid(), "type": "table", "title": "Differences (latest first)", "datasource": INF,
        "description": "Requests where the answers differed. Secrets are masked. Filter by column headers; click a body cell to see it whole.",
        "gridPos": {"x": 0, "y": y, "w": 24, "h": 14},
        "targets": [{"datasource": INF, "refId": chr(65 + i), "type": "json", "source": "url", "format": "table", "parser": "backend",
                     "url": u, "url_options": {"method": "GET", "data": ""}, "root_selector": "",
                     "columns": [{"selector": s, "text": t, "type": ty} for s, t, ty in cols]}
                    for i, u in enumerate(cfg["diffs_urls"])],
        "transformations": [{"id": "merge"}, {"id": "sortBy", "options": {"sort": [{"field": "Time", "desc": True}]}}],
        "fieldConfig": {"defaults": {"custom": {"filterable": True, "inspect": True}}, "overrides": [
            {"matcher": {"id": "byName", "options": "Result"}, "properties": [
                {"id": "custom.cellOptions", "value": {"type": "color-text"}},
                {"id": "mappings", "value": [{"type": "value", "options": {
                    "status_diff": {"color": "red", "index": 0}, "shadow_error": {"color": "red", "index": 1},
                    "shadow_timeout": {"color": "red", "index": 2}, "body_diff": {"color": "orange", "index": 3}}}]}]},
            {"matcher": {"id": "byRegexp", "options": ".* ms"}, "properties": [{"id": "unit", "value": "ms"}, {"id": "decimals", "value": 0}]},
            {"matcher": {"id": "byRegexp", "options": ".* body"}, "properties": [{"id": "custom.width", "value": 320}]},
            {"matcher": {"id": "byName", "options": "Full path"}, "properties": [{"id": "custom.hidden", "value": True}]}]},
        "options": {"showHeader": True, "cellHeight": "sm", "footer": {"show": True, "reducer": ["count"], "fields": ["Endpoint"]}}})
    y += 14

groups = cfg.get("containers") or []
if groups:
    def mem(regex):
        return f'sum(max by (name) (container_memory_working_set_bytes{{name=~"{regex}"}}))'

    p_all = "|".join(g["primary"] for g in groups)
    s_all = "|".join(g["shadow"] for g in groups)
    panels.append(row("Resources", y)); y += 1
    bars = []
    for i, g in enumerate(groups):
        bars.append(target(mem(g["primary"]), f'{g["service"]} · primary', f"P{i}", instant=True))
        bars.append(target(mem(g["shadow"]), f'{g["service"]} · shadow', f"S{i}", instant=True))
    panels.append({"id": nid(), "type": "bargauge", "title": "RAM now: primary vs shadow", "datasource": DS,
                   "description": "Primary: app + worker containers. Shadow: the new service. Databases/Redis excluded.",
                   "gridPos": {"x": 0, "y": y, "w": 12, "h": 10}, "targets": bars,
                   "fieldConfig": {"defaults": {"unit": "bytes", "decimals": 0, "min": 0, "color": {"mode": "fixed", "fixedColor": PRIMARY}},
                                   "overrides": color_overrides()},
                   "options": {"orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True,
                               "valueMode": "color", "namePlacement": "left", "reduceOptions": {"calcs": ["lastNotNull"]}}})
    panels += [
        stat("Primary RAM", mem(p_all), 12, y, w=4, h=5, unit="bytes", decimals=0, steps=[{"color": PRIMARY, "value": None}]),
        stat("Shadow RAM", mem(s_all), 16, y, w=4, h=5, unit="bytes", decimals=0, steps=[{"color": SHADOW, "value": None}]),
        stat("Saved", f"{mem(p_all)} - {mem(s_all)}", 20, y, w=4, h=5, unit="bytes", decimals=0, steps=[{"color": "green", "value": None}]),
    ]
    panels.append(ts("RAM over time", [target(mem(p_all), "primary", "A"), target(mem(s_all), "shadow", "B")], 12, y + 5, h=5, unit="bytes"))
    y += 10
    panels.append(ts("CPU", [target(f'sum by (name) (rate(container_cpu_usage_seconds_total{{name=~"{p_all}|{s_all}"}}[$__rate_interval]))', "{{name}}")],
                     0, y, w=24, h=8, unit="percentunit"))
    y += 8

dash = {
    "uid": cfg.get("uid", "primary-vs-shadow"), "title": cfg.get("title", "Primary vs shadow (mirror)"),
    "tags": ["mirror", "shadow"], "schemaVersion": 39, "version": 1, "refresh": "30s",
    "time": {"from": "now-6h", "to": "now"},
    "templating": {"list": [
        {"name": "ds", "label": "Datasource", "type": "datasource", "query": "prometheus"},
        {"name": "service", "label": "Service", "type": "custom", "multi": True, "includeAll": True, "allValue": ".*",
         "query": ",".join(cfg["services"]), "current": {"text": "All", "value": "$__all"}},
        {"name": "route", "label": "Endpoint", "type": "query", "datasource": DS, "multi": True, "includeAll": True, "allValue": ".*",
         "query": {"query": 'label_values(shadow_requests_total{service=~"$service"}, route)', "refId": "route"},
         "refresh": 2, "current": {"text": "All", "value": "$__all"}},
    ]},
    "panels": panels,
}
WORD = re.compile(r"\b(primary|shadow)\b", re.IGNORECASE)
LABELS = {"primary": PN, "shadow": SN}


def relabel(node, key=None):
    if isinstance(node, dict):
        for k, v in node.items():
            if k == "renameByName" and isinstance(v, dict):
                node[k] = {kk: WORD.sub(lambda m: LABELS[m.group(1).lower()], vv) for kk, vv in v.items()}
            elif k in ("title", "legendFormat", "description", "text") and isinstance(v, str):
                node[k] = WORD.sub(lambda m: LABELS[m.group(1).lower()], v)
            else:
                relabel(v, k)
    elif isinstance(node, list):
        for v in node:
            relabel(v, key)


relabel(dash["panels"])
dash["title"] = cfg.get("title", f"{PN} vs {SN} (mirror)")
json.dump(dash, open(sys.argv[2], "w", encoding="utf-8"), ensure_ascii=False, indent=1)
print("panels:", len(panels))
