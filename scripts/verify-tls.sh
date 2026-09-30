#!/usr/bin/env bash
# T-14.7 — what a compromised pod in the release namespace can reach (ADR-014 D-14.3, D-14.8).
#
# A throwaway pod with no certificates and none of the chart's labels — exactly what an attacker
# who landed in any other workload here would have — tries every listener that carries topology,
# and the database. Everything must refuse it except the plain ops port, which must serve health
# and nothing else. Run against the live kind demo: `make verify-tls`.
set -uo pipefail

CONTEXT="${KIND_CONTEXT:-kind-topology}"
NAMESPACE="${NAMESPACE:-topology}"
RELEASE="${RELEASE:-topology}"
K="kubectl --context $CONTEXT -n $NAMESPACE"
BACKEND="${RELEASE}-visualizer-backend.${NAMESPACE}.svc.cluster.local"
DB="${RELEASE}-visualizer-postgresql.${NAMESPACE}.svc.cluster.local"

pass=0; fail=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }

# Two idle probe pods from images already on the nodes, then `kubectl exec`: a one-shot
# `kubectl run --rm -i` loses the output of a command that exits before the attach catches it.
# No labels, so no NetworkPolicy admits them anywhere; deleted on exit.
PODS=(tls-probe-curl tls-probe-psql)
cleanup() { $K delete pod "${PODS[@]}" --ignore-not-found --wait=false >/dev/null 2>&1; }
trap cleanup EXIT INT TERM
$K run tls-probe-curl --restart=Never --image=topology-frontend:dev --image-pull-policy=IfNotPresent \
  --command -- sleep 600 >/dev/null
$K run tls-probe-psql --restart=Never --image=postgres:17-alpine --image-pull-policy=IfNotPresent \
  --command -- sleep 600 >/dev/null
$K wait --for=condition=Ready pod "${PODS[@]}" --timeout=120s >/dev/null || { echo "probe pods did not start" >&2; exit 1; }

probe() {
  local image="$1"; shift
  local pod=tls-probe-curl
  [[ "$image" == postgres* ]] && pod=tls-probe-psql
  $K exec "$pod" -- "$@" 2>&1
}

echo "== from an unlabelled pod with no certificate =="

# curl -k: skip verifying the SERVER, so the only thing under test is whether the server demands
# a client certificate. The http code is 000 when no HTTP response ever came back.
code="$(probe topology-frontend:dev curl -sk -o /dev/null -w '%{http_code}' --max-time 5 \
  -X POST -H 'Content-Type: application/json' -d '{}' "https://${BACKEND}:8443/api/v1/ingest/batches")"
[[ "$code" == *000* ]] && ok "ingest listener refuses a client without a certificate" \
  || bad "ingest listener answered HTTP $code to a client with no certificate"

code="$(probe topology-frontend:dev curl -sk -o /dev/null -w '%{http_code}' --max-time 5 \
  "https://${BACKEND}:8444/api/v1/graph?window=5m")"
[[ "$code" == *000* ]] && ok "API listener refuses a client without a certificate" \
  || bad "API listener answered HTTP $code to a client with no certificate"

code="$(probe topology-frontend:dev curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  "http://${BACKEND}:8444/api/v1/graph?window=5m")"
[[ "$code" == *000* ]] && ok "API listener does not speak plain HTTP" \
  || bad "API listener answered plain HTTP with $code"

code="$(probe topology-frontend:dev curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  "http://${BACKEND}:8000/api/v1/graph?window=5m")"
[[ "$code" == *404* ]] && ok "the plain ops port serves no topology (404)" \
  || bad "the plain ops port answered the graph with $code"

code="$(probe topology-frontend:dev curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  -X POST -H 'Content-Type: application/json' -d '{}' "http://${BACKEND}:8000/api/v1/ingest/batches")"
[[ "$code" == *404* ]] && ok "the plain ops port accepts no batches (404)" \
  || bad "the plain ops port answered an ingest POST with $code"

code="$(probe topology-frontend:dev curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  "http://${BACKEND}:8000/health/live")"
[[ "$code" == *200* ]] && ok "control: the ops port answers health (200), so the refusals above are real" \
  || bad "control failed: the ops port did not answer health ($code), so nothing above proves anything"

echo "== the database =="
out="$(probe postgres:17-alpine psql "host=${DB} user=topology dbname=topology sslmode=disable connect_timeout=5" -c 'select 1')"
if grep -q "no encryption" <<<"$out"; then
  ok "PostgreSQL rejects a connection without TLS (pg_hba: no encryption)"
else
  bad "PostgreSQL without TLS was not rejected for lack of encryption: $(head -c 200 <<<"$out")"
fi
out="$(probe postgres:17-alpine psql "host=${DB} user=topology dbname=topology sslmode=require connect_timeout=5" -c 'select 1')"
if grep -qi "password" <<<"$out"; then
  ok "over TLS it asks for the password — TLS alone does not let anyone in"
else
  bad "PostgreSQL over TLS did not ask for a password: $(head -c 200 <<<"$out")"
fi

echo
echo "TLS verification: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
