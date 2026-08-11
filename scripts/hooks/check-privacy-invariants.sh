#!/usr/bin/env bash
# PostToolUse advisory — ADR-001 §6 privacy constraints.
#
# Never blocks. Surfaces edits that touch the two invariants which are easy to violate by
# accident and expensive to discover late: payload capture and raw external IP exposure.
set -uo pipefail

INPUT="$(cat)"
FILE_PATH="$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // empty' 2>/dev/null)"

[[ -z "$FILE_PATH" || ! -f "$FILE_PATH" ]] && exit 0

case "$FILE_PATH" in
  *.c|*.h|*.go|*.py|*.ts|*.tsx) ;;
  *) exit 0 ;;
esac

warn() { echo "PRIVACY CHECK ($FILE_PATH): $1" >&2; }

# Payload capture — the BPF program must never read socket buffers (ADR-002 D-2.1, test T-2.7).
if grep -qE 'bpf_probe_read[^(]*\((&|)[a-z_]*(buf|payload|data|msg)' "$FILE_PATH" 2>/dev/null; then
  warn "possible payload read in a BPF program. ADR-001 §6 forbids capturing payload bytes."
fi

if grep -qiE '\b(payload|packet_data|pkt_buf)\b' "$FILE_PATH" 2>/dev/null; then
  warn "a 'payload'-named symbol appeared. Confirm no payload bytes enter maps, structs, or logs."
fi

# Raw external IPs must not be logged at default level (ADR-002 D-2.7, prototype defect C9).
if grep -qE '(log|Printf|print|console\.log)[^\n]*\b(saddr|daddr|src_ip|dst_ip|remote_ip|srcIP|dstIP)\b' "$FILE_PATH" 2>/dev/null; then
  if ! grep -qE 'DEBUG_RAW_EVENTS|debugRawEvents' "$FILE_PATH" 2>/dev/null; then
    warn "raw IP appears in a log statement without a debug-flag guard. ADR-001 §6 requires raw event logging to be off by default."
  fi
fi

exit 0
