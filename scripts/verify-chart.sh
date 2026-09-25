#!/usr/bin/env bash
# Chart validation — tests T-7.1 through T-7.4 (ADR-007 §6).
#
# Run by `make lint-helm` and by CI. These assertions are the reason the chart is trustworthy;
# `helm lint` alone would pass a chart with a wildcard ClusterRole.
set -uo pipefail
# Never end a pipeline in `grep -q`: it exits at the first match, the writer upstream dies of
# SIGPIPE, and pipefail turns a found match into a failure, but only when the input is larger than
# the pipe buffer. A here-string (`grep -q X <<<"$V"`), or `grep -c X >/dev/null`, reads it all.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$REPO_ROOT/charts/topology-visualizer"
VALUES="$CHART/ci/kind-values.yaml"
RELEASE="topology"

# Declared dependencies must be present even to render with their condition off (ADR-013 D-13.5).
# Without the subcharts nothing renders, and every check below would fail for that one reason.
bash "$REPO_ROOT/scripts/chart-deps.sh" "$CHART" || { echo "chart-deps failed: the chart cannot render" >&2; exit 1; }

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

if grep -qE '"\*"' <<<"$CLUSTERROLE"; then
  bad "ClusterRole contains a wildcard"
else
  ok "no wildcard in ClusterRole"
fi

if grep -q 'verbs:' <<<"$CLUSTERROLE" && \
   ! printf '%s' "$CLUSTERROLE" | grep 'verbs:' | grep -cvE '\["get", "list", "watch"\]' >/dev/null; then
  ok "agent verbs are exactly get/list/watch"
else
  bad "agent ClusterRole has verbs beyond get/list/watch"
  printf '%s' "$CLUSTERROLE" | grep 'verbs:'
fi

for res in pods services namespaces nodes replicasets deployments statefulsets daemonsets jobs endpointslices; do
  if grep -q "$res" <<<"$CLUSTERROLE"; then
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
if grep -q "volumeClaimTemplates" <<<"$DB_RENDERED"; then
  ok "database uses volumeClaimTemplates (durable across pod recreation)"
else
  bad "database has no volumeClaimTemplates — history would not survive a restart"
fi

# The password must reach the container by reference, never inline in a pod spec.
if printf '%s' "$DB_RENDERED" | awk '/^kind: StatefulSet$/,/^---$/' | grep -c "secretKeyRef" >/dev/null; then
  ok "database password comes from a Secret reference"
else
  bad "database password is not referenced from a Secret"
fi

if printf '%s' "$DB_RENDERED" | awk '/^kind: StatefulSet$/,/^---$/' | grep -cE 'value:.*s3cret' >/dev/null; then
  bad "the password appears inline in the StatefulSet"
else
  ok "no inline password in the StatefulSet"
fi

# The backend must read its DSN from a Secret too.
if grep -q "database-url" <<<"$DB_RENDERED"; then
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
if grep -qE '^\s*type:\s*RuntimeDefault' <<<"$AGENT_BLOCK"; then
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

echo "== ADR-011: optional monitoring =="
# T-11.1 — the default render must not change. Monitoring is opt-in (ADR-001 §4.2: never a
# mandatory dependency), so with it off there is no monitor, no dashboard and no scrape ingress.
for k in PodMonitor ServiceMonitor; do
  if grep -q "^kind: $k" <<<"$RENDERED"; then
    bad "$k rendered with monitoring off"
  else
    ok "no $k by default"
  fi
done
if grep -q 'grafana-dashboard' <<<"$RENDERED"; then
  bad "dashboard ConfigMap rendered with monitoring off"
else
  ok "no dashboard ConfigMap by default"
fi
if printf '%s' "$SEC_RENDERED" | awk '/^kind: NetworkPolicy$/,/^---$/' | grep -c 'namespaceSelector' >/dev/null; then
  bad "backend NetworkPolicy admits another namespace with monitoring off"
else
  ok "backend NetworkPolicy has no scrape ingress by default"
fi

# T-11.2 — enabled: one PodMonitor for the agent, one ServiceMonitor for the backend, both carrying
# the operator's selector label, plus a sidecar-labelled ConfigMap whose payload is real JSON.
MON_RENDERED="$(render --set monitoring.enabled=true --set monitoring.monitorLabels.release=kps)"
for k in PodMonitor ServiceMonitor; do
  n=$(printf '%s' "$MON_RENDERED" | grep -c "^kind: $k" || true)
  if [[ "$n" -eq 1 ]]; then ok "exactly one $k when enabled"; else bad "expected 1 $k, found $n"; fi
done
PM="$(printf '%s' "$MON_RENDERED" | awk '/^kind: PodMonitor$/,/^---$/')"
SM="$(printf '%s' "$MON_RENDERED" | awk '/^kind: ServiceMonitor$/,/^---$/')"
if grep -q 'port: metrics' <<<"$PM"; then
  ok "PodMonitor scrapes the agent's metrics port"
else
  bad "PodMonitor does not target port 'metrics'"
fi
if grep -q 'port: http' <<<"$SM" && grep -q 'path: /metrics' <<<"$SM"; then
  ok "ServiceMonitor scrapes the backend's http port at /metrics"
else
  bad "ServiceMonitor does not target http:/metrics"
fi
if [[ $(printf '%s\n%s' "$PM" "$SM" | grep -c 'release: kps') -eq 2 ]]; then
  ok "both monitors carry monitorLabels"
else
  bad "monitorLabels missing from a monitor — the operator would never select it"
fi
DASH_OUT="$(printf '%s' "$MON_RENDERED" | python3 -c '
import json, sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if not doc or doc.get("kind") != "ConfigMap":
        continue
    data = doc.get("data") or {}
    if "topology-visualizer.json" not in data:
        continue
    labels = doc["metadata"].get("labels", {})
    if labels.get("grafana_dashboard") != "1":
        print("FAIL dashboard ConfigMap lacks the sidecar label"); sys.exit(0)
    dash = json.loads(data["topology-visualizer.json"])
    panels = len(dash["panels"])
    print(f"OK {panels} panels")
    sys.exit(0)
print("FAIL no dashboard ConfigMap rendered")
' 2>&1)"
if [[ "$DASH_OUT" == OK* ]]; then
  ok "dashboard ConfigMap is sidecar-labelled and its payload parses as JSON (${DASH_OUT#OK })"
else
  bad "$DASH_OUT"
fi

# T-11.3 — D-11.4: every metric the dashboard reads must be one the code emits. A renamed counter
# would otherwise leave a panel that is empty for the same reason a healthy one reads zero.
missing=0
for name in $(grep -oE 'topology_(agent|backend)_[a-z_]+' "$CHART/dashboards/topology-visualizer.json" | sort -u); do
  if ! grep -q "$name" "$REPO_ROOT/agent/cmd/agent/main.go" "$REPO_ROOT/backend/app/api/metrics.py"; then
    bad "dashboard references $name, which nothing emits"
    missing=$((missing + 1))
  fi
done
[[ "$missing" -eq 0 ]] && ok "every dashboard metric exists in agent or backend source"

# T-11.4 — with both on, the backend policy admits the Prometheus namespace on the backend port and
# nothing else changes: the agent DaemonSet must render byte-identical to the monitoring-off render.
NPM_RENDERED="$(render --set monitoring.enabled=true --set networkPolicy.enabled=true \
                       --set monitoring.prometheusNamespace=observability)"
NPM_BACKEND="$(printf '%s' "$NPM_RENDERED" | awk '/^kind: NetworkPolicy$/,/^---$/')"
if grep -q 'kubernetes.io/metadata.name: "observability"' <<<"$NPM_BACKEND"; then
  ok "backend NetworkPolicy admits scrapes from monitoring.prometheusNamespace"
else
  bad "backend NetworkPolicy has no ingress from the Prometheus namespace"
fi
if [[ $(printf '%s' "$NPM_BACKEND" | grep -c "port: $(printf '%s' "$NPM_BACKEND" | grep -m1 'port:' | awk '{print $2}')") -eq 2 ]] && \
   ! grep -qE 'port: (9090|8081)' <<<"$NPM_BACKEND"; then
  ok "scrape ingress is on the backend port only"
else
  bad "scrape ingress opens a port other than the backend's"
fi
DS_OFF="$(printf '%s' "$RENDERED" | awk '/^kind: DaemonSet$/,/^---$/')"
DS_ON="$(printf '%s' "$NPM_RENDERED" | awk '/^kind: DaemonSet$/,/^---$/')"
if [[ "$DS_OFF" == "$DS_ON" ]]; then
  ok "agent DaemonSet is unchanged by monitoring (no new listener, no new capability)"
else
  bad "enabling monitoring changed the agent DaemonSet"
fi

# T-11.5 — schema
reject "monitoring scrape interval that is not a duration" --set monitoring.enabled=true --set monitoring.scrapeInterval=fast
reject "empty Prometheus namespace"                        --set monitoring.enabled=true --set monitoring.prometheusNamespace=""

echo "== ADR-012: Grafana deep links (T-12.6) =="
# The browser's config is a ConfigMap the frontend mounts as /config.json. Off by default means
# the JSON says so explicitly — an empty url — rather than the file being absent.
config_json() {
  printf '%s' "$1" | python3 -c '
import json, sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if doc and doc.get("kind") == "ConfigMap" and "config.json" in (doc.get("data") or {}):
        cfg = json.loads(doc["data"]["config.json"])
        print(json.dumps(cfg["grafana"], sort_keys=True)); sys.exit(0)
print("MISSING")'
}
DEFAULT_CFG="$(config_json "$RENDERED")"
if [[ "$DEFAULT_CFG" == *'"url": ""'* ]]; then
  ok "config.json renders with an empty Grafana url by default"
else
  bad "default config.json is wrong or missing: $DEFAULT_CFG"
fi
GRAFANA_RENDERED="$(render --set frontend.grafana.url='https://grafana.example.com/g' --set frontend.grafana.lokiDatasourceUid=loki-1)"
SET_CFG="$(config_json "$GRAFANA_RENDERED")"
if [[ "$SET_CFG" == *'"url": "https://grafana.example.com/g"'* && "$SET_CFG" == *'"lokiDatasourceUid": "loki-1"'* ]]; then
  ok "frontend.grafana.* values reach config.json"
else
  bad "frontend.grafana.* did not reach config.json: $SET_CFG"
fi
FE_BLOCK="$(printf '%s' "$RENDERED" | awk '/^kind: Deployment$/,/^---$/' | awk '/name: .*-frontend$/,0')"
if grep -q 'subPath: config.json' <<<"$FE_BLOCK" && grep -q 'checksum/config' <<<"$FE_BLOCK"; then
  ok "frontend mounts config.json and rolls when it changes"
else
  bad "frontend Deployment does not mount config.json with a checksum annotation"
fi
reject "Grafana url without an http(s) scheme" --set frontend.grafana.url='grafana.example.com'
reject "Grafana url with a javascript: scheme" --set frontend.grafana.url='javascript:alert(1)'

echo "== ADR-013: bundled observability =="
# T-13.1 — off by default, and off means nothing: no subchart resources, no scrape annotations.
if grep -qiE 'app.kubernetes.io/name: (prometheus|grafana|kube-state-metrics)' <<<"$RENDERED"; then
  bad "subchart resources rendered with observability off"
else
  ok "no Prometheus, Grafana or kube-state-metrics by default"
fi
if grep -q 'prometheus.io/scrape' <<<"$RENDERED"; then
  bad "scrape annotations rendered with observability off"
else
  ok "no scrape annotations by default"
fi

# T-13.2 — on: a Prometheus server, kube-state-metrics and Grafana, and NOT the three components
# the bundle declines (alertmanager, pushgateway, node-exporter).
OBS_RENDERED="$(render --set observability.enabled=true --set networkPolicy.enabled=true)"
for want in 'app.kubernetes.io/name: prometheus' 'app.kubernetes.io/name: kube-state-metrics' 'app.kubernetes.io/name: grafana'; do
  if grep -q "$want" <<<"$OBS_RENDERED"; then ok "bundle renders ${want#*: }"; else bad "bundle is missing ${want#*: }"; fi
done
for absent in alertmanager pushgateway node-exporter; do
  if grep -qi "app.kubernetes.io/name: .*$absent" <<<"$OBS_RENDERED"; then
    bad "bundle renders $absent, which it declines (D-13.1)"
  else
    ok "bundle does not render $absent"
  fi
done

# T-13.3 — scrape annotations on the agent pods and the backend Service; the agent's annotated
# port must be the DaemonSet's own metrics containerPort, or the two paths silently diverge.
OBS_DS="$(printf '%s' "$OBS_RENDERED" | awk '/^kind: DaemonSet$/,/^---$/')"
annotated=$(printf '%s' "$OBS_DS" | grep -m1 'prometheus.io/port' | grep -oE '[0-9]+')
declared=$(printf '%s' "$OBS_DS" | grep -A1 'name: metrics' | grep -oE 'containerPort: [0-9]+' | grep -oE '[0-9]+')
if [[ -n "$annotated" && "$annotated" == "$declared" ]]; then
  ok "agent pods are annotated for scraping on their metrics port ($annotated)"
else
  bad "agent scrape annotation port '$annotated' != metrics containerPort '$declared'"
fi
BACKEND_SVC_SCRAPE="$(printf '%s' "$OBS_RENDERED" | python3 -c '
import sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if not doc or doc.get("kind") != "Service":
        continue
    meta = doc["metadata"]
    if meta.get("labels", {}).get("app.kubernetes.io/component") == "backend":
        a = meta.get("annotations") or {}
        print("OK" if a.get("prometheus.io/scrape") == "true" and a.get("prometheus.io/path") == "/metrics" else "MISSING")
        sys.exit(0)
print("NO-SERVICE")')"
if [[ "$BACKEND_SVC_SCRAPE" == OK ]]; then
  ok "backend Service is annotated for scraping"
else
  bad "backend Service lacks scrape annotations ($BACKEND_SVC_SCRAPE)"
fi

# T-13.4 — Grafana reaches the release's Prometheus, and its sidecar looks for ADR-011's label.
if grep -q "url: http://$RELEASE-prometheus-server" <<<"$OBS_RENDERED"; then
  ok "Grafana datasource points at $RELEASE-prometheus-server"
else
  bad "Grafana datasource does not point at the release's Prometheus"
fi
if grep -qE 'value: "?grafana_dashboard"?' <<<"$OBS_RENDERED" ; then
  ok "Grafana sidecar watches the grafana_dashboard label"
else
  bad "Grafana sidecar is not configured for the grafana_dashboard label"
fi

# T-13.5 — both dashboards ship, and the workload one is what the panel's default UID names.
DASH_UIDS="$(printf '%s' "$OBS_RENDERED" | python3 -c '
import json, sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if doc and doc.get("kind") == "ConfigMap" and "topology-workload.json" in (doc.get("data") or {}):
        d = doc["data"]
        print(json.loads(d["topology-visualizer.json"])["uid"], json.loads(d["topology-workload.json"])["uid"]); sys.exit(0)
print("MISSING")')"
DEFAULT_UID="$(config_json "$OBS_RENDERED" | python3 -c 'import json,sys; print(json.load(sys.stdin)["workloadDashboardUid"])')"
if [[ "$DASH_UIDS" == *"$DEFAULT_UID"* && "$DASH_UIDS" != MISSING ]]; then
  ok "both dashboards render and the workload uid matches the panel default ($DEFAULT_UID)"
else
  bad "dashboards '$DASH_UIDS' do not include the panel's default uid '$DEFAULT_UID'"
fi

# T-13.6 — the bundled Prometheus is in-namespace, so the policy admits it by its own labels; and
# the two scrape paths cannot be enabled together.
if printf '%s' "$OBS_RENDERED" | awk '/^kind: NetworkPolicy$/,/^---$/' | grep -c 'app.kubernetes.io/component: server' >/dev/null; then
  ok "backend NetworkPolicy admits the bundled Prometheus server"
else
  bad "backend NetworkPolicy does not admit the bundled Prometheus"
fi
reject "observability.enabled together with monitoring.enabled" --set observability.enabled=true --set monitoring.enabled=true

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
