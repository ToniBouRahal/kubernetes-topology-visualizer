// SPDX-License-Identifier: GPL-2.0
//
// Runtime TCP topology collector.
//
// Attaches to tracepoint/sock/inet_sock_set_state and emits one event per *active open* —
// a connection this host initiated. Events go to a ring buffer; drops are counted in BPF
// because the ring buffer, unlike a perf array, reports no lost-sample count to userspace.
//
// ADR-002 D-2.1, D-2.2. Types come from vmlinux.h (CO-RE), so field offsets relocate at load
// time rather than being hard-coded for one kernel.
//
// PRIVACY INVARIANT (ADR-001 §6): this program must never read, copy, or reference socket
// payload data. There is no bpf_probe_read of any buffer here, and there must never be one.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>

// vmlinux.h already provides IPPROTO_TCP and the TCP state enum (TCP_ESTABLISHED = 1,
// TCP_SYN_SENT = 2). Redefining them as macros would break those enum declarations.
// AF_INET is not in BTF, so it is defined here.
#define AF_INET 2

// Bumped whenever `struct event` changes shape. The Go side asserts a match.
#define EVENT_SCHEMA_VERSION 2

char LICENSE[] SEC("license") = "GPL";

// ── Counters ────────────────────────────────────────────────────────────────────────────────
// A plain array with atomic increments rather than a per-CPU map: at the target rate of
// ~1,000 events/s/node the contention is irrelevant, and userspace reads stay trivial.
enum stat_index {
	STAT_EVENTS_SUBMITTED = 0,
	STAT_RINGBUF_DROPPED = 1,  // → kernel_samples_lost; the whole reason this map exists
	STAT_FILTERED_FAMILY = 2,
	STAT_FILTERED_PROTOCOL = 3,
	STAT_FILTERED_TRANSITION = 4,
	STAT_TRACKING_MISSED = 5,
	STAT_TRACKING_FAILED = 6,
	STAT_MAX = 7,
};

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, STAT_MAX);
	__type(key, __u32);
	__type(value, __u64);
} stats SEC(".maps");

static __always_inline void stat_inc(__u32 index)
{
	__u64 *slot = bpf_map_lookup_elem(&stats, &index);
	if (slot)
		__sync_fetch_and_add(slot, 1);
}

// ── Event ───────────────────────────────────────────────────────────────────────────────────
// Field order avoids implicit padding, and _pad makes the trailing alignment explicit so the
// Go struct can mirror this byte for byte (40 bytes). Test T-2.1 asserts the layout.
//
// Addresses stay as raw 4-byte arrays in network byte order and are never widened to __u32.
// The prototype packed them into a __u32 and read them back little-endian — a double reversal
// that only works by accident on x86 (ADR-002 C4).
struct event {
	__u64 timestamp_ns;  // bpf_ktime_get_ns, monotonic
	__u32 pid;           // best effort only; see the note below
	__u8 saddr[4];       // network byte order
	__u8 daddr[4];       // network byte order
	__u16 sport;         // host byte order — the tracepoint already converted these
	__u16 dport;         // host byte order
	__u8 family;         // AF_INET
	__u8 protocol;       // IPPROTO_TCP
	__u8 version;        // EVENT_SCHEMA_VERSION
	__u8 outcome;        // 0 = established, 1 = failed/aborted during setup
	__u8 duration_known; // a matching SYN_SENT entry was observed
	__u8 _pad[3];
	__u64 duration_us;   // successful establishment time, not request latency
};

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 256 * 1024);
} events SEC(".maps");

// Bounded tracking: eviction sacrifices timing, never invents a zero-duration sample.
// Socket pointers are transient local keys only; they never leave this map.
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, __u64);
	__type(value, __u64);
} starts SEC(".maps");

// Force the type into the BTF emitted for this object so bpf2go generates a Go mirror.
const struct event *unused_event __attribute__((unused));

SEC("tracepoint/sock/inet_sock_set_state")
int trace_inet_sock_set_state(struct trace_event_raw_inet_sock_set_state *ctx)
{
	if (ctx->family != AF_INET) {
		stat_inc(STAT_FILTERED_FAMILY);
		return 0;
	}

	if (ctx->protocol != IPPROTO_TCP) {
		stat_inc(STAT_FILTERED_PROTOCOL);
		return 0;
	}

	__u64 socket = (__u64)ctx->skaddr;
	__u64 now = bpf_ktime_get_ns();
	if (ctx->newstate == TCP_SYN_SENT) {
		if (bpf_map_update_elem(&starts, &socket, &now, BPF_ANY))
			stat_inc(STAT_TRACKING_FAILED);
		return 0;
	}

	// Restrict outcomes to active opens. Accepted server sockets never qualify.
	// A close AFTER establishment is not a failed connection.
	if (ctx->oldstate != TCP_SYN_SENT ||
	    (ctx->newstate != TCP_ESTABLISHED && ctx->newstate != TCP_CLOSE)) {
		// Unusual transitions (e.g. simultaneous open) have no supported outcome.
		if (ctx->oldstate == TCP_SYN_SENT || ctx->newstate == TCP_CLOSE)
			bpf_map_delete_elem(&starts, &socket);
		stat_inc(STAT_FILTERED_TRANSITION);
		return 0;
	}

	__u64 *start = bpf_map_lookup_elem(&starts, &socket);
	__u64 duration_us = 0;
	__u8 duration_known = 0;
	if (start) {
		if (ctx->newstate == TCP_ESTABLISHED && now >= *start) {
			duration_us = (now - *start) / 1000;
			duration_known = 1;
		}
	} else {
		stat_inc(STAT_TRACKING_MISSED);
	}
	// Clean up even if ring-buffer reservation fails below.
	bpf_map_delete_elem(&starts, &socket);

	struct event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
	if (!e) {
		// The ring buffer is full and userspace is not draining fast enough. This is the
		// only place a drop is observable — there is no LostSamples equivalent on the
		// consumer side — so silence here means silent data loss (ADR-002 D-2.2).
		stat_inc(STAT_RINGBUF_DROPPED);
		return 0;
	}

	e->timestamp_ns = now;
	e->outcome = ctx->newstate == TCP_CLOSE;
	e->duration_known = duration_known;
	e->duration_us = duration_us;

	// Best effort only. This tracepoint can fire in softirq context, where the current task is
	// not the process that opened the connection. PID is metadata and must never take part in
	// graph identity or the edge key (ADR-002 D-2.1).
	e->pid = bpf_get_current_pid_tgid() >> 32;

	__builtin_memcpy(e->saddr, ctx->saddr, 4);
	__builtin_memcpy(e->daddr, ctx->daddr, 4);

	e->sport = ctx->sport;
	e->dport = ctx->dport;
	e->family = AF_INET;
	e->protocol = IPPROTO_TCP;
	e->version = EVENT_SCHEMA_VERSION;
	__builtin_memset(e->_pad, 0, sizeof(e->_pad));

	bpf_ringbuf_submit(e, 0);
	stat_inc(STAT_EVENTS_SUBMITTED);
	return 0;
}
