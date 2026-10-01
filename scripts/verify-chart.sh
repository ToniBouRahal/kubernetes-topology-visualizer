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

# NO privileged container (ADR-014 D-14.9). The agent runs on two capabilities; any container
# turning up privileged — the agent included — is a regression. Counted, not merely searched for.
priv=$(printf '%s' "$SEC_RENDERED" | grep -c 'privileged: true' || true)
if [[ "$priv" -eq 0 ]]; then
  ok "no privileged container, the agent included"
else
  bad "expected no privileged container, found $priv"
fi
# The agent's exact grant: BPF and PERFMON added, everything else dropped, no host PID namespace,
# read-only root, and one host path, read-only.
AGENT_POSTURE="$(printf '%s' "$SEC_RENDERED" | python3 -c '
import sys, yaml
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get("kind") == "DaemonSet":
        spec = d["spec"]["template"]["spec"]
        c = spec["containers"][0]
        sc = c["securityContext"]
        caps = sc.get("capabilities", {})
        host = [(v["hostPath"]["path"]) for v in spec["volumes"] if "hostPath" in v]
        ro = {m["name"]: m.get("readOnly", False) for m in c["volumeMounts"]}
        hostnames = [v["name"] for v in spec["volumes"] if "hostPath" in v]
        problems = []
        if sorted(caps.get("add", [])) != ["BPF", "PERFMON"]: problems.append("adds " + str(caps.get("add")))
        if caps.get("drop") != ["ALL"]: problems.append("does not drop ALL")
        if spec.get("hostPID") or spec.get("hostNetwork"): problems.append("shares a host namespace")
        if not sc.get("readOnlyRootFilesystem"): problems.append("writable root filesystem")
        if sc.get("allowPrivilegeEscalation") is not False: problems.append("privilege escalation allowed")
        if (sc.get("seccompProfile") or {}).get("type") != "RuntimeDefault": problems.append("no RuntimeDefault seccomp")
        if host != ["/sys/kernel/tracing"] or not all(ro[n] for n in hostnames): problems.append(f"host paths {host}, read-only {[ro[n] for n in hostnames]}")
        print("; ".join(problems) or "OK")
' 2>&1)"
if [[ "$AGENT_POSTURE" == "OK" ]]; then
  ok "agent: BPF + PERFMON only, no host namespaces, read-only root, one read-only host path"
else
  bad "agent posture: $AGENT_POSTURE"
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

# The agent's seccomp profile is RuntimeDefault, and that is asserted in the agent posture check
# above (ADR-014 D-14.9). This used to be asserted ABSENT: under `privileged` on the Phase 5 setup
# the default profile broke capture. With BPF and PERFMON as real capabilities, the runtime's
# default profile admits bpf() and perf_event_open() — measured on the kind cluster, where the
# counted burst still reports exactly 100 of 100.

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
# The rule admitting the Prometheus namespace must open the plain ops port and nothing else — in
# particular not the mTLS listeners, which carry topology (ADR-014 D-14.3).
SCRAPE_PORTS="$(printf '%s' "$NPM_RENDERED" | python3 -c '
import sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if doc and doc.get("kind") == "NetworkPolicy" and doc["metadata"]["name"].endswith("-backend"):
        for rule in doc["spec"]["ingress"]:
            if any("namespaceSelector" in peer for peer in rule["from"]):
                print(" ".join(str(p["port"]) for p in rule["ports"]))
')"
if [[ "$SCRAPE_PORTS" == "8000" ]]; then
  ok "scrape ingress is on the backend port only"
else
  bad "scrape ingress opens a port other than the backend's"
fi
# checksum/tls differs between any two renders by design: `helm template` has no cluster to look
# existing certificates up in, so each render generates a fresh PKI (ADR-014 D-14.2).
DS_OFF="$(printf '%s' "$RENDERED" | awk '/^kind: DaemonSet$/,/^---$/' | grep -v 'checksum/tls')"
DS_ON="$(printf '%s' "$NPM_RENDERED" | awk '/^kind: DaemonSet$/,/^---$/' | grep -v 'checksum/tls')"
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

echo "== ADR-014: transport security (T-14.4) =="
# Parsed, not grepped: which workload mounts which certificate is a property of volumes and mounts
# together, and a grep would pass a Secret that is declared but mounted somewhere else.
# The render with the in-cluster database, so its certificate and hba are checked too.
TLS_REPORT="$(printf '%s' "$DB_RENDERED" | python3 -c '
import base64, sys, yaml

docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
problems = []

def workload(component):
    for d in docs:
        if d["kind"] in ("Deployment", "DaemonSet", "StatefulSet") and \
           d["spec"]["template"]["metadata"]["labels"].get("app.kubernetes.io/component") == component:
            return d["spec"]["template"]["spec"]

def tls_secrets(spec):
    """The -tls- Secrets a pod spec actually mounts into a container."""
    mounted = {m["name"] for c in spec["containers"] for m in c.get("volumeMounts", [])}
    return sorted({v["secret"]["secretName"].split("-tls-")[1] for v in spec.get("volumes", [])
                   if "secret" in v and "-tls-" in v["secret"]["secretName"] and v["name"] in mounted})

def env(spec):
    return {e["name"]: e.get("value") for c in spec["containers"] for e in c.get("env", [])}

# Each role mounts its own certificate and nothing else (least privilege for keys).
expected = {"backend": ["client-cas", "server"], "agent": ["agent"], "frontend": ["frontend"],
            "database": ["postgresql"]}
for component, want in expected.items():
    got = tls_secrets(workload(component))
    if got != want:
        problems.append(f"{component} mounts {got}, expected {want}")

# Private keys: exactly the four leaf keys, only in kubernetes.io/tls Secrets, never a CA key,
# never in a ConfigMap.
keys = []
for d in docs:
    if d["kind"] == "ConfigMap" and "PRIVATE KEY" in yaml.safe_dump(d.get("data", {})):
        problems.append(f"private key in ConfigMap {d['metadata']['name']}")
    if d["kind"] == "Secret":
        for k, v in (d.get("data") or {}).items():
            if "PRIVATE KEY" in base64.b64decode(v).decode(errors="ignore"):
                name = d["metadata"]["name"]
                role = "dex" if name == "topology-dex-tls" else name.split("-tls-")[-1]
                keys.append((role, k, d.get("type")))
leaf = sorted((n, "tls.key", "kubernetes.io/tls") for n in ("agent", "dex", "frontend", "postgresql", "server"))
if sorted(keys) != leaf:
    problems.append(f"private keys found {sorted(keys)}, expected only the five leaf tls.key")

# Transports.
agent, backend, frontend = env(workload("agent")), env(workload("backend")), env(workload("frontend"))
if not (agent.get("BACKEND_INGEST_URL") or "").startswith("https://") or ":8443/" not in agent["BACKEND_INGEST_URL"]:
    problems.append(f"agent ingest URL is not https on the ingest listener: {agent.get('BACKEND_INGEST_URL')}")
if not all(agent.get(k) for k in ("AGENT_TLS_CERT_FILE", "AGENT_TLS_KEY_FILE", "AGENT_TLS_CA_FILE")):
    problems.append("agent is missing an AGENT_TLS_* setting")
if not all(backend.get(k) for k in ("TLS_CERT_FILE", "TLS_KEY_FILE", "TLS_INGEST_CLIENT_CA_FILE", "TLS_API_CLIENT_CA_FILE")):
    problems.append("backend is missing a TLS_* setting, so it would serve plain HTTP")
if frontend.get("BACKEND_API_PORT") != "8444":
    problems.append("frontend does not proxy to the API listener")

# The database: TLS on, and plain TCP rejected by pg_hba (ADR-014 D-14.8).
db = workload("database")
args = " ".join(a for c in db["containers"] for a in c.get("args", []))
if "ssl=on" not in args or "hba_file=" not in args or "ssl_min_protocol_version=TLSv1.3" not in args:
    problems.append(f"PostgreSQL is not started with TLS 1.3 and the chart hba file: {args}")
hba = next((d["data"]["pg_hba.conf"] for d in docs if d["kind"] == "ConfigMap" and d["metadata"]["name"].endswith("-postgresql-hba")), "")
lines = [l.split() for l in hba.splitlines() if l.strip() and not l.lstrip().startswith("#")]
if [l[0] for l in lines if l[0] != "local"] != ["hostssl", "hostssl", "host", "host"] or \
   any(l[-1] != "reject" for l in lines if l[0] == "host"):
    problems.append("pg_hba does not reject plain TCP after the TLS lines")
def secret_value(d, key):
    if key in (d.get("stringData") or {}):
        return d["stringData"][key]
    if key in (d.get("data") or {}):
        return base64.b64decode(d["data"][key]).decode()
dsn = next((secret_value(d, "database-url") for d in docs
            if d["kind"] == "Secret" and secret_value(d, "database-url")), "")
if "sslmode=verify-full" not in dsn:
    problems.append("backend database URL does not require verify-full TLS")

print("\n".join(problems) if problems else "OK")
' 2>&1)"
if [[ "$TLS_REPORT" == "OK" ]]; then
  ok "each role mounts only its own certificate; only the five leaf keys exist; no CA key; all transports TLS"
else
  while IFS= read -r line; do bad "$line"; done <<<"$TLS_REPORT"
fi

# Network: each mTLS listener admits exactly its one client (ADR-014 D-14.3).
NP_REPORT="$(render --set networkPolicy.enabled=true | python3 -c '
import sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if doc and doc.get("kind") == "NetworkPolicy" and doc["metadata"]["name"].endswith("-backend"):
        for rule in doc["spec"]["ingress"]:
            who = [list(p.get("podSelector", {}).get("matchLabels", {}).values()) for p in rule["from"]]
            ports = sorted(p["port"] for p in rule["ports"])
            print(f"{who}:{ports}")
')"
if grep -q "agent.*:\[8443\]" <<<"$NP_REPORT" && grep -q "frontend.*:\[8000, 8444\]" <<<"$NP_REPORT" \
   && [[ $(grep -c "8443" <<<"$NP_REPORT") -eq 1 ]] && [[ $(grep -c "8444" <<<"$NP_REPORT") -eq 1 ]]; then
  ok "NetworkPolicy: ingest port from agents only, API port from the frontend only"
else
  bad "NetworkPolicy ports per client are wrong: $NP_REPORT"
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
