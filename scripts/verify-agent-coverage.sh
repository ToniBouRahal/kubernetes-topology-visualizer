#!/usr/bin/env bash
# T-7.5: an agent pod must be Running on EVERY node, control-plane included.
#
# This is a correctness check, not a scale check. The agent only observes connections that
# originate on its own node, so a node without an agent is a silent hole in the graph — traffic
# from it simply never appears, with no error anywhere (ADR-002 §8).
set -uo pipefail

CONTEXT="${KIND_CONTEXT:-kind-topology}"
NAMESPACE="${NAMESPACE:-topology}"
KUBECTL=(kubectl --context "$CONTEXT")

echo "== T-7.5: agent coverage on context $CONTEXT =="

nodes=$("${KUBECTL[@]}" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | sort)
if [[ -z "$nodes" ]]; then
  echo "  FAIL could not list nodes on context $CONTEXT"
  exit 1
fi

node_count=$(wc -l <<<"$nodes")
echo "  cluster has $node_count node(s)"

# Nodes that have a Running agent pod.
covered=$("${KUBECTL[@]}" -n "$NAMESPACE" get pods \
  -l app.kubernetes.io/component=agent \
  --field-selector=status.phase=Running \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' 2>/dev/null | sort -u)

fail=0
while read -r node; do
  [[ -z "$node" ]] && continue
  if grep -qx "$node" <<<"$covered"; then
    echo "  PASS agent Running on $node"
  else
    echo "  FAIL no Running agent pod on $node"
    fail=1
  fi
done <<<"$nodes"

# Report pods that exist but are not Running, so a CrashLoop is not mistaken for absence.
notready=$("${KUBECTL[@]}" -n "$NAMESPACE" get pods -l app.kubernetes.io/component=agent \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.phase}{" "}{.spec.nodeName}{"\n"}{end}' 2>/dev/null \
  | grep -v " Running " || true)
if [[ -n "$notready" ]]; then
  echo "  agent pods not Running:"
  sed 's/^/    /' <<<"$notready"
fi

if [[ "$fail" -eq 0 ]]; then
  echo "  agent coverage: all $node_count node(s) covered"
else
  echo "  agent coverage: INCOMPLETE — the graph will have silent holes"
fi
exit "$fail"
