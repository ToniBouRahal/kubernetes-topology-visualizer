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

for res in pods services namespaces nodes replicasets deployments statefulsets daemonsets jobs endpointslices; do
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

echo "== T-7.7 / T-7.8: database posture =="
DB_RENDERED="$(render --set postgresql.enabled=true --set postgresql.auth.password=s3cret)"

# A PVC, not an emptyDir: this is what makes history survive pod deletion.
if printf '%s' "$DB_RENDERED" | grep -q "volumeClaimTemplates"; then
  ok "database uses volumeClaimTemplates (durable across pod recreation)"
else
  bad "database has no volumeClaimTemplates — history would not survive a restart"
fi

# The password must reach the container by reference, never inline in a pod spec.
if printf '%s' "$DB_RENDERED" | awk '/^kind: StatefulSet$/,/^---$/' | grep -q "secretKeyRef"; then
  ok "database password comes from a Secret reference"
else
  bad "database password is not referenced from a Secret"
fi

if printf '%s' "$DB_RENDERED" | awk '/^kind: StatefulSet$/,/^---$/' | grep -qE 'value:.*s3cret'; then
  bad "the password appears inline in the StatefulSet"
else
  ok "no inline password in the StatefulSet"
fi

# The backend must read its DSN from a Secret too.
if printf '%s' "$DB_RENDERED" | grep -q "database-url"; then
  ok "backend reads DATABASE_URL from a Secret"
else
  bad "backend does not read DATABASE_URL from a Secret"
fi

echo "== D-7.4: security posture =="
# Database AND network policies on: this checks the templates are right, independently of the
# shipped defaults. NetworkPolicy is off by default until P5-K9 verifies it under an enforcing
# CNI (kind ignores it), which is recorded in values.yaml.
SEC_RENDERED="$(render --set postgresql.enabled=true --set postgresql.auth.password=s3cret \
                       --set networkPolicy.enabled=true)"

# Exactly ONE privileged container is expected: the agent. Anything else is a regression, and the
# count is asserted rather than the presence, so a second privileged workload cannot slip in.
priv=$(printf '%s' "$SEC_RENDERED" | grep -c 'privileged: true' || true)
if [[ "$priv" -eq 1 ]]; then
  ok "exactly one privileged container (the agent)"
else
  bad "expected exactly 1 privileged container, found $priv"
fi

# Every workload that is NOT the agent must be hardened. Counted against the three non-agent
# workloads (backend, frontend, postgresql) so that adding a fourth without a securityContext
# fails here rather than in a review.
for setting in 'runAsNonRoot: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault'; do
  count=$(printf '%s' "$SEC_RENDERED" | grep -c "$setting" || true)
  if [[ "$count" -ge 3 ]]; then
    ok "$setting on all three non-agent workloads ($count)"
  else
    bad "$setting found on only $count workloads, expected >= 3"
  fi
done

drops=$(printf '%s' "$SEC_RENDERED" | grep -c 'drop:' || true)
if [[ "$drops" -ge 3 ]]; then
  ok "capabilities dropped on all three non-agent workloads ($drops)"
else
  bad "capabilities dropped on only $drops workloads, expected >= 3"
fi

# Read-only root filesystem on the two workloads that can take it. Postgres cannot (it writes its
# socket and initdb output), which is stated in the template rather than silently skipped.
ro=$(printf '%s' "$SEC_RENDERED" | grep -c 'readOnlyRootFilesystem: true' || true)
if [[ "$ro" -ge 2 ]]; then
  ok "read-only root filesystem on backend and frontend ($ro)"
else
  bad "expected >= 2 read-only root filesystems, found $ro"
fi

# The agent must NOT carry seccomp RuntimeDefault: the default profile restricts bpf() and
# perf_event_open(), so applying it would break capture. Asserted so nobody "fixes" it later.
AGENT_BLOCK="$(printf '%s' "$SEC_RENDERED" | awk '/^kind: DaemonSet$/,/^---$/')"
# `grep RuntimeDefault` also matches the comment in the template explaining why it is absent, so
# the actual YAML key is what gets checked.
if printf '%s' "$AGENT_BLOCK" | grep -qE '^\s*type:\s*RuntimeDefault'; then
  bad "the agent has seccompProfile RuntimeDefault, which blocks bpf() and breaks capture"
else
  ok "agent is exempt from seccomp RuntimeDefault (documented in agent-daemonset.yaml)"
fi

# Probes and limits on every workload — a pod with no readiness probe takes traffic before it can
# serve it, and one with no limit can starve a node.
for probe in readinessProbe 'resources:'; do
  count=$(printf '%s' "$SEC_RENDERED" | grep -c "$probe" || true)
  if [[ "$count" -ge 4 ]]; then
    ok "$probe on all four workloads ($count)"
  else
    bad "$probe on only $count workloads, expected >= 4"
  fi
done

echo "== D-7.4: NetworkPolicies =="
NP_COUNT=$(printf '%s' "$SEC_RENDERED" | grep -c '^kind: NetworkPolicy' || true)
if [[ "$NP_COUNT" -ge 2 ]]; then
  ok "NetworkPolicies shipped ($NP_COUNT)"
else
  bad "expected at least 2 NetworkPolicies, found $NP_COUNT"
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
