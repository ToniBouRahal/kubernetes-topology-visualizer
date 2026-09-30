#!/usr/bin/env bash
# Every third-party image must be pinned by digest — ADR-007 D-7.4, task P5-K10.
#
# A tag is mutable. `postgres:17-alpine` resolves to a different image today than it did last
# month, so a tag alone cannot make a build reproducible and cannot make a scan result mean
# anything: what was scanned is not necessarily what will be pulled.
#
# Run by `make verify-pinning` and by CI.
set -uo pipefail
# Never end a pipeline in `grep -q`: it exits at the first match, the writer upstream dies of
# SIGPIPE, and pipefail turns a found match into a failure, but only when the input is larger than
# the pipe buffer. A here-string (`grep -q X <<<"$V"`), or `grep -c X >/dev/null`, reads it all.

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
# Sign-in is on by default and needs a URL and a provider before the chart renders at all
# (ADR-014 D-14.1); the bundled Dex supplies the provider, and its images are checked below.
AUTH_ARGS=(--set auth.externalUrl=https://topology.example --set auth.dex.enabled=true)
RENDERED="$(helm template pin charts/topology-visualizer "${AUTH_ARGS[@]}" \
  --set clusterId=c1 --set postgresql.enabled=true --set postgresql.auth.password=x 2>/dev/null)"
if printf '%s' "$RENDERED" | grep -E 'image: "postgres' | grep -c '@sha256:' >/dev/null; then
  ok "the rendered database image resolves to a digest"
else
  bad "the rendered database image has no digest"
fi

echo "== sign-in images (ADR-014 D-14.6, D-14.7) =="
for want in "quay.io/oauth2-proxy/oauth2-proxy" "ghcr.io/dexidp/dex"; do
  ref="$(printf '%s' "$RENDERED" | grep -oE "image: \"?${want}[^\" ]*" | head -1 | sed -E 's/image: "?//')"
  if [[ -z "$ref" ]]; then
    bad "$want is not rendered with sign-in on"
  elif [[ "$ref" == *"@sha256:"* ]]; then
    ok "$want pinned by digest"
  else
    bad "$want is pinned only by tag: $ref"
  fi
done

echo "== bundled observability images (ADR-013 D-13.6, T-13.7) =="
# Everything the optional bundle would pull, checked the same way. The project's own images carry
# a :dev tag and are side-loaded, so they are the only ones excused.
OBS_RENDERED="$(helm template pin charts/topology-visualizer --set clusterId=c1 "${AUTH_ARGS[@]}" \
  --set observability.enabled=true 2>/dev/null)"
count=0
while IFS= read -r ref; do
  [[ "$ref" == topology-* ]] && continue
  # Checked in their own section above.
  [[ "$ref" == quay.io/oauth2-proxy/* || "$ref" == ghcr.io/dexidp/* ]] && continue
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
