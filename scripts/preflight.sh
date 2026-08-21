#!/usr/bin/env bash
# Host preflight for the kind demo.
#
# Exists because of a real failure: kube-proxy on one node died with "too many open files", which
# silently broke Service routing on that node. The node still reported Ready, so the symptom was a
# frontend that could not resolve DNS and an agent that could not reach the API server — neither
# of which points at the actual cause.
#
# Run before `make demo-up`. ADR-001 §7 Phase 5 requires failure modes to be actionable.
set -uo pipefail

fail=0
warn=0

ok()   { printf '  \033[32mOK\033[0m   %s\n' "$1"; }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }
soft() { printf '  \033[33mWARN\033[0m %s\n' "$1"; warn=$((warn + 1)); }

echo "== Kernel =="
kernel=$(uname -r)
if [[ "$(printf '%s\n6.8\n' "${kernel%%-*}" | sort -V | head -1)" == "6.8" ]]; then
  ok "kernel $kernel (>= 6.8)"
else
  bad "kernel $kernel is below the 6.8 the agent is validated against"
fi

if [[ -r /sys/kernel/btf/vmlinux ]]; then
  ok "BTF present at /sys/kernel/btf/vmlinux"
else
  bad "no readable BTF at /sys/kernel/btf/vmlinux — the eBPF program cannot load (CO-RE needs it)"
fi

echo "== inotify limits =="
# kind runs many containers, each consuming inotify instances. Below these thresholds kube-proxy
# and kubelet fail with "too many open files" AFTER the cluster looks healthy.
check_sysctl() {
  local key="$1" minimum="$2" current
  current=$(sysctl -n "$key" 2>/dev/null || echo 0)
  if [[ "$current" -ge "$minimum" ]]; then
    ok "$key = $current (>= $minimum)"
  else
    bad "$key = $current, needs >= $minimum
         Fix:  sudo sysctl -w $key=$minimum
         Persist: echo '$key=$minimum' | sudo tee -a /etc/sysctl.d/99-kind.conf"
  fi
}
check_sysctl fs.inotify.max_user_watches 524288
check_sysctl fs.inotify.max_user_instances 512

echo "== Tooling =="
for tool in docker kind kubectl helm go; do
  if command -v "$tool" >/dev/null 2>&1; then
    ok "$tool present"
  else
    bad "$tool not on PATH — see docs/prerequisites.md"
  fi
done

if docker info >/dev/null 2>&1; then
  ok "docker daemon reachable"
else
  bad "cannot reach the docker daemon"
fi

echo "== Disk =="
avail=$(df -BG --output=avail / 2>/dev/null | tail -1 | tr -dc '0-9')
if [[ -n "$avail" && "$avail" -ge 20 ]]; then
  ok "${avail}G free on / (the kind node image alone is ~1G)"
else
  soft "only ${avail:-?}G free on /; image pulls may fail"
fi

echo
if [[ "$fail" -eq 0 ]]; then
  echo "preflight: ready ($warn warning(s))"
else
  echo "preflight: $fail blocking problem(s) — fix these before 'make demo-up'"
fi
exit "$fail"
