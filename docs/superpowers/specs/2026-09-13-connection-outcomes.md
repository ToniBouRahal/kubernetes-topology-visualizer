# Connection outcomes

Feature 5 adds unsuccessful active-open IPv4 TCP connections and successful establishment timing.
The user authorized implementation. Existing namespace grouping changes remain in place.

Capture SYN_SENT entry time in a bounded socket-keyed BPF LRU map. Emit one successful event for
SYN_SENT -> ESTABLISHED and one unsuccessful event for SYN_SENT -> CLOSE. Never count accepted
server sockets or closes after establishment. Delete tracked state on terminal transition, even
on ring-buffer drop. Missing start records mean unknown timing, not zero; expose tracking misses.
Unsuccessful means failed/aborted: no inferred errno classifications. Pre-SYN_SENT failures,
in-flight attempts, IPv6, DNS and requests are outside this measurement. Timestamp the outcome.

Keep connection_count as successful establishments. Add optional failed_connection_count (nullable,
null means unmeasured), connect_latency_count (default 0), connect_latency_sum_us (default 0) to
edge ingest/read/detail models. Successful latency samples only. Sum counts and sums across agent
flushes, database buckets and namespace groups; mean milliseconds = sum_us / count / 1000.
Allow zero successes only with a positive failure count. Forbid negative counters, sample counts
above successful counts, and nonzero duration sums with zero samples. Existing v1 payloads remain
accepted; upgraded agents require the upgraded backend, which must be rolled out first.

Failure-only edges derive visible nodes normally. Mark failed/aborted counts in graph labels and
node dependencies using words as well as a dashed warning style. Display mean setup time only when
measured. Compare retains successful-connection semantics and excludes failure-only edges.
Do not present missing failure measurements as zero or publish a misleading failure percentage.

Forward SQL migration adds columns without rewriting history as measured. Test shared storage
semantics, duplicate ingestion, failed-only edges, weighted timing, generated schema, agent binary
layout, active/server socket behavior, successful opens, refusals and cancelled pending connections.
