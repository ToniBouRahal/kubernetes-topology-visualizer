#!/usr/bin/env bash
# Known-vulnerability audit of all three dependency trees (ADR-014 D-14.11).
#
# Each tool answers the narrowest honest question for its ecosystem:
#   Go      govulncheck — only vulnerabilities the agent's code can actually reach, stdlib included
#   Python  pip-audit   — every package resolved into the backend's environment, dev tools included
#   npm     npm audit   — the lockfile, dev dependencies included: Vite and its plugins build the
#                         bundle that ships, so a compromised build tool is a compromised release
#
# Fails on any Go finding, any Python finding, and npm findings of HIGH or above. Tool versions
# are pinned so a scanner upgrade cannot change the verdict without a commit saying so.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOVULNCHECK_VERSION="v1.8.0"
PIP_AUDIT_VERSION="2.9.0"
VENV_PY="${VENV_PY:-$REPO_ROOT/backend/.venv/bin/python}"

failed=()

echo "== Go: govulncheck $GOVULNCHECK_VERSION (agent) =="
if ! (cd "$REPO_ROOT/agent" && go run "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION" ./...); then
  failed+=("go")
fi

echo "== Python: pip-audit $PIP_AUDIT_VERSION (backend environment) =="
if [[ ! -x "$VENV_PY" ]]; then
  echo "no backend environment at $VENV_PY — create it first (make backend-venv, or uv venv + uv pip install -e '.[dev]')" >&2
  failed+=("python")
else
  requirements="$(mktemp)"
  trap 'rm -f "$requirements"' EXIT
  # The backend itself is an editable install with no index entry to look up; everything else is
  # pinned to exactly what is installed, which is what --no-deps --disable-pip needs.
  uv pip freeze --python "$VENV_PY" --exclude-editable >"$requirements"
  if ! uvx "pip-audit==$PIP_AUDIT_VERSION" -r "$requirements" --no-deps --disable-pip --progress-spinner off; then
    failed+=("python")
  fi
fi

echo "== npm: npm audit, high and above (frontend lockfile) =="
if ! (cd "$REPO_ROOT/frontend" && npm audit --audit-level=high); then
  failed+=("npm")
fi

if ((${#failed[@]})); then
  echo "dependency audit FAILED: ${failed[*]}" >&2
  exit 1
fi
echo "dependency audit: no known vulnerabilities at the gated severities"
