# Byte accounting — feasibility spike and decision

**Task:** P4-A22 (spike), P4-X1 (decision gate), P4-T9 (this record)
**Governs:** ADR-002 D-2.8
**Date:** 2026-08-21
**Kernel:** 6.8.0-136-generic, x86_64, BTF present
**Code:** `agent/bpf/tcp_bytes_spike.bpf.c`, `agent/internal/spike/`
**Reproduce:** `make spike-bytes`

## Decision

**Byte counters will not be added to the edge model. `connection_count` remains the sole edge
weight, and edge intensity stays connection-based (task P4-F17 is closed as declined, not
deferred-and-forgotten).**

This is one of the two outcomes ADR-002 D-2.8 explicitly permits. The spike did not fail — it
answered the question, and the answer is that this measurement cannot be presented honestly with
the attach point available.

## Why, in one paragraph

Per-connection byte totals are *exactly* measurable, but only at the moment a connection closes.
Whether that is useful depends entirely on how long the workload holds its connections open — a
property the person reading the graph cannot see. On short-lived HTTP traffic, coverage is ~99%.
On persistent connections, it is ~0%: the spike watched 8 pooled connections carry **32.3 MB** and
contribute **nothing** to the window's totals. Since persistent connections are usually the
*busiest* edges (database pools, gRPC channels, keep-alive), a byte-weighted graph would draw the
heaviest edges as the faintest. A metric that is wrong in a consistent direction, with no
indication to the reader, is worse than a metric that is absent.

## What was measured

### 1. The tracepoint carries no byte counts (confirmed)

`struct trace_event_raw_inet_sock_set_state` provides addresses, ports, family, protocol and the
state transition — and nothing else. This matches D-2.8's prediction and rules out obtaining
bytes from the existing attach point alone.

### 2. A cheaper source than per-packet probes exists

D-2.8 anticipated `tcp_sendmsg`/`tcp_cleanup_rbuf` kprobes firing on every send and receive. There
is a cheaper option: `struct tcp_sock` carries cumulative `bytes_sent` and `bytes_received` as
`u64`. Reading them **once**, when the socket enters `TCP_CLOSE` on the tracepoint already
attached, avoids per-packet overhead entirely — one extra event per connection.

CO-RE relocated both fields successfully on this kernel; the program loads and verifies.

### 3. The numbers are exact

| measurement | expected | reported | delta |
|---|---:|---:|---:|
| `bytes_sent` | 65,536 | 65,536 | **0** |
| `bytes_received` | 4,096 | 4,096 | **0** |
| cumulative over 10 writes × 1 KiB, one connection | 10,240 | 10,240 | **0** |

TCP counts payload bytes, not framing, so exactness — not approximation — was the right bar.
There is no sampling error to characterise.

### 4. Both ends must be matched, or the count inflates by half

The first coverage run reported a **151.3%** close-to-open ratio. That was a measurement artifact,
not a result: the close handler saw *every* socket reaching `TCP_CLOSE`, including the server side
of connections whose client side the agent never recorded. Counting those would double-count the
same traffic on locally-terminated edges.

The fix is an LRU hash keyed by socket pointer, populated by the active-open filter and consumed at
close. With it, the same workload measured **98.1%**, and 272 closes were correctly discarded as
not-an-active-open. This is D-2.8's predicted map pressure made concrete: one live entry per
outbound connection, 16,384 entries in the spike.

### 5. Coverage depends entirely on connection lifetime — the finding that decides it

| workload | connections | bytes transferred | bytes reported in-window | coverage |
|---|---:|---:|---:|---:|
| demo cluster (short HTTP) | 515 active opens | — | 907,617 | **99.4%** close-to-open |
| 8 persistent connections, 20 s | 8 | **32,342,016** | 636,842 *(unrelated host traffic)* | **~0%** |

The second row is the whole argument. Those connections were transferring continuously for the
entire window. Every byte was invisible, because the counter is only readable at close. A separate
test confirms the mechanism directly: a still-open connection that has moved 8 KiB reports zero
close events.

A secondary problem compounds it. Even when a connection *does* close, its full lifetime total is
attributed to the one-minute bucket containing the **close**, not the buckets where the traffic
actually flowed. A connection open for five minutes dumps five minutes of bytes into one bucket.

## What would make this work

Sampling live sockets periodically instead of waiting for close, using `bpf_iter/tcp` (kernel 5.8+)
to walk established sockets safely once per bucket and record the *delta* in each counter. That
fixes both defects at once: long-lived connections report while still open, and bytes land in the
bucket where they flowed.

It is not a small change — a new program type, a per-socket previous-value map, delta arithmetic
that must survive counter wrap and socket reuse, and a sampling interval coupled to the bucket
width. Caching socket pointers in a map and dereferencing them later is *not* a safe shortcut: the
pointer may refer to freed memory after close. `bpf_iter/tcp` is the correct mechanism precisely
because the kernel guarantees the sockets it yields are live.

That is the right shape for future work, and it is recorded here so the next person starts from the
measurement rather than repeating it.

## Status of the spike code

`agent/bpf/tcp_bytes_spike.bpf.c` and `agent/internal/spike/` are retained as the evidence for this
decision and are **not** part of the shipped agent — the collector's capture path was never
modified. They build and run only under `make spike-bytes`, behind the `privileged` build tag.

## Consequences for the rest of the system

- `connection_count` stays the edge weight in `contracts/ids.md`, the API, and the schema. No
  contract change, no migration.
- The API's byte fields remain absent rather than zero, and absent still means "not measured" —
  which this record now justifies rather than merely asserts.
- P4-F17 (byte-based edge intensity) is closed as declined.
