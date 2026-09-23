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
helm dependency build "$CHART"
