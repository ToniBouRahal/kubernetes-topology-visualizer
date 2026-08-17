#!/usr/bin/env bash
# Print the service-level edges the agents are currently reporting.
#
# Phase 1 has no backend yet, so the agents emit batches to their structured logs. This script
# collects those log lines from every agent pod and renders the union as an edge list — the
# Phase 1 equivalent of querying the graph API, and the evidence for the Phase 1 gate.
set -uo pipefail

CONTEXT="${KIND_CONTEXT:-kind-topology}"
NAMESPACE="${NAMESPACE:-topology}"
SINCE="${SINCE:-2m}"
KUBECTL=(kubectl --context "$CONTEXT")

pods=$("${KUBECTL[@]}" -n "$NAMESPACE" get pods -l app.kubernetes.io/component=agent \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)

if [[ -z "$pods" ]]; then
  echo "no agent pods found in namespace $NAMESPACE on context $CONTEXT" >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

for pod in $pods; do
  "${KUBECTL[@]}" -n "$NAMESPACE" logs "$pod" --since="$SINCE" 2>/dev/null \
    | grep '"msg":"batch"' >>"$tmp" || true
done

if [[ ! -s "$tmp" ]]; then
  echo "no batches emitted in the last $SINCE."
  echo "the agents may still be starting, or no NEW connections were opened —"
  echo "the collector records active opens, so a reused pooled connection produces no event."
  exit 0
fi

python3 - "$tmp" <<'PY'
import json, sys
from collections import defaultdict

edges = defaultdict(lambda: {"count": 0, "first": None, "last": None})
batches = 0

for line in open(sys.argv[1], encoding="utf-8"):
    try:
        record = json.loads(line)
    except json.JSONDecodeError:
        continue
    payload = record.get("payload")
    if not isinstance(payload, dict):
        continue
    batches += 1
    for e in payload.get("edges", []):
        key = (e["source"]["id"], e["target"]["id"], e["protocol"], e["destination_port"])
        agg = edges[key]
        agg["count"] += e["connection_count"]
        agg["first"] = min(x for x in (agg["first"], e["first_seen"]) if x)
        agg["last"] = max(x for x in (agg["last"], e["last_seen"]) if x)

def label(node_id: str) -> str:
    # Display only. Identity stays opaque — this is a human-readable rendering of an ID we
    # already hold, not a consumer parsing an ID to derive meaning.
    if node_id == "external:EXTERNAL":
        return "EXTERNAL"
    parts = node_id.split(":")
    return f"{parts[2]}/{parts[3]}:{parts[4]}" if len(parts) == 5 else node_id

print(f"{batches} batch(es) observed\n")
if not edges:
    print("no edges")
    raise SystemExit(0)

width = max(len(label(k[0])) for k in edges)
for key in sorted(edges, key=lambda k: (k[0], k[1], k[2], k[3])):
    src, dst, proto, port = key
    agg = edges[key]
    print(f"  {label(src):<{width}}  ->  {label(dst):<{width}}  {proto}:{port:<6} "
          f"connections={agg['count']}")
PY
