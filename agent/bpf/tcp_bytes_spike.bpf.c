// SPDX-License-Identifier: GPL-2.0
//
// SPIKE — byte accounting feasibility (ADR-002 D-2.8, task P4-A22).
//
// NOT part of the shipped collector. Built and measured separately so the working capture path
// stays untouched while the question is answered.
//
// THE QUESTION: can per-edge bytes_sent/bytes_received be obtained reliably without per-packet
// overhead?
//
// THE APPROACH: struct tcp_sock carries cumulative u64 bytes_sent and bytes_received. Rather
// than probing every send and receive, read those counters ONCE when the socket closes, on the
// same sock/inet_sock_set_state tracepoint the collector already uses. One extra event per
// connection, no per-packet cost.
//
// The close event carries the same 4-tuple as the open, so userspace can reconstruct the edge
// key and attribute the bytes without holding any per-socket state.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

#define AF_INET 2

struct close_event {
	__u64 bytes_sent;
	__u64 bytes_received;
	__u8 saddr[4];
	__u8 daddr[4];
	__u16 sport;
	__u16 dport;
	__u8 _pad[4];
};

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 256 * 1024);
} closes SEC(".maps");

// Coverage counters. The decision does not hinge on whether the byte numbers are correct — the
// accuracy test already settles that — but on what FRACTION of connections report them inside an
// observation window. A connection still open when the window ends contributes nothing.
#define STAT_ESTABLISHED  0
#define STAT_CLOSED       1
#define STAT_BYTES_TOTAL  2
#define STAT_CLOSED_ZERO  3
#define STAT_UNMATCHED    4   // closed, but never seen as an active open (passive/server side)
#define STAT_MAX          5

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, STAT_MAX);
	__type(key, __u32);
	__type(value, __u64);
} spike_stats SEC(".maps");

// Sockets seen as ACTIVE opens. A close is only attributable to an edge the collector recorded
// if the same socket passed the active-open filter first — otherwise it is the server side of
// somebody else's connection, and counting it would double-count the same traffic.
//
// This map is also the point of D-2.8's warning about map pressure: it holds one entry per live
// outbound connection. LRU so it degrades by eviction instead of failing to insert.
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 16384);
	__type(key, __u64);
	__type(value, __u8);
} active_opens SEC(".maps");

static __always_inline void stat_add(__u32 slot, __u64 n)
{
	__u64 *v = bpf_map_lookup_elem(&spike_stats, &slot);
	if (v)
		__sync_fetch_and_add(v, n);
}

SEC("tracepoint/sock/inet_sock_set_state")
int trace_establish(struct trace_event_raw_inet_sock_set_state *ctx)
{
	if (ctx->family != AF_INET || ctx->protocol != IPPROTO_TCP)
		return 0;
	// The collector's exact active-open filter, so the denominator matches what it would record.
	if (ctx->oldstate != TCP_SYN_SENT || ctx->newstate != TCP_ESTABLISHED)
		return 0;
	stat_add(STAT_ESTABLISHED, 1);

	__u64 sk = (__u64)ctx->skaddr;
	__u8 one = 1;
	bpf_map_update_elem(&active_opens, &sk, &one, BPF_ANY);
	return 0;
}

const struct close_event *unused __attribute__((unused));

char LICENSE[] SEC("license") = "GPL";

SEC("tracepoint/sock/inet_sock_set_state")
int trace_close(struct trace_event_raw_inet_sock_set_state *ctx)
{
	if (ctx->family != AF_INET || ctx->protocol != IPPROTO_TCP)
		return 0;

	// Only the transition INTO TCP_CLOSE. A socket reaches it once, so each connection
	// reports its totals exactly once.
	if (ctx->newstate != TCP_CLOSE)
		return 0;

	// skaddr is the struct sock*. tcp_sock embeds it as its first member, so the cast is the
	// standard kernel idiom; CO-RE relocates the field offsets at load time.
	// Only sockets this agent saw open actively. Without this the server side of every
	// connection is counted too, inflating the result well past 100%.
	__u64 sk = (__u64)ctx->skaddr;
	if (!bpf_map_lookup_elem(&active_opens, &sk)) {
		stat_add(STAT_UNMATCHED, 1);
		return 0;
	}
	bpf_map_delete_elem(&active_opens, &sk);

	struct tcp_sock *tp = (struct tcp_sock *)ctx->skaddr;

	__u64 sent = 0;
	__u64 received = 0;
	// BPF_CORE_READ, not a direct dereference: skaddr points into kernel memory that this
	// program does not own, and the offsets differ between kernels.
	sent = BPF_CORE_READ(tp, bytes_sent);
	received = BPF_CORE_READ(tp, bytes_received);

	// A connection that carried nothing tells us nothing worth an event.
	if (sent == 0 && received == 0) {
		stat_add(STAT_CLOSED_ZERO, 1);
		return 0;
	}

	stat_add(STAT_CLOSED, 1);
	stat_add(STAT_BYTES_TOTAL, sent + received);

	struct close_event *e = bpf_ringbuf_reserve(&closes, sizeof(*e), 0);
	if (!e)
		return 0;

	e->bytes_sent = sent;
	e->bytes_received = received;
	__builtin_memcpy(e->saddr, ctx->saddr, 4);
	__builtin_memcpy(e->daddr, ctx->daddr, 4);
	e->sport = ctx->sport;
	e->dport = ctx->dport;
	__builtin_memset(e->_pad, 0, sizeof(e->_pad));

	bpf_ringbuf_submit(e, 0);
	return 0;
}
