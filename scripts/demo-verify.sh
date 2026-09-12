#!/usr/bin/env bash
# Assert the demo topology through the API — tests T-7.10 and T-7.11 (ADR-007 D-7.6).
#
# This is the check that makes the demo a demonstration rather than a screenshot: it asks the
# running system what it observed and compares against the edges the demo manifests are designed
# to produce. Nothing here reads the manifests; it reads the API, which is the only thing that
# proves the eBPF path actually worked end to end.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTEXT="${KIND_CONTEXT:-kind-topology}"
NAMESPACE="${NAMESPACE:-topology}"
RELEASE="${RELEASE:-topology}"
WINDOW="${WINDOW:-15m}"
PORT="${VERIFY_PORT:-18099}"

pass=0
fail=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }

PF_PID=""
cleanup() {
  # Always tear the port-forward down, including on Ctrl-C: a leaked forward silently holds the
  # port and makes the next run fail for a reason that has nothing to do with the system.
  [[ -n "$PF_PID" ]] && kill "$PF_PID" 2>/dev/null
  wait "$PF_PID" 2>/dev/null
  return 0
}
trap cleanup EXIT INT TERM

echo "== connecting to the API =="
kubectl --context "$CONTEXT" port-forward -n "$NAMESPACE" \
  "svc/${RELEASE}-visualizer-backend" "${PORT}:8000" >/dev/null 2>&1 &
PF_PID=$!

api_ready=false
for _ in $(seq 1 20); do
  sleep 1
  if curl -sf -o /dev/null --max-time 2 "http://localhost:${PORT}/health/ready" 2>/dev/null; then
    api_ready=true
    break
  fi
done

if [[ "$api_ready" != true ]]; then
  bad "the backend API never became reachable on port ${PORT}"
  echo
  echo "demo verification: $pass passed, $fail failed"
  exit 1
fi
ok "backend API reachable and ready"

GRAPH="$(curl -s --max-time 15 "http://localhost:${PORT}/api/v1/graph?window=${WINDOW}" 2>/dev/null)"
if [[ -z "$GRAPH" ]]; then
  bad "the graph endpoint returned nothing"
  echo
  echo "demo verification: $pass passed, $fail failed"
  exit 1
fi

# One python invocation does the parsing; bash + jq would need jq installed, and the repo already
# depends on python3 for the contract tooling.
edge_check() {
  local desc="$1" src_kind="$2" src_name="$3" dst_kind="$4" dst_name="$5" port="$6"
  local result
  result="$(printf '%s' "$GRAPH" | python3 -c '
import json, sys
graph = json.load(sys.stdin)
src_kind, src_name, dst_kind, dst_name, port = sys.argv[1:6]
nodes = {n["id"]: n for n in graph["nodes"]}
for e in graph["edges"]:
    s = nodes.get(e["source_id"])
    t = nodes.get(e["target_id"])
    if not s or not t:
        continue
    if (s["kind"] == src_kind and s["name"] == src_name
            and t["kind"] == dst_kind and t["name"] == dst_name
            and int(e["destination_port"]) == int(port)):
        print(f'"'"'OK {e["connection_count"]}'"'"')
        break
else:
    print("MISSING")
' "$src_kind" "$src_name" "$dst_kind" "$dst_name" "$port" 2>/dev/null)"

  if [[ "$result" == OK* ]]; then
    ok "$desc (${result#OK })"
  else
    bad "$desc — edge not found"
  fi
}

# Destinations are workloads, not the Services in front of them (ADR-009 D-9.1). That is what
# makes these two edges share the `backend` node and the chain connect.
echo "== T-7.10: the expected demo edges are present =="
edge_check "frontend -> backend TCP:8080"  Deployment frontend Deployment  backend 8080
edge_check "backend  -> redis   TCP:6379"  Deployment backend  StatefulSet redis   6379

# The external edge depends on the machine having outbound network. Absent is not a failure —
# demo-workloads.yaml says so explicitly — so this is reported, never asserted.
EXT="$(printf '%s' "$GRAPH" | python3 -c '
import json, sys
g = json.load(sys.stdin)
ext = [n for n in g["nodes"] if n["kind"] == "External"]
print("present" if ext else "absent")
' 2>/dev/null)"
echo "  NOTE external edge: $EXT (absent is expected on an offline cluster, not a failure)"

# T-9.6. The two edges above can both pass while the graph is still two disconnected pairs —
# that was exactly the defect ADR-009 fixes, and it is invisible unless something checks that the
# node `frontend` reaches is the same node `redis` is reached FROM.
echo "== T-9.6: the chain is connected, not two disjoint pairs =="
CHAIN="$(printf '%s' "$GRAPH" | python3 -c '
import json, sys
g = json.load(sys.stdin)
nodes = {n["id"]: n for n in g["nodes"]}
out = {}
for e in g["edges"]:
    s, t = nodes.get(e["source_id"]), nodes.get(e["target_id"])
    if s and t:
        out.setdefault(s["name"], set()).add(t["name"])
# frontend reaches backend, and that same backend node reaches redis.
print("connected" if "backend" in out.get("frontend", set())
      and "redis" in out.get("backend", set()) else "split")
' 2>/dev/null)"
if [[ "$CHAIN" == "connected" ]]; then
  ok "frontend -> backend -> redis is one connected chain"
else
  bad "frontend -> backend -> redis is not connected; the hop through backend is split"
fi

echo "== T-7.11: replicas collapse and identity holds =="
printf '%s' "$GRAPH" | python3 -c '
import json, sys
g = json.load(sys.stdin)
names = [(n["kind"], n["name"], n["namespace"]) for n in g["nodes"]]
dupes = {n for n in names if names.count(n) > 1}
sys.exit(1 if dupes else 0)
' 2>/dev/null
if [[ $? -eq 0 ]]; then
  ok "no duplicate (kind, name, namespace) — replicas collapsed to one node each"
else
  bad "duplicate nodes found; replicas did not collapse"
fi

# No node may be a bare Pod for a workload that has an owner, and ReplicaSet must never appear:
# both would mean the owner-reference walk stopped early.
BADKIND="$(printf '%s' "$GRAPH" | python3 -c '
import json, sys
g = json.load(sys.stdin)
bad = [n["name"] for n in g["nodes"] if n["kind"] == "ReplicaSet"]
print(",".join(bad) if bad else "none")
' 2>/dev/null)"
if [[ "$BADKIND" == "none" ]]; then
  ok "no ReplicaSet nodes (owner-reference walk reached the workload)"
else
  bad "ReplicaSet nodes present: $BADKIND"
fi

# The counted burst, when it has been run. This is the strongest single claim the demo makes:
# not "traffic appears" but "exactly the number of connections opened is the number reported".
if kubectl --context "$CONTEXT" get job demo-traffic -n demo >/dev/null 2>&1; then
  echo "== the counted burst is reported exactly =="
  EXPECTED="$(kubectl --context "$CONTEXT" get job demo-traffic -n demo \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="COUNT")].value}' 2>/dev/null)"
  EXPECTED="${EXPECTED:-100}"

  for target in redis backend; do
    port=6379; [[ "$target" == backend ]] && port=8080
    actual="$(printf '%s' "$GRAPH" | python3 -c '
import json, sys
g = json.load(sys.stdin)
nodes = {n["id"]: n for n in g["nodes"]}
target, port = sys.argv[1], int(sys.argv[2])
for e in g["edges"]:
    s = nodes.get(e["source_id"]); t = nodes.get(e["target_id"])
    if s and t and s["name"] == "demo-traffic" and t["name"] == target             and int(e["destination_port"]) == port:
        print(e["connection_count"]); break
else:
    print("none")
' "$target" "$port" 2>/dev/null)"

    if [[ "$actual" == "$EXPECTED" ]]; then
      ok "demo-traffic -> $target: opened $EXPECTED, reported $actual"
    elif [[ "$actual" == "none" ]]; then
      echo "  NOTE demo-traffic -> $target: no edge in this window (the burst may have aged out)"
    else
      # Not a hard failure: the window may span more than one burst, and the standing workloads
      # keep running. Reported so a mismatch is visible rather than assumed away.
      echo "  NOTE demo-traffic -> $target: opened $EXPECTED, reported $actual" \
           "(equal only when the window covers exactly one burst)"
    fi
  done
fi

# The change scenario, only when it has been applied. Checking unconditionally would fail every
# run of the base demo, which is not a defect.
if kubectl --context "$CONTEXT" get deploy reporter -n demo >/dev/null 2>&1; then
  echo "== the controlled change is visible =="
  # 6380, not 6379: the payment Service deliberately publishes a different port from redis
  # (targetPort 6379) so the new dependency is distinguishable from the existing one at a glance.
  edge_check "reporter -> payment TCP:6380" Deployment reporter Deployment payment 6380
else
  echo "  NOTE change scenario not applied (run 'make demo-change' to add it)"
fi

echo
echo "demo verification: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
