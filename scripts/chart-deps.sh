#!/usr/bin/env bash
# Fetch the chart's declared dependencies if they are not already in charts/ (ADR-013 D-13.5).
#
# `helm template` and `helm install` refuse a chart whose Chart.yaml declares dependencies that
# are missing from charts/, EVEN when their condition is false. So every script and Make target
# that renders or installs the chart goes through here first. Idempotent: with the archives
# present nothing is fetched, and `Chart.lock` (committed) pins what is fetched.
#
# This is the one network fetch a clean checkout needs before the chart can be rendered at all.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${1:-$REPO_ROOT/charts/topology-visualizer}"

declared=$(python3 - "$CHART/Chart.yaml" <<'EOF'
import sys, yaml
chart = yaml.safe_load(open(sys.argv[1]))
print(len(chart.get("dependencies") or []))
EOF
)

present=0
if [[ -d "$CHART/charts" ]]; then
  present=$(find "$CHART/charts" -maxdepth 1 -name '*.tgz' | wc -l)
fi

if [[ "$present" -ge "$declared" ]]; then
  exit 0
fi

echo "chart-deps: fetching $declared chart dependencies into $CHART/charts (needs the network once)"

# `helm dependency build` resolves a repository URL only through a repo already added with
# `helm repo add`; on a fresh machine or CI runner it fails with "no repository definition".
# Register each declared repository that is not known yet, under a name of our own so an
# operator's existing repo names are never overwritten.
known=$(helm repo list -o json 2>/dev/null || echo "[]")
python3 - "$CHART/Chart.yaml" "$known" <<'EOF_PY' | while read -r name url; do
import json, sys, yaml
chart = yaml.safe_load(open(sys.argv[1]))
known = {r["url"].rstrip("/") for r in json.loads(sys.argv[2] or "[]")}
for dep in chart.get("dependencies") or []:
    url = (dep.get("repository") or "").rstrip("/")
    if url.startswith(("http://", "https://")) and url not in known:
        known.add(url)
        print(f"topology-dep-{dep['name']} {url}")
EOF_PY
  echo "chart-deps: adding chart repository $url"
  helm repo add "$name" "$url" >/dev/null
done

helm dependency build "$CHART"
