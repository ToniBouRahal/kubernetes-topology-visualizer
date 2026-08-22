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
