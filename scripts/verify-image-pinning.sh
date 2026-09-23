#!/usr/bin/env bash
# Every third-party image must be pinned by digest — ADR-007 D-7.4, task P5-K10.
#
# A tag is mutable. `postgres:17-alpine` resolves to a different image today than it did last
# month, so a tag alone cannot make a build reproducible and cannot make a scan result mean
# anything: what was scanned is not necessarily what will be pulled.
#
# Run by `make verify-pinning` and by CI.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

pass=0
fail=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }

# The reference prototype is deliberately excluded: ADR-001 §12 instruction 3 forbids modifying it,
# so holding it to this project's standards would be a rule nobody is allowed to satisfy.
EXCLUDE='^\./poc-kind-topology/'

echo "== base images in Dockerfiles =="
while IFS= read -r dockerfile; do
  [[ "$dockerfile" =~ $EXCLUDE ]] && continue
  while IFS= read -r line; do
    # `FROM x AS builder` and `FROM scratch` are both fine; a scratch base has nothing to pin.
    ref="$(awk '{print $2}' <<<"$line")"
    [[ "$ref" == "scratch" ]] && continue
    if [[ "$ref" == *"@sha256:"* ]]; then
      ok "$(basename "$(dirname "$dockerfile")")/$(basename "$dockerfile"): ${ref%%@*} pinned"
    else
      bad "$dockerfile: '$ref' is pinned only by tag, which is mutable"
    fi
  done < <(grep -E '^FROM ' "$dockerfile")
done < <(find . -name 'Dockerfile*' -not -path '*/node_modules/*' -type f)

echo "== images in demo manifests =="
while IFS= read -r manifest; do
  while IFS= read -r ref; do
    if [[ "$ref" == *"@sha256:"* ]]; then
      ok "$(basename "$manifest"): ${ref%%@*} pinned"
    else
      bad "$manifest: '$ref' is pinned only by tag"
    fi
  done < <(grep -oE 'image: [a-z0-9./_-]+:[a-zA-Z0-9._-]+(@sha256:[a-f0-9]{64})?' "$manifest" | sed 's/image: //')
done < <(find demo -name '*.yaml' -type f 2>/dev/null)

echo "== third-party images in the chart =="
# The project's own images are built locally and carry a :dev tag in the kind demo, so they are
# not digest-pinned — there is no registry to pin them against until they are published (P5-K11).
if grep -qE '^\s+digest: "sha256:[a-f0-9]{64}"' charts/topology-visualizer/values.yaml; then
  ok "postgresql image carries a digest"
else
  bad "postgresql image has no digest in values.yaml"
fi

bash scripts/chart-deps.sh charts/topology-visualizer
RENDERED="$(helm template pin charts/topology-visualizer \
  --set clusterId=c1 --set postgresql.enabled=true --set postgresql.auth.password=x 2>/dev/null)"
if printf '%s' "$RENDERED" | grep -E 'image: "postgres' | grep -q '@sha256:'; then
  ok "the rendered database image resolves to a digest"
else
  bad "the rendered database image has no digest"
fi

echo "== bundled observability images (ADR-013 D-13.6, T-13.7) =="
# Everything the optional bundle would pull, checked the same way. The project's own images carry
# a :dev tag and are side-loaded, so they are the only ones excused.
OBS_RENDERED="$(helm template pin charts/topology-visualizer --set clusterId=c1 \
  --set observability.enabled=true 2>/dev/null)"
count=0
while IFS= read -r ref; do
  [[ "$ref" == topology-* ]] && continue
  count=$((count + 1))
  if [[ "$ref" == *"@sha256:"* ]]; then
    ok "bundle: ${ref%%@*} pinned"
  else
    bad "bundle: '$ref' is pinned only by tag"
  fi
done < <(printf '%s' "$OBS_RENDERED" | grep -oE '^\s+(- )?image: "?[^" ]+' | sed -E 's/.*image: "?//' | sort -u)
if [[ "$count" -ge 5 ]]; then
  ok "bundle renders $count third-party images (expected Prometheus, reloader, kube-state-metrics, Grafana, sidecar)"
else
  bad "bundle renders only $count third-party images; expected at least 5"
fi

echo
echo "image pinning: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
