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

The successful connection counter counts TCP *establishments*, never requests. A separate counter records failed/aborted setup outcomes. A service handling ten thousand
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

### 1.7 Connection outcomes and setup timing

The collector observes IPv4 TCP active-open `SYN_SENT → ESTABLISHED` and `SYN_SENT → CLOSE`
transitions. The latter means setup failed or was aborted, including application cancellation.
It does not identify refusal versus timeout versus cancellation. Immediate failures before
`SYN_SENT`, pending attempts, DNS and application/HTTP errors are not captured. A closed socket
that had already established is not counted as a setup failure. Failure-only relationships can
therefore appear despite carrying zero successful connections.

Setup timing runs from entering `SYN_SENT` to reaching `ESTABLISHED`, including local TCP setup
work, retransmissions, and any deferred-connect time. It is not request latency or pure network RTT.
The start-time map has a 65,536-entry LRU bound. Attachment boundaries, evictions and failed map
updates can omit samples; counts still survive terminal transitions. `connect_latency_count` states
how many successes were timed, and mean milliseconds is `connect_latency_sum_us / count / 1000`.
Sub-microsecond measurements round down to a known zero; zero samples mean unmeasured.

Old buckets have no failure measurement. When old and new observations are combined, failure
counts describe only the measured contributions; they do not imply complete failure coverage or
support a total failure percentage. The graph's display budget still ranks by successful counts,
so it may omit failed-only relationships; narrow filters or use grouping/focus to inspect them.
Comparison mode retains successful-connection semantics and excludes failure-only observations.

Real-kernel collector tests exercise successes, local refusal, and cancellation of a pending
connection, as well as accepted-socket exclusion and absence of extra events on connection reuse.
These establish behavior, not a new throughput or UI performance benchmark. The transition timing
and pre-SYN_SENT boundary follow the [Linux IPv4 TCP connect implementation](https://github.com/torvalds/linux/blob/v6.8/net/ipv4/tcp_ipv4.c)
and the [socket state tracepoint](https://github.com/torvalds/linux/blob/v6.8/include/trace/events/sock.h).

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

### 2.4 Which Service carried the traffic is not reported — **by design, with a cost**

A destination resolves to the **workload** that serves it, not to the Service in front of it
(ADR-009). A Service is a ClusterIP and a set of routing rules with no process behind it, and
resolving to it split every dependency chain in two: a source resolved to `Deployment:backend`
while a destination resolved to `Service:backend`, so `frontend → backend → redis` could never
connect at a shared node.

The cost is that the route is no longer visible. In a cluster where several Services point at one
workload, the graph shows the dependency but not which Service was used. Carrying it would need a
new field in the batch envelope, the API schema, the generated clients and a database column;
ADR-009 D-9.4 records that as a deliberate omission, not an oversight.

Ambiguity in the other direction is still preserved rather than guessed: where a Service's
endpoints span several **distinct** workloads, the destination stays the Service. A fan-out is
real, and naming one of its arms would invent a dependency that was never observed.

---

## 3. Deployment and environment

### 3.1 The agent holds two capabilities — **measured, minimal**

Until ADR-014 the agent ran `privileged: true` with `hostPID`. It now runs with **only `CAP_BPF` and
`CAP_PERFMON`**, all other capabilities dropped, no host namespace, a read-only root filesystem,
the runtime's default seccomp profile, and one host path, `/sys/kernel/tracing`, mounted read-only.
Measured on the kind cluster (kernel 6.8): the program loads and attaches, and the counted burst
still reports exactly 100 of 100. `CAP_BPF` loads the program and its ring buffer; `CAP_PERFMON`
attaches it to the tracepoint and lets it read kernel socket state. Nothing else was needed.

What remains: those two capabilities are powerful — `CAP_PERFMON` lets a process read kernel
memory through BPF — so the agent is still the most trusted workload, and because it needs them
plus a host path, even the `baseline` Pod Security Standard rejects it (§6.5). `privileged: true`
is kept as a chart value only for kernels without `CAP_BPF` (before 5.8), outside the supported
range. The chart asserts that no container is privileged by default.

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

### 3.6 The agent image is not distroless — **deliberate, worth revisiting**

`debian:bookworm-slim` rather than distroless, for on-node troubleshooting. The original argument
was that a privileged container gains little from dropping its shell. With the agent now down to
two capabilities (§3.1), a shell is a larger share of what an attacker who reached the container
would have, and a distroless image is the natural next step.

---

## 4. Interface

### 4.1 The interface at its stated scale ceiling: **measured, largely met**

ADR-001 §6 sets a ceiling of 500 nodes and 2,000 edges, with no UI freeze over 100 ms. The API
meets it (query p95 62 ms). The interface now meets it for paint, clicks, zoom and polling, with one
exception, stated below.

Measured with `frontend/bench/` (production build, 1280x720, synthetic graph shaped like
`seed-scale.py`). Full method and raw results are in
[`evaluation/p5-f18-canvas-scale.md`](evaluation/p5-f18-canvas-scale.md).

| 500 nodes / 2,000 edges | measured |
|---|---|
| first paint, grouped by namespace (the default at this size) | 654 ms |
| first paint, every edge drawn | 1,065 ms |
| click a component, worst main-thread task | 86–95 ms (up to 107 ms while counts change every poll) |
| zoom, idle polls | no task over 50 ms |
| switch to the per-workload view | **729 ms, once** |

**This corrects Phase 5.** Phase 5 found that 1,000 edges never painted and blamed React Flow's
DOM. The real cause was Dagre. On the dense workload-to-Service shape that real resolution
produces, its default ranking and crossing minimisation took 24 s at 800 edges and did not finish
at 1,000. The layout-only unit test had used a random graph of a different shape, which Dagre lays
out quickly. Past 300 edges the layout now uses a cheaper configuration (2,000 edges in about
0.8 s). Selection, polling and labels were also reworked so a click or a poll no longer redraws
every element.

**What remains:**

- **Layout runs on the main thread.** Opening the per-workload view at 2,000 edges blocks for about
  0.73 s once. A poll that adds or removes a workload pays the same again. Polls that only change
  counts skip layout. Lifting this means moving layout into a Web Worker.
- **Past 300 edges, the layout skips crossing minimisation**, so large graphs have more crossings.
  Smaller graphs, including the demo, lay out exactly as before.
- **Edges carry no text.** The port, counts, failures and setup timing for each link are in the
  details panel when a component is selected. Each edge's accessible name still states both ends
  and the port.
- **Responsive is not readable.** 2,000 edges on one canvas is still dense. Graphs over 400 edges
  therefore open grouped by namespace, and the per-workload view is one click away.
- The canvas still caps at **2,000 edges**, the largest size measured. That matches the backend's
  default `GRAPH_MAX_EDGES`, so it only triggers if an operator raises that setting. When it does,
  the banner states what was left out, keeping the busiest edges.

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

### 4.4 Ingestion is authenticated by mutual TLS — **closed by ADR-014**

This entry used to say that any workload reaching the ingest port could submit fabricated topology.
It no longer can: batches are accepted only on a listener that requires a client certificate from
the ingest-client CA, which signs the agents' certificate and nothing else. Measured from an
unlabelled pod in the namespace (`make verify-tls`): no certificate, no handshake.

What remains: all agents share **one** client certificate, so the backend can tell an agent from
anything else but not one agent from another — a batch's `agent_id` is still what the batch says.
Per-node certificates would need issuing at pod start (cert-manager's CSI driver, for example),
which is outside what the chart can generate. And anyone who can read Secrets in the namespace
can take that certificate, which is why Secret read access there is the real boundary.

---

### 4.5 The Grafana "Logs" link matches pods by name prefix — **by design, stated**

When `frontend.grafana.url` is set (ADR-012), the *Logs* button opens Loki on
`{namespace="…", pod=~"<workload>-.*"}`, and the shipped workload dashboard (ADR-013) selects pods
the same way. A workload named `backend` therefore also matches the pods of a sibling named
`backend-worker`. Matching on a pod label would be exact, but which label a log
shipper attaches is the operator's configuration, not something this tool can know; the prefix
works with any shipper. Standalone pods are matched exactly. A `Service` node — which ADR-009 only
emits when the workload behind it is unknown or ambiguous — gets no link at all, because there is
nothing correct to link to.

---

## 5. Not yet observed

Honest gaps rather than known-bad behaviour.

- **Retention has not been seen firing in-cluster.** The deletion path is covered by tests against
  both repository adapters, but no run has yet been left long enough for the retention task to
  delete a real bucket.
- **`kubectl` 1.31.1 against a 1.36.1 server** is outside the supported skew for the local tooling.
  It has caused no observed problem, but it is not a supported combination.

---

## 6. Security (ADR-014)

What the hardening does not do, measured where it could be.

### 6.1 Certificates are rotated by hand — **by design**

The chart generates every certificate (three CAs, five server and client certificates) and keeps
them across upgrades. They are valid for 365 days and nothing renews them: rotation is deleting the
certificate Secrets and running `helm upgrade`, after which every pod rolls onto the new set
(operator guide). The CA private keys are never stored, so rotation always replaces the whole set.
Automatic renewal is available by supplying the Secrets from cert-manager (`tls.generate: false`).

### 6.2 The bundled Dex also listens on plain HTTP — **stated, mitigated**

The Dex chart this depends on always passes `--web-http-addr` and offers no switch to remove it,
so port 5556 is open inside the pod alongside the HTTPS port every client uses. A NetworkPolicy
admits only the frontend pods, and only on HTTPS — which protects nothing under a CNI that does not
enforce NetworkPolicy, kind's default among them (§3.3). The bundled Dex is a demo convenience; a
real installation points `auth.oidc` at its own identity provider.

### 6.3 Dex's sign-in page trips the Content-Security-Policy — **observed, harmless**

Dex's login page carries one inline script (it focuses the username field), which the policy
`script-src 'self'` blocks, and the browser logs a violation. The form works without it. Loosening
the policy for one convenience script on a demo page would weaken it everywhere else.

### 6.4 Images are scanned but not signed — **declined**

CI scans every image with Trivy and keeps a CycloneDX SBOM of each (`make scan-images`). They are not
signed: this project never publishes images to a registry — kind side-loads them — so there is no
pulled artefact a signature would protect. Signing belongs with a release pipeline that publishes.

### 6.5 The namespace cannot enforce a Pod Security Standard — **measured**

A dry run against the live pods: every workload except the agent — backend, frontend and its
oauth2-proxy, PostgreSQL, Dex — already meets **`restricted`**. The agent fails even `baseline`, for
exactly the two things §3.1 measured as necessary: non-default capabilities and a host path. So the
namespace enforces only `privileged` and warns and audits at `restricted` (`make pod-security`),
where the agent's warnings are the only ones expected. Running the agent in its own namespace would
let this one enforce `restricted`; the chart installs into one namespace and does not do that.

### 6.6 Development mode serves plain HTTP — **deliberate, loud**

The backend with no TLS settings, and the agent with an `http://` URL and no certificate, run as
before ADR-014 so unit tests and local development keep working. Both log a warning at start-up
saying so; half a TLS configuration refuses to start. The chart always configures TLS.

### 6.7 Who signed in is logged — **deliberate**

The backend's request log records the signed-in user's e-mail for every API read, from a header
nginx sets and the backend trusts only on the listener nginx alone can reach. That is an audit
trail of who looked at the topology; it is also personal data in the backend's logs, retained as
long as those logs are.


### 6.8 One binary is excluded from the image-scan gate — **argued, bounded**

The official `postgres:17-alpine` image ships `gosu`, built with Go 1.24.6, and every HIGH and
CRITICAL finding left in that image is in its Go runtime. The image's entrypoint runs `gosu` only
when started as root (`if [ "$(id -u)" = '0' ]`), to drop to the `postgres` user; this chart starts
PostgreSQL as uid 999 with `runAsNonRoot`, so the binary is never executed. `make scan-images`
therefore skips that one file when gating — the SBOM still lists it — and says why in the script.
The exclusion is only sound while the database runs non-root, which `verify-chart.sh` asserts.
Lifting it means an upstream image rebuilt with a current Go, or building our own without `gosu`.
