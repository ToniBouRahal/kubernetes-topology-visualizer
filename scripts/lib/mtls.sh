# Reaching the backend's mutual-TLS listeners from a developer machine (ADR-014 D-14.3).
#
# Source this file, then:
#
#   mtls_open frontend 18444     # the API listener, as the frontend: reads
#   mtls_open agent    18443     # the ingest listener, as an agent: writes
#   "${MTLS_CURL[@]}" "$MTLS_BASE/api/v1/graph?window=5m"
#   mtls_close                   # also runs on EXIT
#
# The client certificate comes from the role's Secret in the cluster, so this works exactly as far
# as the caller's own RBAC does: no Secret read, no API access. The certificate and key are written
# to a private temporary directory and removed on close. The backend's certificate is verified
# against the server CA by its Service name, with --resolve pointing that name at the port-forward.
#
# Needs: KUBECTL (or kubectl), NAMESPACE, RELEASE — the same variables the Makefile exports.

MTLS_DIR=""
MTLS_PF=""
MTLS_BASE=""
MTLS_CURL=()

mtls_open() {
  local role="$1" local_port="$2" remote_port secret host
  local kubectl="${KUBECTL:-kubectl}"
  case "$role" in
    frontend) remote_port=8444 ;;
    agent) remote_port=8443 ;;
    *) echo "mtls_open: role is frontend or agent, not '$role'" >&2; return 1 ;;
  esac
  secret="${RELEASE}-visualizer-tls-${role}"
  host="${RELEASE}-visualizer-backend.${NAMESPACE}.svc.cluster.local"

  MTLS_DIR="$(mktemp -d)"
  chmod 700 "$MTLS_DIR"
  trap mtls_close EXIT
  local key
  for key in tls.crt tls.key ca.crt; do
    if ! $kubectl -n "$NAMESPACE" get secret "$secret" -o "jsonpath={.data.${key//./\\.}}" \
        | base64 -d >"$MTLS_DIR/$key" 2>/dev/null || [[ ! -s "$MTLS_DIR/$key" ]]; then
      echo "cannot read $key from Secret $NAMESPACE/$secret" >&2
      return 1
    fi
  done
  chmod 600 "$MTLS_DIR"/*

  $kubectl -n "$NAMESPACE" port-forward "svc/${RELEASE}-visualizer-backend" \
    "${local_port}:${remote_port}" >/dev/null 2>&1 &
  MTLS_PF=$!

  MTLS_BASE="https://${host}:${local_port}"
  MTLS_CURL=(curl -s --cacert "$MTLS_DIR/ca.crt" --cert "$MTLS_DIR/tls.crt" --key "$MTLS_DIR/tls.key"
             --resolve "${host}:${local_port}:127.0.0.1")

  local i
  for i in $(seq 1 30); do
    # Any TLS answer, even a 404, proves the forward is up and the handshake works.
    if "${MTLS_CURL[@]}" -o /dev/null --max-time 2 "$MTLS_BASE/" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "the $role listener did not answer through the port-forward on :$local_port" >&2
  return 1
}

mtls_close() {
  [[ -n "$MTLS_PF" ]] && kill "$MTLS_PF" 2>/dev/null
  [[ -n "$MTLS_DIR" && -d "$MTLS_DIR" ]] && rm -rf "$MTLS_DIR"
  MTLS_PF=""
  MTLS_DIR=""
}
