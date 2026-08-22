# Limitations

What this system does not do, cannot do, or does only under stated conditions.

Each entry says what the limitation is, whether it was **measured or argued**, and what would lift
it. Where a number appears it came from a run recorded under `docs/evaluation/`, not an estimate.

The organising principle throughout the project has been that a wrong specific answer is worse than
an honest general one. Several entries below exist because that principle was applied — a metric
was withdrawn, or an edge was dropped, rather than shipped looking confident.

---

## 1. What is observed

### 1.1 Connections are not requests — **inherent**

The agent counts TCP connection *establishments*, never requests. A service handling ten thousand
requests over one pooled connection reports **1**. A service opening a new connection per request
reports one per request. Two workloads under identical load can therefore differ by orders of
magnitude purely from client library configuration.

The metric is labelled `connections` in the API, the schema, the legend and the edge tooltip, and
edge thickness is explicitly described as "connection count — TCP establishments, not requests" in
the UI. It is never called traffic, load, or volume.

**Lifting it** requires L7 parsing, which means either payload inspection — forbidden by ADR-001 §6
— or application instrumentation, which the project exists to avoid.

### 1.2 Only IPv4 TCP — **inherent to the current filter**

The BPF program filters on `AF_INET` and `IPPROTO_TCP`. IPv6 traffic, UDP (including DNS), and any
other protocol are invisible. On the demo cluster IPv6 events are the single largest category
discarded in the kernel.

**Lifting it** means widening the filter and extending the event struct to carry 16-byte addresses;
the collector's layout already reserves the shape for it. UDP is a larger change — there is no
connection establishment to hook, so "an edge" would need defining differently.

### 1.3 Encrypted traffic is opaque — **by design**

The agent observes connection metadata only. TLS payloads are never decrypted, and nothing about
message content is recoverable from what is stored.

### 1.4 A dependency that did not communicate does not exist — **inherent**

This is runtime observation. A dependency that was idle throughout the selected window is absent
from the graph, and absent is indistinguishable from "never existed" without widening the window.
This is the direct cost of the property that makes the tool useful: everything shown actually
happened.

### 1.5 Byte volume is not reported — **measured, declined**

Per-connection byte counts are obtainable and **exact**, but only at connection close. Measured:

| workload | bytes transferred | bytes reported in-window |
|---|---:|---:|
| short-lived HTTP | — | ~99% of connections accounted |
| 8 persistent connections, 20 s | **32,342,016** | **~0** |

Kubernetes runs on persistent connections — database pools, gRPC channels, keep-alive — and those
are usually the *busiest* edges. A byte-weighted graph would therefore draw the heaviest edges as
the faintest, with no indication to the reader. The metric was withdrawn rather than shipped.

The API's `bytes_sent` / `bytes_received` fields remain in the schema and are **always absent**;
absent means "not measured", never zero. Full experiment, including the `bpf_iter/tcp` design that
would fix it, in [`evaluation/byte-accounting.md`](evaluation/byte-accounting.md).

---

## 2. Accuracy of the counts

### 2.1 A pod's first connections are lost — **measured**

An observation is only attributable once the agent's informer has seen the source pod's IP. A pod
that starts and immediately connects loses its opening seconds.

| connecting | opened | reported |
|---|---:|---:|
| immediately on start | 20 | **13** |
| after a 25 s settle | 20 | **20** |

This is inherent to resolving identity from the API server rather than from the kernel event, and
it is why `demo/demo-traffic.yaml` settles before opening its counted burst. Short-lived Jobs are
the workload most affected; long-running services lose only their first moments.

### 2.2 On kind, counts were inflated by the node count — **measured, fixed**

kind's "nodes" are containers sharing **one host kernel**, and the tracepoint fires for every
network namespace on it. Every agent therefore observed every connection cluster-wide, and each was
counted once per node — threefold on a three-node cluster. A burst of 100 connections reported 297.

Fixed by dropping events whose source pod is known to run on another node. The filter is
deliberately one-sided: an unresolvable IP, a host-network pod or a node-local process is kept,
because dropping what cannot be identified would trade a visible counting error for an invisible
missing edge. Drops are exposed as `topology_agent_events_filtered_foreign_node_total`.

**Note for anyone reading the Phase 1 evaluation:** its event counts predate this fix and are
inflated by roughly 3×. Its criteria did not depend on magnitude, so the gate stands.

### 2.3 Host-network traffic is excluded, including legitimate traffic — **measured, deliberate**

Every hostNetwork pod carries the node's address as its PodIP. On a control-plane node that means
etcd, kube-apiserver, kube-scheduler, kube-controller-manager, kube-proxy and the CNI agent are all
indexed under one address — as is the kubelet itself.

A source lookup on that address can only return an arbitrary one of them. Before this was
understood, the graph showed edges that are simply false (`etcd → coredns:8080` — etcd does not
call CoreDNS's health port; the kubelet does). Node addresses now resolve to `host` and are excluded
from the default graph.

The cost is real: a genuine outbound call *from* a host-network pod is also excluded, because it
cannot be distinguished from a kubelet probe. Declining to name a workload is preferred over naming
the wrong one.

### 2.4 Ambiguous Service attribution is preserved, not resolved — **by design**

Where several Services select the same pod and port, the destination resolves to the **workload**
with the candidate Service names carried as metadata, rather than picking one.

---

## 3. Deployment and environment

### 3.1 The agent is privileged — **inherent**

Loading a BPF program and attaching it to a tracepoint requires CAP_BPF and CAP_PERFMON
(CAP_SYS_ADMIN on older kernels). There is no unprivileged configuration that can do it.

The blast radius is bounded elsewhere: read-only Kubernetes RBAC (get/list/watch), no payload
capture, no database credentials, and no seccomp profile *only* because `RuntimeDefault` blocks the
very syscalls the agent exists to make. Every other workload runs non-root with a read-only root
filesystem, all capabilities dropped and seccomp applied; the chart asserts that exactly one
privileged container exists.

### 3.2 Linux 5.8+ with BTF — **inherent**

CO-RE requires `/sys/kernel/btf/vmlinux`. Verified on 6.8. There is no fallback for kernels without
BTF, and no Windows or macOS node support.

### 3.3 NetworkPolicies do nothing on the kind demo — **measured elsewhere**

kind's default CNI ignores NetworkPolicy entirely. The shipped policies were verified on a separate
kind cluster running **Calico**: every pod reached Ready with them enforced, the legitimate paths
worked, and an unlabelled pod was blocked from both the database and the ingest port.

They default to **on** in the chart and **off** in the kind demo values, because objects that do
nothing there would imply a protection that cluster does not provide.

### 3.4 Separate-kernel operation is argued, not measured — **argued**

No kind-based cluster can demonstrate that the node-scoping filter is a no-op where each node has
its own kernel. The argument is that it drops only pods *provably* on another node, and such events
are never observed on a real cluster — sound, and covered by unit tests, but an argument.

**Lifting it** needs two machines or VMs: `kubeadm init` on one, `join` on the other, then the
chart unchanged. The check is that `topology_agent_events_filtered_foreign_node_total` reads **zero**
on every agent, where on kind it reads in the hundreds.

### 3.5 Single backend replica — **deferred**

`values.schema.json` rejects `backend.replicaCount > 1`. Nothing in the ingest path forbids
horizontal scaling — the idempotency key makes duplicate delivery safe — but it has not been tested,
and a schema that permits an untested topology invites a failure nobody has seen.

### 3.6 The agent image is not distroless — **deliberate**

`debian:bookworm-slim` rather than distroless. The container is already privileged, so a shell adds
little exposure while making on-node troubleshooting materially easier.

---

## 4. Interface

### 4.1 The interface does not survive its own stated scale ceiling — **measured, open**

ADR-001 §6 states a ceiling of 500 nodes and 2,000 edges with no UI freeze beyond 100 ms. **The API
meets it comfortably; the interface does not meet it at all.**

Measured in a real browser against real ingested data:

| graph | time to first paint | main thread |
|---|---|---|
| 11 nodes / 6 edges | 1.06 s | 2 ms frame response |
| 102 nodes / 307 edges | 1.06 s | 2 ms frame response |
| 172 nodes / 1,002 edges | **> 250 s (timed out)** | — |
| 500 nodes / 1,908 edges | **> 379 s, page stopped responding** | — |

Up to roughly 300 edges the interface behaves as though the graph were empty. Past that it does not
degrade gradually — it stops.

**The honest ceiling is about 100 nodes and 300 edges.** For the demo cluster, which produces a
dozen nodes, this is invisible; for a real cluster of any size it is disqualifying, and it is the
single largest gap between what this system claims and what it does.

An earlier measurement in `frontend/tests/layout.test.ts` put a topology-changing poll at 208 ms and
treated that as the limitation. It was measuring the wrong thing: dagre is not the bottleneck. The
cost is React Flow rendering roughly 2,500 DOM elements, each edge carrying a text label — which no
unit test on the layout function could have exposed.

Tracked as `P5-F18`. Closing it means edge virtualisation, canvas rendering rather than DOM, or
refusing to render past a threshold and saying so — the API already returns a `truncated` flag the
interface could act on, which is the cheapest of the three.

### 4.2 Comparing unequal windows produces spurious CHANGED — **measured**

A five-minute window compared against a one-minute window will report almost everything as CHANGED,
because the counts are not normalised by duration. The UI enforces equal-length periods; the API
does not prohibit it, since a caller may legitimately want the raw comparison.

### 4.3 External destinations are summarised to a single node — **by design**

All non-cluster traffic collapses to `external:EXTERNAL`. Individual external IP addresses are never
persisted or returned (ADR-001 §6). The cost is that "which external service" is unanswerable —
accepted deliberately, because the alternative is a privacy-sensitive record of every address a
cluster contacted.

---

## 5. Not yet observed

Honest gaps rather than known-bad behaviour.

- **Retention has not been seen firing in-cluster.** The deletion path is covered by tests against
  both repository adapters, but no run has yet been left long enough for the retention task to
  delete a real bucket.
- **`kubectl` 1.31.1 against a 1.36.1 server** is outside the supported skew for the local tooling.
  It has caused no observed problem, but it is not a supported combination.
