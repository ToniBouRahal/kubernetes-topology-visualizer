#!/usr/bin/env bash
# Privacy verification before release — ADR-008 D-8.7, ADR-001 §6 and §9, task P5-T18.
#
# Three separate claims, each checked rather than asserted:
#   1. no screenshot or recording exposes a credential or an individual external IP
#   2. raw event logging is off by default
#   3. no source file carries a hard-coded credential
#
# Screenshots are OCR'd because the risk is text a human skimmed past — a DSN in a log pane, an
# address in a tooltip — not something anyone would commit deliberately.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

pass=0; fail=0; skip=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
note() { printf '  \033[33mSKIP\033[0m %s\n' "$1"; skip=$((skip+1)); }

echo "== images and recordings =="
if ! command -v tesseract >/dev/null 2>&1; then
  note "tesseract not installed; cannot read text out of images"
else
  found=0
  while IFS= read -r img; do
    found=$((found+1))
    text="$(tesseract "$img" - 2>/dev/null)"
    if printf '%s' "$text" | python3 -c '
import re, sys
t = sys.stdin.read()
pats = {
  "public IPv4": r"\b(?!10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|127\.|0\.|169\.254\.)(\d{1,3}\.){3}\d{1,3}\b",
  "DSN":          r"postgres(ql)?://\S*",
  "password":     r"(?i)passw(or)?d\s*[:=]\s*\S+",
  "token":        r"(?i)\b(bearer|api[_-]?key)\b\s*[:=]?\s*\S{8,}",
}
hits = {k: sorted({m.group(0) for m in re.finditer(v, t)})[:3] for k, v in pats.items()}
hits = {k: v for k, v in hits.items() if v}
if hits:
    print(repr(hits)); sys.exit(1)
sys.exit(0)
' >/dev/null 2>&1; then
      ok "$(basename "$img")"
    else
      bad "$(basename "$img") appears to contain a credential or a public IP"
    fi
  done < <(find docs -type f \( -name '*.png' -o -name '*.jpg' -o -name '*.jpeg' \) 2>/dev/null)
  [[ "$found" -eq 0 ]] && note "no images found under docs/"
fi

echo "== raw event logging default =="
if grep -q 'env("AGENT_DEBUG_RAW_EVENTS", "false")' agent/cmd/agent/main.go; then
  ok "AGENT_DEBUG_RAW_EVENTS defaults to false"
else
  bad "the raw-event logging default is not false"
fi
if grep -q 'debugRawEvents: false' charts/topology-visualizer/values.yaml; then
  ok "the chart ships debugRawEvents: false"
else
  bad "the chart does not ship debugRawEvents: false"
fi

echo "== no committed credentials =="
# A real password in values.yaml would be shipped to every user of the chart.
if grep -qE '^\s+password:\s*""' charts/topology-visualizer/values.yaml; then
  ok "chart ships an empty database password"
else
  bad "the chart's database password is not empty"
fi

# Only TRACKED files. Scanning the working tree also reads backend/.venv and node_modules, where
# third-party docstrings contain example connection strings as documentation — a false positive
# that would train someone to ignore this check.
# Only files that would SHIP a credential. Three categories legitimately contain a DSN-with-password
# and are excluded by path, each for a stated reason rather than to make the check quiet:
#
#   tests/       - a DSN carrying a password is the INPUT to the test
#                  proving that value never reaches a response. Removing it would remove the proof.
#                  (Written without literal DSN syntax on purpose: this file is scanned too, and a
#                  checker that has to exempt itself is one that could hide a real leak later.)
#   templates/   - Helm templates build the DSN from values into a Secret; the password is a
#                  template reference, which is exactly the right design.
#   docs/        - the evaluation records discuss the above.
#
# What remains in scope is application source and any values file — where a literal credential
# would actually be distributed.
leaks=$(git ls-files -z 2>/dev/null \
  | xargs -0 grep -IE "(postgres(ql)?://[^ \"']*:[^ \"'@]+@)" 2>/dev/null \
  | grep -vE "^(backend/tests/|charts/[^:]*/templates/|docs/)" \
  | grep -viE "topology:\*\*\*|user:pw@host|\*\*\*@|localhost|example|user:password" | head -3)
if [[ -z "$leaks" ]]; then
  ok "no DSN with an inline credential in source or manifests"
else
  bad "a DSN with a credential appears in source:"
  printf '        %s\n' "$leaks"
fi

echo
echo "privacy verification: $pass passed, $fail failed, $skip skipped"
[[ "$fail" -eq 0 ]]
