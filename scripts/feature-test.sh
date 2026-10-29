#!/usr/bin/env bash
# End-to-end feature checks against the running kind demo, one phase per subcommand.
#
#   feature-test.sh mesh        every edge the shop mesh is built to produce, node kinds, detail APIs
#   feature-test.sh diff        a controlled change, then /diff classifies NEW and REMOVED
#   feature-test.sh anomalies   triggers all five ADR-016 rules at once and asserts each fired
#   feature-test.sh metrics     the Prometheus series the anomaly rules and canaries promise
#   feature-test.sh security    mTLS-only API, plain listener serves health only, Pod Security
#   feature-test.sh auth        sign-in is enforced through the UI's own front door
#   feature-test.sh cli         kubectl-topology and its MCP server (skips unless signed in)
#   feature-test.sh alerts      a chat test message (skips unless `make alerts-on` was run)
#   feature-test.sh scale       graph size and API latency at whatever size the mesh is now
#
# The style is demo-verify.sh's: ask the running system, never the manifests, and print PASS,
# FAIL or NOTE per claim. NOTE is for what depends on the machine (outbound internet, timing of a
# CronJob) rather than on this code — reported, never asserted.
#
# The scenario phases (diff, anomalies) change the cluster and undo it on exit, Ctrl-C included.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTEXT="${KIND_CONTEXT:-kind-topology}"
NAMESPACE="${NAMESPACE:-topology}"
RELEASE="${RELEASE:-topology}"
KUBECTL="kubectl --context $CONTEXT"
HEALTH_PORT="${FT_HEALTH_PORT:-18199}"
API_PORT="${FT_API_PORT:-18198}"
UI_PORT="${FT_UI_PORT:-8080}"
CLI="${CLI:-$REPO_ROOT/bin/kubectl-topology}"
# How long a scenario runs before it is judged. Two full one-minute buckets plus the agent's
# flush and delivery: shorter and the window holds too few attempts for FAILURE_SPIKE's
# minimum, longer and the run is just slower.
SCENARIO_SECONDS="${FT_SCENARIO_SECONDS:-150}"
# shellcheck source=lib/mtls.sh
source "$REPO_ROOT/scripts/lib/mtls.sh"

WORK="$(mktemp -d)"
pass=0
fail=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note() { printf '  \033[33mNOTE\033[0m %s\n' "$1"; }
skip() { printf '  \033[90mSKIP\033[0m %s\n' "$1"; }

PIDS=()
UNDO=()
cleanup() {
  local i
  # Undo in reverse, so a later change that depends on an earlier one is removed first.
  for ((i = ${#UNDO[@]} - 1; i >= 0; i--)); do eval "${UNDO[$i]}" >/dev/null 2>&1 || true; done
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; done
  mtls_close
  rm -rf "$WORK"
  return 0
}
trap cleanup EXIT INT TERM

finish() {
  echo
  echo "$1: $pass passed, $fail failed"
  [[ "$fail" -eq 0 ]]
}

iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

# The next one-minute bucket boundary (ADR-005: storage is in one-minute buckets, so a window that
# starts mid-minute would mix traffic from before the trigger into the first bucket).
wait_minute() {
  local s=$(( 60 - $(date +%s) % 60 ))
  echo "  … waiting ${s}s for the next minute boundary"
  sleep "$s"
}

countdown() {
  local total="$1" what="$2" left
  for ((left = total; left > 0; left -= 30)); do
    echo "  … ${what}: ${left}s left"
    sleep $(( left < 30 ? left : 30 ))
  done
}

# ── connections ─────────────────────────────────────────────────────────────────────────────

open_health() {
  $KUBECTL port-forward -n "$NAMESPACE" "svc/${RELEASE}-visualizer-backend" \
    "${HEALTH_PORT}:8000" >/dev/null 2>&1 &
  PIDS+=($!)
  for _ in $(seq 1 20); do
    sleep 1
    curl -sf -o /dev/null --max-time 2 "http://localhost:${HEALTH_PORT}/health/ready" && return 0
  done
  bad "the backend's plain listener never became reachable on port ${HEALTH_PORT}"
  return 1
}

open_api() {
  if ! mtls_open frontend "$API_PORT"; then
    bad "could not reach the API listener over mutual TLS"
    return 1
  fi
  # mtls_open installs its own EXIT trap; put ours back so the undo list still runs.
  trap cleanup EXIT INT TERM
}

# api PATH OUTFILE — GET over mTLS as the frontend; non-2xx is a failure with the body shown.
api() {
  local code
  code="$("${MTLS_CURL[@]}" -s -o "$2" -w '%{http_code}' --max-time 20 "$MTLS_BASE$1")"
  if [[ "$code" != 2* ]]; then
    bad "GET $1 answered $code: $(head -c 300 "$2" 2>/dev/null)"
    return 1
  fi
}

# py FILE <<'PY' … — run a check with the response file as sys.argv[1] plus any extra args.
py() { python3 - "$@"; }

# ── preconditions ───────────────────────────────────────────────────────────────────────────

require_mesh() {
  if ! $KUBECTL get deploy checkout -n shop >/dev/null 2>&1; then
    bad "the shop mesh is not deployed — run: make mesh-up"
    finish "$1"
    exit 1
  fi
}

# The scenario phases compare a window against the time before it. That comparison is only
# meaningful once the mesh's traffic is old enough to be IN the earlier period: the anomaly rules
# suppress novelty on an empty baseline (ADR-016 D-16.2), and SLOW_SETUP needs ten timing samples
# on both sides. Wait for it rather than fail on a freshly created mesh.
wait_baseline() {
  local age="${1:-150}" deadline=$(( $(date +%s) + 420 )) now
  echo "  … waiting until the mesh has traffic older than ${age}s (fresh mesh: up to 7 min)"
  while :; do
    now=$(date +%s)
    if api "/api/v1/graph?from=$(iso $((now - 86400)))&to=$(iso $((now - age)))&namespace=shop-edge" \
         "$WORK/base.json" 2>/dev/null \
       && py "$WORK/base.json" <<'PY'
import json, sys
g = json.load(open(sys.argv[1]))
names = {n["id"]: n["name"] for n in g["nodes"]}
hit = any(names.get(e["source_id"]) == "synthetic-users"
          and names.get(e["target_id"]) == "ingress-gateway" for e in g["edges"])
sys.exit(0 if hit else 1)
PY
    then
      return 0
    fi
    if (( now > deadline )); then
      bad "the mesh never produced traffic older than ${age}s; is the agent running?"
      return 1
    fi
    sleep 15
  done
}

# ── phases ──────────────────────────────────────────────────────────────────────────────────

phase_mesh() {
  require_mesh "mesh"
  open_api || { finish "mesh"; return; }

  echo "== every edge the mesh is built to produce =="
  api "/api/v1/graph?window=15m" "$WORK/graph.json" || { finish "mesh"; return; }
  python3 "$REPO_ROOT/scripts/gen-mesh.py" --scale "${MESH_SCALE:-0}" --expected >"$WORK/expected"
  while read -r verdict rest; do
    case "$verdict" in
      FAIL) bad "$rest" ;;
      NOTE) note "$rest" ;;
      COUNT) ok "$rest expected edges present in the last 15 minutes" ;;
    esac
  done < <(py "$WORK/graph.json" "$WORK/expected" <<'PY'
import json, sys
g = json.load(open(sys.argv[1]))
nodes = {n["id"]: n for n in g["nodes"]}
seen = set()
for e in g["edges"]:
    s, t = nodes.get(e["source_id"]), nodes.get(e["target_id"])
    if s and t:
        seen.add((s["namespace"], s["kind"], s["name"], t["namespace"] or "-", t["kind"], t["name"],
                  int(e["destination_port"])))
present = 0
for line in open(sys.argv[2]):
    sns, sk, sn, tns, tk, tn, port, strength = line.split()
    desc = (f"{sns}/{sn} -> EXTERNAL TCP:{port}" if tk == "External"
            else f"{sns}/{sn} -> {tns}/{tn} TCP:{port}")
    if (sns, sk, sn, tns, tk, tn, int(port)) in seen:
        present += 1
    elif strength == "hard":
        print(f"FAIL {desc} — edge not found")
    else:
        print(f"NOTE {desc} — absent (needs outbound internet)")
print(f"COUNT {present}")
PY
)

  echo "== workload kinds and replica collapse =="
  for kind in Deployment StatefulSet DaemonSet Job; do
    if py "$WORK/graph.json" "$kind" <<'PY'
import json, sys
g = json.load(open(sys.argv[1]))
sys.exit(0 if any(n["kind"] == sys.argv[2] and (n["namespace"] or "").startswith("shop")
                  for n in g["nodes"]) else 1)
PY
    then ok "a $kind from the mesh is a node"
    else bad "no $kind node from the mesh"
    fi
  done
  py "$WORK/graph.json" <<'PY' && ok "replicas and DaemonSet pods collapse to one node each" \
                              || bad "duplicate (namespace, kind, name) nodes"
import json, sys
g = json.load(open(sys.argv[1]))
keys = [(n["namespace"], n["kind"], n["name"]) for n in g["nodes"]]
sys.exit(1 if len(keys) != len(set(keys)) else 0)
PY
  # A Pod node is correct for a pod whose ReplicaSet was already gone when it last connected —
  # the scenario phases delete Deployments, and their terminating pods land here
  # (resolver.go workloadFor). It is a defect only for a pod that still exists with an owner.
  local kind ns name owner
  local rs=0 owned=0 gone=0
  while read -r kind ns name; do
    [[ -z "$kind" ]] && continue
    if [[ "$kind" == ReplicaSet ]]; then
      rs=$((rs + 1)); bad "ReplicaSet node $ns/$name: the owner walk stopped early"
    elif owner="$($KUBECTL -n "$ns" get pod "$name" -o jsonpath='{.metadata.ownerReferences[0].kind}' 2>/dev/null)" \
         && [[ -n "$owner" ]]; then
      owned=$((owned + 1)); bad "Pod node $ns/$name, though the pod is alive and owned by a $owner"
    else
      gone=$((gone + 1))
    fi
  done < <(py "$WORK/graph.json" <<'PY'
import json, sys
for n in json.load(open(sys.argv[1]))["nodes"]:
    if n["kind"] in ("Pod", "ReplicaSet"):
        print(n["kind"], n["namespace"], n["name"])
PY
)
  (( rs + owned == 0 )) && ok "every live owned pod collapsed to its workload (no ReplicaSet nodes)"
  (( gone > 0 )) && note "$gone Pod node(s) from pods deleted with their Deployment — expected after diff/anomalies"

  echo "== namespaces, filters, node and edge detail =="
  api "/api/v1/namespaces?window=15m" "$WORK/ns.json" && {
    missing="$(py "$WORK/ns.json" <<'PY'
import json, sys
body = json.load(open(sys.argv[1]))
items = body.get("namespaces", body) if isinstance(body, dict) else body
names = {i if isinstance(i, str) else i.get("name") for i in items}
want = {"shop-edge", "shop", "shop-async", "shop-data", "shop-ops"}
print(" ".join(sorted(want - names)))
PY
)"
    [[ -z "$missing" ]] && ok "/namespaces lists all five mesh namespaces" \
                        || bad "/namespaces is missing: $missing"
  }
  api "/api/v1/graph?window=15m&namespace=shop-data" "$WORK/filtered.json" && {
    py "$WORK/filtered.json" <<'PY' && ok "a namespace filter returns only edges touching shop-data" \
                                    || bad "the namespace filter let unrelated edges through"
import json, sys
g = json.load(open(sys.argv[1]))
nodes = {n["id"]: n for n in g["nodes"]}
ok = g["edges"] and all("shop-data" in (nodes[e["source_id"]]["namespace"], nodes[e["target_id"]]["namespace"])
                        for e in g["edges"])
sys.exit(0 if ok else 1)
PY
  }

  # checkout's node detail must list exactly the dependencies the generator gave it.
  read -r node_id edge_id < <(py "$WORK/graph.json" <<'PY'
import json, sys, urllib.parse as u
g = json.load(open(sys.argv[1]))
n = next(n for n in g["nodes"] if n["namespace"] == "shop" and n["name"] == "checkout")
e = next(e for e in g["edges"] if e["source_id"] == n["id"])
print(u.quote(n["id"], safe=""), u.quote(e["id"], safe=""))
PY
)
  if [[ -n "${node_id:-}" ]] && api "/api/v1/nodes/${node_id}?window=15m" "$WORK/node.json"; then
    out="$(py "$WORK/node.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print(len({o["node_id"] for o in d["outgoing"]}), len(d["incoming"]))
PY
)"
    read -r outgoing incoming <<<"$out"
    [[ "$outgoing" == 7 ]] && ok "node detail: checkout has its 7 dependencies" \
                           || bad "node detail: checkout has $outgoing dependencies, expected 7"
    (( incoming >= 1 )) && ok "node detail: checkout has callers (mobile-bff)" \
                        || bad "node detail: checkout has no callers"
  fi
  if [[ -n "${edge_id:-}" ]] && api "/api/v1/edges/${edge_id}?window=15m" "$WORK/edge.json"; then
    py "$WORK/edge.json" <<'PY' && ok "edge detail resolves source and target" \
                                || bad "edge detail does not name checkout as its source"
import json, sys
d = json.load(open(sys.argv[1]))
sys.exit(0 if d["source"]["name"] == "checkout" and d["edge"]["connection_count"] > 0 else 1)
PY
  fi

  finish "mesh"
}

phase_diff() {
  require_mesh "diff"
  open_api || { finish "diff"; return; }
  wait_baseline 150 || { finish "diff"; return; }

  echo "== a controlled change: one service removed, one added =="
  # A fresh name each run: a rerun inside the lookback would otherwise find the previous run's
  # loyalty -> postgres in the baseline and rightly call it CHANGED, not NEW.
  local svc="loyalty-$(date +%H%M)"
  $KUBECTL -n shop delete deploy -l topology-ft=diff --ignore-not-found --wait >/dev/null 2>&1
  # Remove the caller a minute BEFORE the boundary, so its last connection lands in the baseline
  # and none in the current period: that is what REMOVED means.
  wait_minute
  $KUBECTL -n shop-edge delete deploy synthetic-users --grace-period=1 --wait >/dev/null
  UNDO+=("python3 '$REPO_ROOT/scripts/gen-mesh.py' --scale '${MESH_SCALE:-0}' | $KUBECTL apply -f -")
  echo "  removed shop-edge/synthetic-users"
  wait_minute
  local m0; m0=$(date +%s); m0=$(( m0 - m0 % 60 ))
  sed "s/loyalty/$svc/g" "$REPO_ROOT/demo/demo-mesh-change.yaml" | $KUBECTL apply -f - >/dev/null
  echo "  added shop/$svc (-> shop-data/postgres) at $(iso "$m0")"
  local len=120
  countdown $(( m0 + len + 30 - $(date +%s) )) "letting both periods fill"

  # include_unchanged: without it /diff omits UNCHANGED edges and counts none of them.
  local q="baseline_from=$(iso $((m0 - len)))&baseline_to=$(iso "$m0")&current_from=$(iso "$m0")&current_to=$(iso $((m0 + len)))&include_unchanged=true"
  api "/api/v1/diff?$q" "$WORK/diff.json" || { finish "diff"; return; }
  api "/api/v1/graph?from=$(iso $((m0 - len)))&to=$(iso $((m0 + len)))" "$WORK/both.json" \
    || { finish "diff"; return; }
  while read -r verdict rest; do
    case "$verdict" in PASS) ok "$rest" ;; FAIL) bad "$rest" ;; NOTE) note "$rest" ;; esac
  done < <(py "$WORK/diff.json" "$WORK/both.json" "$svc" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
# Names come from the graph's node list, never from parsing an id (contracts/ids.md §2).
names = {n["id"]: (n["namespace"], n["name"]) for n in json.load(open(sys.argv[2]))["nodes"]}
cls = {(names.get(e["source_id"]), names.get(e["target_id"])): e["classification"] for e in d["edges"]}
def check(src, dst, want):
    got = cls.get((src, dst))
    word = "PASS" if got == want else "FAIL"
    print(f"{word} {src[1]} -> {dst[1]} is {got or 'absent'} (expected {want})")
check(("shop", sys.argv[3]), ("shop-data", "postgres"), "NEW")
check(("shop-edge", "synthetic-users"), ("shop-edge", "ingress-gateway"), "REMOVED")
s = d["summary"]
print(f"{'PASS' if s['unchanged_count'] + s['changed_count'] > 20 else 'FAIL'} "
      f"summary: {s['new_count']} new, {s['removed_count']} removed, "
      f"{s['changed_count']} changed, {s['unchanged_count']} unchanged")
PY
)
  echo "  restoring shop-edge/synthetic-users"
  finish "diff"
}

phase_anomalies() {
  require_mesh "anomalies"
  open_api || { finish "anomalies"; return; }
  wait_baseline 150 || { finish "anomalies"; return; }

  echo "== triggering all five rules at once =="
  local pg_ip pg_node pg_dev
  pg_ip="$($KUBECTL -n shop-data get pod postgres-0 -o jsonpath='{.status.podIP}')"
  pg_node="$($KUBECTL -n shop-data get pod postgres-0 -o jsonpath='{.spec.nodeName}')"
  pg_dev="$(docker exec "$pg_node" ip -o route get "$pg_ip" 2>/dev/null | grep -o 'dev [^ ]*' | cut -d' ' -f2)"
  $KUBECTL delete job intruder -n demo --ignore-not-found --now >/dev/null 2>&1
  # A fresh name each run, for the reason phase_diff gives: novelty is judged over 24 hours.
  local rogue="rogue-crawler-$(date +%H%M)"
  $KUBECTL -n shop delete deploy -l topology-ft=anomalies --ignore-not-found --wait >/dev/null 2>&1

  wait_minute
  local t0; t0=$(date +%s); t0=$(( t0 - t0 % 60 ))

  # NEW_EDGE + NEW_EXTERNAL_EGRESS: a workload nobody has seen, talking to the database and out.
  sed "s/rogue-crawler/$rogue/g" "$REPO_ROOT/demo/demo-anomalies.yaml" | $KUBECTL apply -f - >/dev/null
  UNDO+=("$KUBECTL -n shop delete deploy -l topology-ft=anomalies --ignore-not-found --wait=false")
  echo "  $rogue deployed  -> NEW_EDGE, NEW_EXTERNAL_EGRESS"

  # FAILURE_SPIKE: the search index stops listening. Pointing the Service at a port nothing
  # serves makes every connection a refused one, with no pod restart and a one-line undo.
  $KUBECTL -n shop-data patch svc elasticsearch --type=json \
    -p='[{"op":"replace","path":"/spec/ports/0/targetPort","value":6390}]' >/dev/null
  UNDO+=("$KUBECTL -n shop-data patch svc elasticsearch --type=json -p='[{\"op\":\"replace\",\"path\":\"/spec/ports/0/targetPort\",\"value\":6379}]'")
  echo "  elasticsearch refusing           -> FAILURE_SPIKE"

  # SLOW_SETUP: 50 ms on the wire into postgres-0, applied on its veth inside the kind node.
  local slow=false
  if [[ -n "$pg_dev" ]] && docker exec "$pg_node" tc qdisc replace dev "$pg_dev" root netem delay 50ms 2>/dev/null; then
    UNDO+=("docker exec '$pg_node' tc qdisc del dev '$pg_dev' root")
    slow=true
    echo "  postgres-0 +50 ms SYN latency    -> SLOW_SETUP"
  else
    echo "  could not add latency (no netem?) -> SLOW_SETUP will be skipped"
  fi

  # CANARY_CONTACT: the decoy and its intruder, as `make demo-canary` deploys them.
  $KUBECTL apply -f "$REPO_ROOT/demo/demo-canary.yaml" >/dev/null
  echo "  intruder touching legacy-admin   -> CANARY_CONTACT"

  countdown $(( t0 + SCENARIO_SECONDS - $(date +%s) )) "letting the window fill"
  api "/api/v1/anomalies?from=$(iso "$t0")&to=$(iso "$(date +%s)")" "$WORK/anom.json" \
    || { finish "anomalies"; return; }

  echo "== what the backend found =="
  while read -r verdict rest; do
    case "$verdict" in
      PASS) ok "$rest" ;; FAIL) bad "$rest" ;; NOTE) note "$rest" ;; SKIP) skip "$rest" ;;
      INFO) echo "       $rest" ;;
    esac
  done < <(py "$WORK/anom.json" "$slow" "$rogue" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
slow, rogue = sys.argv[2] == "true", sys.argv[3]
nodes = {n["id"]: n for n in d["nodes"]}
def name(i):
    n = nodes.get(i)
    return n["name"] if n else i
found = [(a["type"], name(a["source_id"]), name(a["target_id"]), a) for a in d["anomalies"]]
def have(kind, src=None, dst=None):
    return next((a for t, s, x, a in found if t == kind and (src is None or s == src)
                 and (dst is None or x == dst)), None)
print(("PASS" if d["summary"]["baseline_observed"] else "FAIL")
      + " the baseline (24 h before the window) holds traffic, so novelty rules are live")
checks = [
    ("CANARY_CONTACT", None, "legacy-admin", "hard"),
    ("NEW_EDGE", rogue, "postgres", "hard"),
    ("NEW_EXTERNAL_EGRESS", rogue, "EXTERNAL", "soft"),
    ("FAILURE_SPIKE", "search", "elasticsearch", "hard"),
    ("SLOW_SETUP", None, "postgres", "hard" if slow else "skip"),
]
for kind, src, dst, strength in checks:
    a = have(kind, src, dst)
    label = f"{kind:<20} {src or '*'} -> {dst}"
    if a:
        print(f"PASS {label}")
        print(f"INFO {a['severity']}: {a['reason']}")
    elif strength == "soft":
        print(f"NOTE {label} absent — needs outbound internet to {dst}")
    elif strength == "skip":
        print(f"SKIP {label} — latency could not be injected")
    else:
        print(f"FAIL {label} not raised")
s = d["summary"]
print(f"INFO total: {s['critical_count']} critical, {s['warning_count']} warning")
PY
)

  # D-16.5: contact is counted and logged at ingest, independent of any query.
  # Into a file, not `| grep -q`: grep exiting early SIGPIPEs kubectl, and pipefail reports that.
  $KUBECTL -n "$NAMESPACE" logs "deploy/${RELEASE}-visualizer-backend" --since=10m >"$WORK/log" 2>/dev/null
  if grep -q '"canary contacted"' "$WORK/log"; then
    ok "the backend logged a WARNING for the canary contact"
  else
    bad "no 'canary contacted' line in the backend log"
  fi
  echo "  undoing the triggers"
  finish "anomalies"
}

phase_metrics() {
  open_health || { finish "metrics"; return; }
  echo "== Prometheus series (ADR-013, ADR-016, ADR-018) =="
  curl -sf --max-time 10 "http://localhost:${HEALTH_PORT}/metrics" >"$WORK/metrics" \
    || { bad "/metrics did not answer"; finish "metrics"; return; }
  local t line
  for t in canary_contact new_external_egress new_edge failure_spike slow_setup; do
    line="$(grep "^topology_backend_active_anomalies{type=\"$t\"}" "$WORK/metrics")"
    [[ -n "$line" ]] && ok "active_anomalies{type=\"$t\"} = ${line##* } (every type present, zero included)" \
                     || bad "active_anomalies gauge missing for $t"
  done
  local canary
  canary="$(awk '/^topology_backend_canary_connections_total/ {s += $NF} END {print s + 0}' "$WORK/metrics")"
  if awk "BEGIN {exit !($canary > 0)}"; then ok "canary_connections_total = $canary"
  else note "canary_connections_total is 0 (run 'make demo-canary' or the anomalies phase first)"
  fi
  grep -qE '^topology_backend_(ingest|batches)[a-z_]*_total' "$WORK/metrics" \
    && ok "ingest counters are exported" || bad "no ingest counters in /metrics"
  if grep -q '^topology_backend_alerts_total' "$WORK/metrics"; then
    ok "alerts_total is exported: $(grep '^topology_backend_alerts_total' "$WORK/metrics" | tr '\n' ' ')"
  else
    note "alerts_total has no samples (chat alerts off, or nothing sent yet)"
  fi
  finish "metrics"
}

phase_security() {
  open_health || { finish "security"; return; }
  echo "== the topology is served only over mutual TLS (ADR-014 D-14.3) =="
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://localhost:${HEALTH_PORT}/api/v1/graph?window=5m")"
  [[ "$code" == 404 || "$code" == 403 || "$code" == 401 ]] \
    && ok "the plain listener refuses /api/v1/graph ($code)" \
    || bad "the plain listener answered /api/v1/graph with $code"
  if mtls_open frontend "$API_PORT"; then
    trap cleanup EXIT INT TERM
    code="$("${MTLS_CURL[@]}" -s -o /dev/null -w '%{http_code}' --max-time 5 "$MTLS_BASE/api/v1/graph?window=5m")"
    [[ "$code" == 200 ]] && ok "the frontend's certificate is accepted" \
                         || bad "the frontend's certificate was refused ($code)"
    # Last on purpose: the refused handshake resets the stream, and kubectl port-forward exits on
    # a reset, so nothing can use this forward afterwards.
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --cacert "$MTLS_DIR/ca.crt" \
      --resolve "${RELEASE}-visualizer-backend.${NAMESPACE}.svc.cluster.local:${API_PORT}:127.0.0.1" \
      "$MTLS_BASE/api/v1/graph?window=5m" 2>/dev/null)"
    [[ "$code" != 2* ]] && ok "the mTLS listener refuses a client with no certificate (handshake refused)" \
                        || bad "the mTLS listener served a client without a certificate"
  fi
  echo "== Pod Security (ADR-014 D-14.12) =="
  local enforce warn
  enforce="$($KUBECTL get ns "$NAMESPACE" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}')"
  warn="$($KUBECTL get ns "$NAMESPACE" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/warn}')"
  [[ "$enforce" == privileged && "$warn" == restricted ]] \
    && ok "namespace $NAMESPACE: enforce=privileged, warn=restricted" \
    || bad "namespace $NAMESPACE labels: enforce='$enforce' warn='$warn' (run: make pod-security)"
  local root
  root="$($KUBECTL -n "$NAMESPACE" get deploy "${RELEASE}-visualizer-backend" \
    -o jsonpath='{.spec.template.spec.securityContext.runAsNonRoot}{.spec.template.spec.containers[0].securityContext.runAsNonRoot}')"
  [[ "$root" == *true* ]] && ok "the backend runs as non-root" || bad "the backend may run as root"
  finish "security"
}

ensure_ui() {
  if curl -s -o /dev/null --max-time 2 "http://localhost:${UI_PORT}/"; then
    return 0
  fi
  $KUBECTL port-forward -n "$NAMESPACE" "svc/${RELEASE}-visualizer-frontend" "${UI_PORT}:8080" \
    >/dev/null 2>&1 &
  PIDS+=($!)
  for _ in $(seq 1 20); do
    sleep 1
    curl -s -o /dev/null --max-time 2 "http://localhost:${UI_PORT}/" && return 0
  done
  bad "the UI never became reachable on port ${UI_PORT}"
  return 1
}

phase_auth() {
  ensure_ui || { finish "auth"; return; }
  echo "== sign-in is enforced at the front door (ADR-014 D-14.7, ADR-017) =="
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://localhost:${UI_PORT}/api/v1/graph?window=5m")"
  [[ "$code" == 401 || "$code" == 302 || "$code" == 403 ]] \
    && ok "the API through the UI refuses an anonymous request ($code)" \
    || bad "the API through the UI answered an anonymous request with $code"
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H 'Authorization: Bearer not-a-token' \
    "http://localhost:${UI_PORT}/api/v1/graph?window=5m")"
  [[ "$code" == 401 || "$code" == 302 || "$code" == 403 ]] \
    && ok "a forged bearer token is refused ($code)" || bad "a forged bearer token got $code"
  if curl -sf --max-time 5 \
       "http://localhost:${UI_PORT}/keycloak/realms/topology/.well-known/openid-configuration" \
       -o "$WORK/oidc.json"; then
    py "$WORK/oidc.json" <<'PY' && ok "Keycloak's OIDC discovery is served under /keycloak" \
                                || bad "the OIDC discovery document is malformed"
import json, sys
d = json.load(open(sys.argv[1]))
sys.exit(0 if d.get("issuer", "").endswith("/realms/topology") and d.get("authorization_endpoint") else 1)
PY
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://localhost:${UI_PORT}/keycloak/admin/")"
    [[ "$code" != 200 ]] && ok "the Keycloak admin console is not exposed ($code)" \
                         || bad "the Keycloak admin console is reachable through the UI"
  else
    note "no OIDC discovery under /keycloak (auth.keycloak.enabled off?)"
  fi
  echo "  sign in by hand at http://localhost:${UI_PORT} as admin@topology.local (make ui prints the password)"
  finish "auth"
}

phase_cli() {
  echo "== kubectl-topology and its MCP server (ADR-015, ADR-016 D-16.8) =="
  [[ -x "$CLI" ]] || { bad "$CLI is not built — run: make cli"; finish "cli"; return; }
  ensure_ui || { finish "cli"; return; }
  local status
  status="$("$CLI" status 2>&1)"
  if [[ "$status" == *"Not signed in"* ]]; then
    skip "not signed in — run 'make cli-login' once (it opens a browser), then rerun"
    finish "cli"
    return
  fi
  run() {
    local desc="$1" want="$2"; shift 2
    local out
    if out="$("$CLI" "$@" 2>&1)" && grep -qE -- "$want" <<<"$out"; then
      ok "$desc"
    else
      bad "$desc — $(head -c 300 <<<"$out")"
    fi
  }
  run "namespaces lists the mesh"           '^shop-data'          namespaces --since 15m
  run "graph -n shop shows checkout"        'checkout'            graph -n shop --since 15m
  run "graph -o mermaid is a flowchart"     '^flowchart LR'       graph -n shop -o mermaid --since 15m
  run "graph -o mermaid has subgraphs"      'subgraph'            graph -o mermaid --since 15m
  run "deps checkout names its payment dep" 'payment-service'     deps checkout -n shop --since 15m
  run "diff over the last 20 minutes"       '(new|removed|changed|unchanged)' diff --from 20m --to now
  run "anomalies runs against the baseline" '.'                   anomalies --since 15m
  local edge
  edge="$("$CLI" graph -n shop -o json --since 15m 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["edges"][0]["id"])' 2>/dev/null)"
  [[ -n "$edge" ]] && run "edge <id> prints one relationship" 'TCP' edge "$edge" --since 15m

  # MCP over stdio: initialize, then list tools, exactly as an agent host would.
  local mcp
  mcp="$(printf '%s\n' \
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"feature-test","version":"1"}}}' \
    '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
    | timeout 15 "$CLI" mcp 2>/dev/null)"
  for tool in list_namespaces get_topology get_dependencies diff_topology get_edge; do
    grep -q "\"$tool\"" <<<"$mcp" && ok "MCP serves $tool" || bad "MCP does not list $tool"
  done
  finish "cli"
}

phase_alerts() {
  echo "== chat alerts (ADR-018) =="
  local spec
  spec="$($KUBECTL -n "$NAMESPACE" get deploy "${RELEASE}-visualizer-backend" -o yaml)"
  if ! grep -qE 'ALERT_(SLACK|TEAMS|TELEGRAM)' <<<"$spec"; then
    skip "chat alerts are off — run 'make alerts-on' with a webhook or bot token first"
    finish "alerts"
    return
  fi
  if $KUBECTL -n "$NAMESPACE" exec "deploy/${RELEASE}-visualizer-backend" -- python -m app.alerts test; then
    ok "a test message was delivered to every configured channel — check the chat"
  else
    bad "the test message was not delivered (see the output above)"
  fi
  local sent
  sent="$($KUBECTL -n "$NAMESPACE" logs "deploy/${RELEASE}-visualizer-backend" --since=30m 2>/dev/null \
    | grep -c '"alert sent' || true)"
  (( sent > 0 )) && ok "$sent alert(s) sent in the last 30 minutes (anomaly phase included)" \
                 || note "no 'alert sent' log lines in the last 30 minutes"
  finish "alerts"
}

phase_scale() {
  open_api || { finish "scale"; return; }
  echo "== graph size and API latency at the current mesh size =="
  local w t
  for w in 5m 15m 1h; do
    t="$("${MTLS_CURL[@]}" -s -o "$WORK/g.json" -w '%{time_total}' --max-time 30 \
      "$MTLS_BASE/api/v1/graph?window=$w")"
    read -r nodes edges truncated < <(py "$WORK/g.json" <<'PY'
import json, sys
g = json.load(open(sys.argv[1]))
print(len(g["nodes"]), len(g["edges"]), g["summary"].get("truncated", False))
PY
)
    if awk "BEGIN {exit !($t < 2.0)}"; then
      ok "window=$w: $nodes nodes, $edges edges in ${t}s (truncated: $truncated)"
    else
      bad "window=$w: $nodes nodes, $edges edges took ${t}s (target < 2s)"
    fi
  done
  echo "  pods in the mesh: $($KUBECTL get pods -A -l topology-demo=true --no-headers 2>/dev/null | wc -l)"
  echo "  node usage:"
  docker stats --no-stream --format '    {{.Name}}  cpu {{.CPUPerc}}  mem {{.MemUsage}}' \
    $(kind get nodes --name "${CONTEXT#kind-}" 2>/dev/null) 2>/dev/null
  finish "scale"
}

case "${1:-}" in
  mesh) phase_mesh ;;
  diff) phase_diff ;;
  anomalies) phase_anomalies ;;
  metrics) phase_metrics ;;
  security) phase_security ;;
  auth) phase_auth ;;
  cli) phase_cli ;;
  alerts) phase_alerts ;;
  scale) phase_scale ;;
  *) sed -n '2,13p' "$0"; exit 2 ;;
esac
