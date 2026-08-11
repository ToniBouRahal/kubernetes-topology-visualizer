#!/usr/bin/env bash
# PreToolUse guard for Write/Edit — ADR-001 §12 instruction 10.
#
# Hard-denies writes to read-only reference material. Warns on writes outside the repository.
# Exit 2 blocks the tool call and returns stderr to the model.
#
# Reads the tool-call JSON on stdin.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

INPUT="$(cat)"
FILE_PATH="$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // .tool_input.path // empty' 2>/dev/null)"

[[ -z "$FILE_PATH" ]] && exit 0

# Resolve without requiring the file to exist yet.
ABS="$(cd "$(dirname "$FILE_PATH")" 2>/dev/null && pwd)/$(basename "$FILE_PATH")" || ABS="$FILE_PATH"

# ── Hard deny: read-only reference material ────────────────────────────────────────────────
case "$ABS" in
  /home/it-laptop/claude-test-fyp/*)
    echo "BLOCKED: $ABS is inside the reference prototype." >&2
    echo "ADR-001 §12 instruction 3: do not modify or delete the reference prototype." >&2
    echo "Copy what you need into the target repository instead." >&2
    exit 2
    ;;
  "$REPO_ROOT"/poc-kind-topology/*)
    echo "BLOCKED: $ABS is pre-existing user content, preserved but not part of this project." >&2
    echo "It is gitignored. Delete it manually if you no longer want it." >&2
    exit 2
    ;;
esac

# ── Hard deny: generated artifacts that must never be hand-edited (ADR-003 §5) ─────────────
case "$ABS" in
  "$REPO_ROOT"/contracts/openapi.json)
    echo "BLOCKED: contracts/openapi.json is generated." >&2
    echo "Edit backend/app/domain/models.py, then run 'make contracts'." >&2
    exit 2
    ;;
  "$REPO_ROOT"/frontend/src/api/generated/*)
    echo "BLOCKED: frontend/src/api/generated/ is generated from contracts/openapi.json." >&2
    echo "Edit the Pydantic models and regenerate; never hand-edit the client." >&2
    exit 2
    ;;
esac

# ── Warn: outside the target repository ────────────────────────────────────────────────────
if [[ "$ABS" != "$REPO_ROOT"/* ]]; then
  echo "NOTE: $ABS is outside the implementation repository ($REPO_ROOT)." >&2
  echo "ADR-001 §12 instruction 10 restricts implementation writes to the target repo." >&2
fi

exit 0
