#!/bin/sh
# Import (or overwrite) the generated dashboard into an EXISTING Grafana through its HTTP API.
#
#   GRAFANA_URL=https://grafana.example.com GRAFANA_TOKEN=<service-account token, Editor> \
#     ./import.sh dashboard.json
#
# For the "Differences" table install the Infinity plugin (yesoreyeram-infinity-datasource) and
# create a datasource with uid = config "infinity_uid" (default shadow-infinity).
set -eu
: "${GRAFANA_URL:?}" "${GRAFANA_TOKEN:?}"
FILE=${1:-dashboard.json}
python3 - "$FILE" <<'PY' | curl -sS -X POST "$GRAFANA_URL/api/dashboards/db" \
  -H "Authorization: Bearer $GRAFANA_TOKEN" -H "Content-Type: application/json" --data-binary @-
import json, sys
print(json.dumps({"dashboard": json.load(open(sys.argv[1], encoding="utf-8")), "overwrite": True}))
PY
echo
