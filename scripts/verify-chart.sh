#!/usr/bin/env bash
# Chart validation — tests T-7.1 through T-7.4 (ADR-007 §6).
#
# Run by `make lint-helm` and by CI. These assertions are the reason the chart is trustworthy;
# `helm lint` alone would pass a chart with a wildcard ClusterRole.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$REPO_ROOT/charts/topology-visualizer"
VALUES="$CHART/ci/kind-values.yaml"
RELEASE="topology"

pass=0
fail=0

ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }

render() { helm template "$RELEASE" "$CHART" -f "$VALUES" "$@" 2>/dev/null; }

# A values override that must be rejected by values.schema.json.
reject() {
  local desc="$1"; shift
  if helm template "$RELEASE" "$CHART" -f "$VALUES" "$@" >/dev/null 2>&1; then
    bad "schema should reject: $desc"
  else
    ok "schema rejects: $desc"
  fi
}

echo "== T-7.1: helm lint =="
if helm lint "$CHART" >/dev/null 2>&1; then ok "helm lint"; else bad "helm lint"; helm lint "$CHART"; fi

echo "== T-7.2: renders valid YAML =="
RENDERED="$(render)"
if [[ -n "$RENDERED" ]]; then
  ok "helm template produced output"
  if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null; then
    if printf '%s' "$RENDERED" | python3 -c 'import sys,yaml; list(yaml.safe_load_all(sys.stdin))' 2>/dev/null; then
      ok "rendered output parses as YAML"
    else
      bad "rendered output is not valid YAML"
    fi
  else
    echo "  SKIP yaml parse check (python3 yaml module unavailable)"
  fi
  count=$(printf '%s' "$RENDERED" | grep -c '^kind:')
  ok "rendered $count resources"
else
  bad "helm template produced no output"
fi

echo "== T-7.4: least-privilege RBAC =="
CLUSTERROLE="$(printf '%s' "$RENDERED" | awk '/^kind: ClusterRole$/,/^---$/')"

if printf '%s' "$CLUSTERROLE" | grep -qE '"\*"'; then
  bad "ClusterRole contains a wildcard"
else
  ok "no wildcard in ClusterRole"
fi

if printf '%s' "$CLUSTERROLE" | grep -q 'verbs:' && \
   ! printf '%s' "$CLUSTERROLE" | grep 'verbs:' | grep -qvE '\["get", "list", "watch"\]'; then
  ok "agent verbs are exactly get/list/watch"
else
  bad "agent ClusterRole has verbs beyond get/list/watch"
  printf '%s' "$CLUSTERROLE" | grep 'verbs:'
fi

for res in pods services namespaces replicasets deployments statefulsets daemonsets jobs endpointslices; do
  if printf '%s' "$CLUSTERROLE" | grep -q "$res"; then
    ok "watches $res"
  else
    bad "missing RBAC for $res"
  fi
done

# Backend and frontend must hold no Kubernetes API credentials at all.
tokens=$(printf '%s' "$RENDERED" | grep -c 'automountServiceAccountToken: false')
if [[ "$tokens" -ge 2 ]]; then
  ok "backend and frontend do not mount a ServiceAccount token"
else
  bad "expected 2 workloads with automountServiceAccountToken:false, found $tokens"
fi

echo "== D-7.5: CLUSTER_ID has one source =="
# Parsed, not grepped: a neighbouring ConfigMap key would otherwise look like a second value.
CID_OUT="$(printf '%s' "$RENDERED" | python3 "$REPO_ROOT/scripts/check_cluster_id.py" 2>&1)"
if [[ "$CID_OUT" == OK* ]]; then
  ok "agent and backend share one CLUSTER_ID (${CID_OUT#OK single CLUSTER_ID: })"
else
  bad "$CID_OUT"
fi

echo "== T-7.3: values.schema.json rejects malformed values =="
reject "empty clusterId"                     --set clusterId=""
reject "clusterId containing ':'"            --set clusterId="bad:id"
reject "CORS wildcard"                       --set backend.corsAllowedOrigins='*'
reject "in-cluster database with no password" --set postgresql.enabled=true --set postgresql.mode=internal
reject "multi-replica backend (HA deferred)" --set backend.replicaCount=3
reject "invalid image pullPolicy"            --set agent.image.pullPolicy=Sometimes
reject "port out of range"                   --set backend.service.port=99999

if render --set postgresql.enabled=true --set postgresql.mode=internal \
          --set postgresql.auth.password=s3cret >/dev/null 2>&1; then
  ok "valid database configuration still accepted"
else
  bad "schema rejects a valid database configuration"
fi

echo
echo "chart verification: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
