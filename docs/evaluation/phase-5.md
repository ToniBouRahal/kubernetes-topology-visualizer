# Phase 5 gate — packaging, validation, and handoff

**Task:** P5-T20 (in progress — this file is built up as each item lands)
**Cluster:** `kind-topology`, 3 nodes, kernel 6.8.0-136-generic

---

## P5-K6 — Complete chart: probes, limits, security contexts, NetworkPolicies

**Status: DONE.** `make lint-helm` now runs **40** assertions (was 30).

### Security posture, as deployed

Verified against the running pods, not just the rendered chart:

| workload | runAsNonRoot | user:group | read-only rootfs | caps | seccomp |
|---|---|---|---|---|---|
| backend | yes | 10001:10001 | yes | drop ALL | RuntimeDefault |
| frontend | yes | 10001:10001 | yes | drop ALL | RuntimeDefault |
| postgresql | yes | 999:999 | **no** | drop ALL | RuntimeDefault |
| agent | **no — privileged** | root | n/a | n/a | **none** |

Three deliberate exceptions, each asserted in `verify-chart.sh` so they cannot drift:

- **The agent is privileged.** Loading a BPF program and attaching it to a tracepoint requires
  CAP_BPF and CAP_PERFMON; there is no unprivileged configuration that can do it. The blast radius
  is bounded elsewhere: get/list/watch RBAC only, no payload capture, no database credentials. The
  chart asserts there is *exactly one* privileged container, so a second cannot appear unnoticed.
- **The agent has no seccomp profile.** `RuntimeDefault` restricts `bpf()` and `perf_event_open()`,
  the two syscalls the agent exists to make. Applying it would break capture at start-up, so the
  chart asserts its *absence* to stop someone "fixing" this later.
- **PostgreSQL has a writable root filesystem.** It writes its socket to `/var/run/postgresql` and
  its configuration into PGDATA at initdb time. Everything else is dropped.

Fixed along the way: the frontend was running with `gid=0` because only `runAsUser` was set.
Harmless given no capabilities and a read-only rootfs, but not a sentence worth defending in a
review. Now `runAsGroup: 10001`.

### NetworkPolicies

D-7.4 requires two restrictions. Only one existed — ingest limited to agent and frontend pods. The
second, **PostgreSQL reachable only from the backend**, was missing entirely and has been added:
without it any pod in the namespace could read the full observed topology on 5432, which is the
most sensitive artefact this system produces.

They remain **off by default**, deliberately. kind's CNI ignores NetworkPolicy, so enabling them
here would prove nothing while claiming protection nobody has observed. The specific untested risk
is that kubelet health probes originate from the *node* address, not a pod, so an ingress policy
written purely as podSelectors can drop them and leave every pod permanently un-ready — behaviour
that varies by CNI. `P5-K9` (kubeadm multi-node) is where this gets tested, and the default flips
to `true` once it passes.

## T-7.6 — Readiness fails while the database is down, and recovers

Tested against the live cluster by scaling the StatefulSet to zero and back.

```
baseline                              /health/ready=200

kubectl scale statefulset ... --replicas=0
t+5s    /health/ready=503   pod.ready=true      # endpoint responds immediately
t+10s   /health/ready=503   pod.ready=false     # kubelet acts; endpoint.ready=false

kubectl scale statefulset ... --replicas=1
t+5s    /health/ready=503   pod.ready=false   restarts=2
t+10s   /health/ready=200   pod.ready=true    restarts=2
```

**PASS.** Readiness drops within 5s, the pod is removed from the Service endpoints, and both
recover within 10s of the database returning.

The detail that matters: **`restarts` never changed.** A running backend degrades and recovers
rather than crash-looping through a database outage. History was intact afterwards — 10 nodes,
5 edges, 20,178 connections.

A backend that starts *while* the database is unreachable does exit rather than start unready, and
that asymmetry is deliberate: an instance that has never connected cannot know whether its
migrations have run, and serving an empty graph from an unmigrated database would be worse than
failing loudly. Observed during this phase's rollout, where the backend restarted twice with
`ConnectionError: could not connect to PostgreSQL at postgresql://topology:***@...` — note the
credential is redacted, which is the ADR-001 §6 requirement working.

---

## P5-K7 / P5-K8 — the demo loop, and the counting defect it exposed

**Status: DONE.** `make demo-up · demo-traffic · demo-change · demo-verify · demo-down`, plus
`make images`. `demo-verify` runs **8** assertions through the API.

Building a demo that asserts a *number* rather than a screenshot turned up three real defects.

### 1. Connection counts were inflated ~3x on kind

ADR-002 states the agent "sees only active opens originating on its own node". Nothing enforced it,
and on kind it was false: the "nodes" are containers sharing **one host kernel**, and
`inet_sock_set_state` fires for every network namespace on that kernel. Every agent observed every
connection cluster-wide.

The proof was unambiguous — `topology-control-plane` runs none of the demo workloads, yet its agent
reported the identical complete edge set as both workers:

```
topology-worker            edges=5
topology-worker2           edges=5
topology-control-plane     edges=5     <- runs none of these workloads
```

A counted burst of 100 connections was reported as 297.

Fixed by `Resolver.OriginatesElsewhere`, which drops an event whose source IP belongs to a pod
running on another node. The check is one-sided on purpose: unresolvable IPs, host-network pods and
node-local processes are all kept, because dropping the unidentifiable would trade a visible
counting error for an invisible missing edge. Drops are exposed as
`topology_agent_events_filtered_foreign_node_total` — zero on any cluster with per-node kernels,
non-zero on kind, where it measured 304 of 1,136 raw events on one agent.

After the fix the agents report *different* edge sets, as they should.

### 2. Cold-start under-counting, found while verifying the fix

With the inflation gone, a 20-connection burst reported 13. The cause is not the fix: a pod's first
connections are unresolvable until the agent's informer has seen its IP, so a Job that starts and
immediately connects loses its opening seconds.

| scenario | opened | reported |
|---|---:|---:|
| before the node-scope fix | 100 | 297 |
| after the fix, connecting immediately | 20 | 13 |
| after the fix, 25s settle first | 20 | **20** |
| `make demo-traffic` (settles, both edges) | 100 each | **100 each** |

This is inherent to resolving identity from the API server rather than a defect, and it is why
`demo/demo-traffic.yaml` settles before opening its burst. `demo-verify` now asserts the exact
number, which is a far stronger claim than "traffic appears".

### 3. The `backend -> EXTERNAL` edge had never once worked

`demo-workloads.yaml` documented this edge and used `(echo > /dev/tcp/example.com/80)` to produce
it. `/dev/tcp` is a **bash** feature and the container runs BusyBox `ash`, so with `|| true`
swallowing the failure the line opened no connection at all. Over 24 hours of running, the only
EXTERNAL edges came from `kindnet` and `kube-proxy`.

The manifest's comment blamed an offline cluster — "the edge simply does not appear, it is not an
error" — which made a real bug look expected. Replaced with `nc`; the edge appeared within 45
seconds and `demo-verify` now reports it.

### 4. The agent's advertised metrics port refused connections

The chart declared `containerPort: 9090` and set `AGENT_METRICS_PORT`, but only one HTTP listener
was ever started, on the health port. Anything scraping the advertised port got connection refused.
A second listener now serves the same mux, so `/metrics` works on both.

### `demo-verify` output

```
== T-7.10: the expected demo edges are present ==
  PASS frontend -> backend TCP:8080 (872)
  PASS backend  -> redis   TCP:6379 (475)
  NOTE external edge: present
== T-7.11: replicas collapse and identity holds ==
  PASS no duplicate (kind, name, namespace) — replicas collapsed to one node each
  PASS no ReplicaSet nodes (owner-reference walk reached the workload)
== the counted burst is reported exactly ==
  PASS demo-traffic -> redis:   opened 100, reported 100
  PASS demo-traffic -> backend: opened 100, reported 100
== the controlled change is visible ==
  PASS reporter -> payment TCP:6380 (660)

demo verification: 8 passed, 0 failed
```

### `demo-down` is surgical

D-7.6 requires teardown that never deletes something it did not create. Nothing in it takes a
wildcard: the release is removed by name in this project's namespace, demo namespaces are selected
by the `topology-demo=true` label this project sets rather than by bare name — so a pre-existing
`demo` namespace belonging to someone else is untouched — and the kind cluster is deleted by name,
only if it exists.
