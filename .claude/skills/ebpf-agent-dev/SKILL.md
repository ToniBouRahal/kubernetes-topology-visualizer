---
name: ebpf-agent-dev
description: >
  Build, regenerate, and validate the eBPF collector in agent/. Load before editing
  agent/bpf/*.c, running go generate, changing the event struct, or debugging verifier
  rejections and missing events.
---

# eBPF agent development

ADR: `docs/adr/ADR-002-ebpf-node-agent.md`. Kernel 6.8+, BTF at `/sys/kernel/btf/vmlinux`.

## 1. The filter invariant — the most important rule here

Emit an event only when **all four** conditions hold:

```c
ctx->family   == AF_INET          /* 2 */
ctx->protocol == IPPROTO_TCP      /* 6 */
ctx->oldstate == TCP_SYN_SENT     /* 2 */
ctx->newstate == TCP_ESTABLISHED  /* 1 */
```

**Dropping the `oldstate == TCP_SYN_SENT` check reintroduces false reverse edges from accepted
server sockets.** An accepted socket also reaches `ESTABLISHED`, from `TCP_SYN_RECV`. The reference
prototype checks only `newstate` (`tcp_connect.bpf.c:56`) and that is exactly the defect ADR-001 §3
records. Only active opens are edges.

## 2. The privacy invariant

No payload bytes in BPF maps, Go structs, logs, or output. No `bpf_probe_read` of any socket
buffer. Asserted structurally by test T-2.7.

Raw source/destination IPs are logged **only** when `AGENT_DEBUG_RAW_EVENTS=true`. Default is off.
The prototype logs them unconditionally at `main.go:349`.

## 3. Byte order — keep addresses as `[4]byte`

The tracepoint gives `saddr`/`daddr` as `__u8[4]` in network byte order. Keep them `[4]byte` in
both C and Go and never convert.

The prototype copies them into a `__u32` and reads them back with `binary.LittleEndian`
(`main.go:103-107`) — a double reversal that works only by accident on little-endian x86.

## 4. Struct synchronisation

The C `struct event` and its Go counterpart must match **byte for byte**. Field order avoids
implicit padding. Changing one requires changing the other *and* bumping the `version` field.
Test T-2.1 asserts the layout.

## 5. Regeneration

```bash
# vmlinux.h from the running kernel's BTF (committed; regenerate only on purpose)
bpftool btf dump file /sys/kernel/btf/vmlinux format c > agent/bpf/vmlinux.h

# bpf2go bindings — runs in the builder container because host clang is 14
make generate
```

Two non-obvious requirements, both validated in the prototype's Dockerfile and worth preserving:

- the `bpf2go` flag `-idirafter /usr/include/x86_64-linux-gnu`
- the symlink `ln -sf /usr/include/x86_64-linux-gnu/asm /usr/include/asm`, without which
  `linux/types.h` fails to resolve `asm/types.h`

## 6. Ring buffer, not perf

Use `BPF_MAP_TYPE_RINGBUF`. The prototype uses a perf event array.

**Lost events need explicit accounting.** `perf.Record.LostSamples` has no ring-buffer equivalent —
drops are only visible where `bpf_ringbuf_reserve()` returns `NULL`. Increment a counter in a
`BPF_MAP_TYPE_ARRAY` on that branch and poll it into the `kernel_samples_lost` metric. Without
this, ADR-001 §6's "expose lost kernel samples" is silently unmet.

## 7. PID is best-effort

`bpf_get_current_pid_tgid()` on this tracepoint can fire in softirq context, where the current task
is not the connecting process. PID is metadata only and must **never** participate in graph
identity or the edge key.

## 8. Resolution is asymmetric

Source and destination resolve by different rules — see `topology-contract` and ADR-002 D-2.4.
Briefly: a source resolves to its owning workload and **never** to a Service; a destination tries
ClusterIP, then EndpointSlice+port, then ambiguity-preserved workload, then external, then
unresolved. Never pick a Service arbitrarily. Unresolved and external are different states.

## 9. Debug ladder — "no events appear"

1. `bpftool prog list` — is the program actually attached?
2. `test -r /sys/kernel/btf/vmlinux` — BTF present?
3. Check the `events_filtered` metric **before** suspecting the kernel. The four-condition filter
   is strict by design.
4. Is the traffic a genuine *new* active open? A reused pooled connection emits nothing — this is
   expected behaviour, not a bug.
5. Is the destination port in `INFRASTRUCTURE_PORTS`?

## 10. Verifier rejections

Hand the **full verifier log** to `/codex:rescue` before iterating. Verifier errors are opaque and
an independent second derivation is the best available tool. Do not guess-and-recompile.

## 11. Before you finish

- [ ] All four filter conditions present?
- [ ] No payload read anywhere?
- [ ] Addresses still `[4]byte` end to end?
- [ ] C and Go structs still byte-identical?
- [ ] Lost-sample counter still incremented and exported?
- [ ] Raw IP logging still behind the debug flag?
- [ ] `cd agent && go test ./... -count=1` passes?
