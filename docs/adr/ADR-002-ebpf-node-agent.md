# ADR-002: eBPF Node Agent — Collection, Resolution, Aggregation, Delivery

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §5.1, §5.2 · Source of truth §7, §8, §9, §18
- **Component path:** `agent/`
- **Owning phases:** Phase 1 (primary), Phase 2 (delivery), Phase 4 (byte-accounting spike)
- **Language / runtime:** Go 1.23+, `github.com/cilium/ebpf`, CO-RE, Linux 6.8+ with BTF

## 1. Scope

This ADR covers the DaemonSet agent: the BPF program, the event reader, Kubernetes identity
resolution, edge aggregation, and batch delivery. It does **not** cover the wire format (ADR-003),
the receiving API (ADR-004), or the DaemonSet manifest and RBAC (ADR-007).

## 2. What the prototype proved, and what must change

The reference agent is 357 lines of Go plus 71 lines of BPF C. It proves the data path end to end
on this exact kernel. Treat it as a validated feasibility artifact, not as a starting skeleton.

### Reuse deliberately

| Prototype asset | Location | Why it is worth keeping |
|---|---|---|
| Tracepoint choice and struct layout | `bpf/tcp_connect.bpf.c:18-31` | `sock/inet_sock_set_state` field offsets are verified on this machine. |
| The `bpf2go` invocation | `main.go:6` | `-target bpfel -idirafter /usr/include/x86_64-linux-gnu` plus the `ln -sf /usr/include/x86_64-linux-gnu/asm /usr/include/asm` step in `agent/Dockerfile:12-16` is the non-obvious incantation that makes `linux/types.h` resolve. Preserve both. |
| Infrastructure port list | `main.go:63-75` | Eleven verified control-plane ports. Becomes the default of `INFRASTRUCTURE_PORTS`. |
| SharedInformerFactory approach | `main.go:138-204` | The comment at `main.go:134-137` records why informers replaced raw `Watch()` loops: file-descriptor exhaustion. Do not regress to watches. |
| Cache-sync-before-attach ordering | `main.go:291-293` | Prevents a burst of unresolved events at startup. |

### Must change — each of these is a defect, not a simplification

| # | Prototype behaviour | Location | Required behaviour |
|---|---|---|---|
| C1 | Filters on `newstate == TCP_ESTABLISHED` only | `tcp_connect.bpf.c:56` | Also require `oldstate == TCP_SYN_SENT` (value 2). Without it, an *accepted* server socket emits a false reverse edge. This is the single most important correctness fix in the agent. |
| C2 | `BPF_MAP_TYPE_PERF_EVENT_ARRAY` | `tcp_connect.bpf.c:44` | Use `BPF_MAP_TYPE_RINGBUF` (ADR-001 §5.1 item 2). |
| C3 | Hand-declared tracepoint struct | `tcp_connect.bpf.c:18-31` | Generate `vmlinux.h` from BTF and use CO-RE types where practical. |
| C4 | Addresses copied `__u8[4]` → `__u32`, then read with `binary.LittleEndian` | `tcp_connect.bpf.c:36-37`, `main.go:103-107` | A double byte reversal that only works by accident on little-endian x86. Declare the address as `[4]byte` in both C and Go and never convert. |
| C5 | `resolveIP` applies Service-then-Pod lookup to *both* endpoints | `main.go:109-119` | Source and destination resolve differently. See D-2.4. |
| C6 | Resolution yields `namespace/name` strings | `main.go:152`, `main.go:215` | Must yield canonical IDs per ADR-003. No owner-reference walking exists at all today. |
| C7 | `edgeCounts` reset **before** the POST | `main.go:250` vs `main.go:265` | Loses a batch on every failed delivery. Clear only after `2xx`. |
| C8 | Bare `http.Post`, no retry, no `batch_id`, no queue bound | `main.go:265` | See D-2.6. |
| C9 | Raw source/destination IPs logged unconditionally | `main.go:349` | ADR-001 §6: raw event logging off by default; never log external IPs at default level. |
| C10 | No `first_seen` / `last_seen` per edge | `main.go:352-355` | Both are required fields in the batch contract. |
| C11 | `rec.LostSamples` logged and the record discarded | `main.go:328-331` | Must become an exported counter metric. Ring buffer has no equivalent field — see D-2.2. |
| C12 | No `/healthz`, no `/metrics`, no graceful shutdown | — | Required by ADR-001 §5.1 item 8 and §6. |

## 3. Decisions

### D-2.1 — BPF program

Attach to `tracepoint/sock/inet_sock_set_state`. Emit an event only when **all** hold:

```c
ctx->family   == AF_INET       /* 2 */
ctx->protocol == IPPROTO_TCP   /* 6 */
ctx->oldstate == TCP_SYN_SENT  /* 2 */
ctx->newstate == TCP_ESTABLISHED /* 1 */
```

The emitted struct is versioned and field-ordered to avoid implicit padding:

```c
struct event {
    __u64 timestamp_ns;   /* bpf_ktime_get_ns, monotonic */
    __u32 pid;            /* best effort, see note */
    __u8  saddr[4];       /* network byte order, never converted */
    __u8  daddr[4];
    __u16 sport;          /* host byte order — the tracepoint already ntohs'd */
    __u16 dport;
    __u8  family;         /* AF_INET */
    __u8  protocol;       /* IPPROTO_TCP */
    __u8  version;        /* event schema version, starts at 1 */
    __u8  _pad;
};
```

The program must never read, copy, or reference socket payload data. No `bpf_probe_read` of any
buffer. This invariant is asserted by a test (T-2.7) and by the `ebpf-agent-dev` skill.

**PID caveat:** `bpf_get_current_pid_tgid()` on this tracepoint can fire in softirq context, where
the current task is not the connecting process. Treat PID as best-effort metadata only — ADR-001
§5.1 already says "process ID when available". It must never participate in graph identity.

### D-2.2 — Lost-event accounting

`perf.Record.LostSamples` has no ring-buffer equivalent. Drops are detected in BPF when
`bpf_ringbuf_reserve()` returns `NULL`. Maintain a `BPF_MAP_TYPE_ARRAY` counter map incremented on
that branch, and have the Go reader poll it into the `kernel_samples_lost` metric. Without this,
ADR-001 §6 ("expose lost kernel samples") is silently unmet.

### D-2.3 — Informers

Run shared informers for Pods, ReplicaSets, Deployments, StatefulSets, DaemonSets, Jobs, Services,
EndpointSlices, and Namespaces. Wait for cache sync before attaching the BPF program.

Scope the Pod informer to the local node with a field selector (`spec.nodeName=$NODE_NAME`) for
source resolution, but keep destination lookups cluster-wide — a pod on node A routinely connects
to a pod on node B. Getting this backwards produces silently unresolved destinations on multi-node
clusters, which Phase 5 explicitly validates.

### D-2.4 — Asymmetric endpoint resolution

Source and destination are **not** symmetric. This is the correction to C5.

```text
SOURCE
  pod IP → Pod → follow ownerReferences → collapse Pod→ReplicaSet→Deployment
         → k8s:<cluster>:<ns>:<Kind>:<name>
  never resolves to a Service

DESTINATION
  1. ClusterIP present in Service cache            → that Service
  2. pod IP in EndpointSlice(s) AND the observed
     dport matches a declared Service targetPort   → that Service
  3. ambiguous (multiple Services match)           → destination workload,
                                                     candidate Service names as metadata
  4. pod IP with no matching Service               → owning workload
  5. node/host IP                                  → kind=host, excluded from default graph
  6. routable non-cluster IP                       → external:EXTERNAL (IP never persisted)
  7. private/cluster IP, unresolved                → unresolved, increment counter, do not
                                                     call it external
```

Rule 3 is mandatory: never pick a Service arbitrarily (ADR-001 §5.2). Rule 7 is what stops a
CNI-timing race from being silently misreported as internet traffic.

### D-2.5 — Aggregation

Aggregate in memory keyed by `(source_id, target_id, protocol, destination_port)`. Track
`connection_count`, `first_seen`, and `last_seen` per key. Flush every
`AGENT_FLUSH_INTERVAL_SECONDS` (default 10, per source of truth §9).

Wall-clock timestamps are derived from the monotonic kernel timestamp plus a boot-time offset
captured once at startup; do not call `time.Now()` per event.

### D-2.6 — Delivery

1. On flush, move the aggregation map into an immutable batch and assign a ULID `batch_id`. Reset
   the live map to a fresh one — the batch is now owned by the queue, so C7 cannot recur.
2. Push onto a bounded channel of depth `AGENT_MAX_PENDING_BATCHES`. On overflow, drop the
   **oldest** batch and increment `batches_dropped`.
3. A single delivery goroutine POSTs with exponential backoff **plus jitter**. Remove the batch from
   the queue only on `2xx`. Treat `200` (already ingested) exactly like `202`.
4. `4xx` other than `408`/`429` is permanent: drop, increment `batches_rejected`, log the status and
   the `batch_id` — never the payload.
5. On `SIGTERM`, stop the reader, flush the aggregation map as a final batch, drain the queue with a
   bounded deadline, then exit.

### D-2.7 — Configuration and observability

Environment variables from ADR-001 §5.7: `CLUSTER_ID`, `BACKEND_INGEST_URL`,
`AGENT_FLUSH_INTERVAL_SECONDS`, `AGENT_MAX_PENDING_BATCHES`, `INFRASTRUCTURE_PORTS`, plus
`NODE_NAME` (downward API) and `AGENT_DEBUG_RAW_EVENTS` (default `false`, gates C9).

Expose on a container port: `/healthz`, `/readyz` (ready only after cache sync and BPF attach), and
`/metrics` in Prometheus text format with `raw_events_received`, `kernel_samples_lost`,
`events_filtered`, `endpoints_unresolved`, `batches_sent`, `batches_retried`, `batches_dropped`,
`batches_rejected`, `queue_depth`.

### D-2.8 — Byte accounting (Phase 4 only)

Time-boxed spike, separate branch. Do not touch until Phases 1–3 pass. `inet_sock_set_state` carries
no byte counts, so this requires a second attach point (`tcp_sendmsg`/`tcp_cleanup_rbuf` kprobes or
a socket-level tracepoint) keyed by socket pointer with an LRU map — an increase in complexity and
map pressure. If accounting is unreliable, keep `connection_count` and document the failure mode
under `docs/evaluation/`. Both outcomes satisfy ADR-001; silently shipping unreliable numbers does
not.

## 4. Implementation guide

```text
agent/
  cmd/agent/main.go          wiring, signal handling, HTTP server for health+metrics
  internal/collector/        BPF load/attach, ringbuf reader, lost-counter poller
  internal/resolver/         informers, owner-reference walking, D-2.4 rules
  internal/aggregate/        edge key, counters, first/last seen, flush
  internal/delivery/         batching, ULID, bounded queue, retry, shutdown drain
  internal/metrics/          counters and the /metrics handler
  bpf/
    tcp_connect.bpf.c
    vmlinux.h                generated, committed, regeneration documented
  Dockerfile                 two-stage; keep the /usr/include/asm symlink
  go.mod
```

Build order — each step ends runnable:

1. `bpftool btf dump file /sys/kernel/btf/vmlinux format c > bpf/vmlinux.h`
2. BPF program with the four-condition filter, ring buffer, lost counter → `go generate ./...`
3. Ring-buffer reader printing structured JSON events (no raw IPs at default log level)
4. Informers + resolver, unit-tested against fake clients before any cluster run
5. Aggregation with first/last seen
6. Delivery: ULID, queue, retry, shutdown — Phase 2
7. Health, readiness, metrics
8. Dockerfile, then hand off to ADR-007 for the DaemonSet

`internal/resolver` and `internal/aggregate` must be pure functions over injected caches and a
`Clock` interface. They carry most of the correctness risk and must be testable without a kernel or
a cluster.

## 5. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`ebpf-agent-dev`** (custom — spec below) | Every session touching `agent/bpf/` or `internal/collector/` | Holds the four-condition filter, the regeneration command, and the no-payload invariant. These are exactly the details that get quietly lost across six phases. |
| **`gopls-lsp`** | All Go work | Owner-reference walking and the resolver refactor need real symbol intelligence. `/plugin install gopls-lsp@claude-plugins-official` |
| **`context7`** | Before writing any `cilium/ebpf` or `client-go` call | The prototype pins `cilium/ebpf v0.15.0` and `k8s.io/* v0.29.3`. Ring-buffer and informer APIs have moved. ADR-001 §12 instruction 8 requires verifying current compatible versions — do this from docs, not from recall. |
| **`topology-contract`** (custom, ADR-003) | Editing `internal/resolver` or the batch struct | Canonical IDs are produced here and consumed by three other components. |
| **`adr-guard`** (custom) | Any proposal to add IPv6, UDP, or a second attach point outside D-2.8 | ADR-001 §4.2. |

### Situational

- **`codex:rescue`** — genuinely valuable when the BPF verifier rejects a program. Verifier errors
  are opaque and a second model that reasons about them independently is worth the call.
  Use `/codex:rescue` with the verifier log attached.
- **`/code-review high`** — at the end of Phase 1 and Phase 2 on `agent/`.
- **`/security-review`** — before Phase 5, focused on the privileged container surface.
- **`k8s-demo-loop`** (ADR-007) — once the agent is deployable in kind.

### Do not use

`frontend-design`, `dataviz`, `artifact-*` — wrong component. `terraform` — not the deployment
decision. Do not reach for a Kubernetes MCP plugin; none exists.

### `ebpf-agent-dev` skill specification

```yaml
name: ebpf-agent-dev
description: >
  Build, regenerate, and validate the eBPF collector in agent/. Load before editing
  agent/bpf/*.c, running go generate, changing the event struct, or debugging verifier
  rejections and missing events.
```

Body must contain:

1. **Regeneration**: the exact `bpf2go` command, the `-idirafter` flag, the `/usr/include/asm`
   symlink requirement, and that `go generate ./...` runs in the builder container because host
   clang is 14.
2. **The filter invariant**: all four conditions of D-2.1, with the sentence "dropping the
   `oldstate == TCP_SYN_SENT` check reintroduces false reverse edges from accepted server sockets".
3. **The privacy invariant**: no payload bytes in BPF maps, Go structs, logs, or output; raw IPs
   only behind `AGENT_DEBUG_RAW_EVENTS=true`.
4. **Byte-order rule**: addresses stay `[4]byte` end to end; never `binary.LittleEndian` on an
   address.
5. **Struct sync**: the C `struct event` and the Go type must match byte for byte; changing one
   requires changing both plus bumping `version`.
6. **Debug ladder** for "no events appear": `bpftool prog list` → confirm attach; verify BTF at
   `/sys/kernel/btf/vmlinux`; check `events_filtered` before suspecting the kernel; confirm traffic
   is genuinely a new active open rather than a reused connection.

## 6. Test requirements (ADR-001 §8 rows 1–3)

| ID | Test | Kernel required |
|---|---|---|
| T-2.1 | Event struct layout matches C byte for byte | no |
| T-2.2 | IPv4 address parsing, both endianness directions | no |
| T-2.3 | Active-open filter: `SYN_SENT→ESTABLISHED` accepted; `SYN_RECV→ESTABLISHED` rejected | no |
| T-2.4 | Non-`AF_INET` and non-TCP rejected | no |
| T-2.5 | Owner resolution: Pod→ReplicaSet→Deployment collapses; ownerless Pod stays `Pod` | no |
| T-2.6 | Service port matching, including the ambiguous-multi-Service case → metadata, not a guess | no |
| T-2.7 | No payload field exists in the BPF map, Go struct, or emitted JSON (structural assertion) | no |
| T-2.8 | Infrastructure ports filtered; counter increments | no |
| T-2.9 | Aggregation: N identical events → one edge, count N, correct first/last seen | no |
| T-2.10 | Delivery: success, timeout, retry-with-jitter, duplicate `200`, queue overflow drops oldest, shutdown flush | no |
| T-2.11 | Ring buffer receives real events from live traffic | **yes** |
| T-2.12 | Lost-sample counter increments under synthetic burst | **yes** |

T-2.11 and T-2.12 need privileged execution and are separated from ordinary CI, with a documented
local command, per ADR-001 §8.

## 7. Acceptance criteria

Phase 1 (ADR-001 §7), all verifiable from agent output alone:

- unmodified demo workloads produce captured IPv4 TCP events;
- `Frontend → Backend` and `Backend → Redis` appear as service-level edges;
- multiple replicas collapse to one logical node and edge;
- external traffic becomes `source → EXTERNAL`, never one node per IP;
- accepted server sockets produce no reverse edges (C1);
- no payload bytes anywhere (T-2.7);
- the agent runs on all three kind nodes.

Phase 2 adds: retries during a backend outage without agent termination; duplicate `batch_id`
counted once (verified from the backend side, ADR-004).

## 8. Consequences

- **Privileged container.** Accepted for the FYP (ADR-001 §5.7, source of truth §18). Capability
  reduction is attempted only after correctness and must not block the demo.
- **Kernel coupling.** The tracepoint layout is verified on 6.8; CO-RE via BTF is what makes the
  kubeadm validation in Phase 5 plausible. If it still fails there, record it as an
  environment-specific value rather than special-casing the code.
- **Connections are not requests.** Connection pooling means a busy service can show a low count.
  Label the metric `connections` everywhere (ADR-001 §10 mitigation).
- **Node-scoped observation.** The agent sees only active opens originating on its own node. Both
  directions of a conversation appear only because both endpoints' nodes run an agent — which is
  why Phase 5's "reports from every node" criterion is a correctness check, not a scale check.

## 9. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). Tick a box only when its named test
passes. `[ ]` open · `[~]` in progress · `[x]` done · `[!]` blocked · `[-]` dropped with reason.

### Phase 1 — capture and resolution

- [ ] **P1-A1** Generate and commit `bpf/vmlinux.h` from `/sys/kernel/btf/vmlinux` — §4
- [ ] **P1-A2** BPF four-condition filter incl. `oldstate == TCP_SYN_SENT` — D-2.1 · test T-2.3
- [ ] **P1-A3** Ring buffer replaces perf event array — D-2.1 (C2)
- [ ] **P1-A4** Lost-event counter map on failed `bpf_ringbuf_reserve` — D-2.2 · test T-2.12
- [ ] **P1-A5** `bpf2go` build in container; `/usr/include/asm` symlink preserved — §4
- [ ] **P1-A6** Ring-buffer reader; addresses stay `[4]byte` end to end — D-2.1 (C4) · tests T-2.1, T-2.2
- [ ] **P1-A7** Lost-counter poller exports `kernel_samples_lost` — D-2.2
- [ ] **P1-A8** Informers for nine resource types; source node-scoped, destinations cluster-wide — D-2.3
- [ ] **P1-A9** Source resolution: owner-reference walk, Pod→ReplicaSet→Deployment — D-2.4 · test T-2.5
- [ ] **P1-A10** Destination resolution ladder, ambiguity preserved as metadata — D-2.4 · test T-2.6
- [ ] **P1-A11** Canonical ID construction — ADR-003 D-3.2 · load `topology-contract`
- [ ] **P1-A12** Infrastructure port filtering — §2 · test T-2.8
- [ ] **P1-A13** Ten-second aggregation with `first_seen`/`last_seen` — D-2.5 · test T-2.9
- [ ] **P1-A14** Structured batch logging; raw IPs behind `AGENT_DEBUG_RAW_EVENTS` — D-2.7 (C9)
- [ ] **P1-A15** Two-stage Dockerfile — §4
- [ ] **P1-A16** Unit tests T-2.1 – T-2.9 — §6 · **→ codex**

**Phase 1 gate** (ADR-001 §7): captured events from unmodified workloads · service-level edges ·
replicas collapse · external summarised · no false reverse edges (P1-A2) · no payload bytes
(T-2.7) · agent on every node (ADR-007 P1-K2).

### Phase 2 — delivery

- [ ] **P2-A17** ULID `batch_id` + immutable batch handoff, fixing C7 — D-2.6
- [ ] **P2-A18** Bounded queue, drop-oldest, `batches_dropped` — D-2.6 · test T-2.10
- [ ] **P2-A19** Retry with exponential backoff + jitter; `200` treated as success — D-2.6
- [ ] **P2-A20** Graceful shutdown: final flush, bounded drain — D-2.6
- [ ] **P2-A21** `/healthz`, `/readyz`, `/metrics` — D-2.7

### Phase 4 — byte-accounting spike

- [ ] **P4-A22** Bounded spike on a separate branch — D-2.8 · **→ codex**
- [ ] **P4-X1** Decision gate: propagate nullable bytes, or document the failure mode

### Privileged tests (out of ordinary CI, before release)

- [ ] **T-2.11** Ring buffer receives real events from live traffic
- [ ] **T-2.12** Lost-sample counter increments under synthetic burst
