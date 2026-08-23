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

---

## P5-K9 — validation under a CNI that enforces

**Status: PARTIAL — NetworkPolicy enforcement validated; separate-kernel validation not possible
on this host.** See "What is still unvalidated" below.

A full kubeadm cluster needs VMs the host cannot spare (9 GiB free). But the half of P5-K9 that was
genuinely unknown — do the shipped NetworkPolicies work? — does not need separate machines, only a
CNI that enforces. kind's default CNI ignores NetworkPolicy entirely, so the main demo cluster
could never answer it.

`kind/networkpolicy-cluster.yaml` creates a throwaway two-node cluster with `disableDefaultCNI` and
Calico v3.28.2.

### Result: the policies work, and they actually block

| check | result |
|---|---|
| every pod reaches Ready with policies enforced | **PASS** — kubelet probes are not dropped |
| agent → ingest | **PASS** — 9 connections observed through the policy |
| backend → database | **PASS** — `/health/ready` 200 |
| frontend → API | **PASS** |
| unlabelled pod → database (5432) | **BLOCKED** |
| unlabelled pod → ingest (8000) | **BLOCKED** |

The last two are the important ones: without them, everything above would pass equally well on a
CNI that was silently permitting everything.

The kubelet-probe risk that kept the default off did not materialise — probes originate from the
node address, but Calico does not drop them. `networkPolicy.enabled` now defaults to **true**. The
kind demo values keep it off, because objects that do nothing there would imply a protection that
cluster does not provide.

### The run also exposed a false-edge defect

Under Calico, with real control-plane components visible, the graph showed edges that are simply
not true:

```
Pod/etcd-...              -> Deployment/coredns:8080
Pod/kube-apiserver-...    -> DaemonSet/topology-visualizer-agent:8081
Pod/kube-scheduler-...    -> Deployment/coredns:8181
```

etcd does not call CoreDNS's health port. The kubelet does — and a kubelet probe originates from
the **node** address. Every hostNetwork pod carries that same address as its PodIP, so on a
control-plane node etcd, kube-apiserver, kube-scheduler, kube-controller-manager, kube-proxy and
the CNI agent are all indexed under one IP. `ResolveSource` consulted the pod cache *before*
checking whether the address was a node, so it returned an arbitrary one of them and named it as
the source.

`contracts/ids.md` documented rule 5 ("Node/host IP → host, excluded") for destinations but never
ordered the node case for sources. Both are fixed: the node check now comes first, and the source
ladder is written down.

Before and after, same cluster:

| | edges | false control-plane edges |
|---|---:|---:|
| before | 25 | ~12 |
| after, with real workloads | 4 | **0** |

```
Deployment/frontend                  -> Service/backend:8080                  (57)
Deployment/backend                   -> External/EXTERNAL:80                  (34)
Deployment/backend                   -> Service/redis:6379                    (33)
DaemonSet/topology-visualizer-agent  -> Service/topology-visualizer-backend    (9)
```

A digression worth recording: immediately after the fix the graph went completely empty, which
looked like a regression. It was not. With no application workloads on that cluster, the only
remaining traffic was control-plane chatter — now correctly classified as `host` and excluded — and
the agent opens a connection to the backend *only when it has a batch to send*. No edges meant no
ingest traffic, which meant no edges: a stable and entirely correct empty state. Deploying the demo
workloads produced the table above.

### What is still unvalidated

**Separate kernels.** kind's nodes share one host kernel, so no kind-based cluster can demonstrate
that `OriginatesElsewhere` is a no-op where each node has its own. The argument that it is safe
holds by construction — the filter drops only pods *provably* on another node, and on a real
cluster no such events are ever observed, so nothing is dropped — and the unit tests cover the
decision table. But it is an argument, not a measurement, and `docs/limitations.md` says so.

Reproducing it needs two machines or two VMs with ~2.5 GiB each: `kubeadm init` on one, `join` on
the other, any CNI, then `helm install` with the chart unchanged. The check is that
`topology_agent_events_filtered_foreign_node_total` reads **zero** on every agent, where on kind it
reads in the hundreds.

---

## P5-K10 — image pinning

**Status: DONE for pinning; scan recorded separately below.** `make verify-pinning` runs **16**
assertions and is wired into CI.

Everything was pinned by *tag*, which is mutable: `postgres:17-alpine` resolves to a different
image today than it did last month. A tag alone cannot make a build reproducible, and it makes a
scan result meaningless — what was scanned is not necessarily what will be pulled. All nine
third-party images are now pinned by digest as well as tag, the tag kept for readability:

| where | images |
|---|---|
| `agent/Dockerfile` | `golang:1.26-bookworm`, `debian:bookworm-slim` |
| `backend/Dockerfile` | `python:3.13-slim-bookworm` (×2 stages) |
| `frontend/Dockerfile` | `node:24-bookworm-slim`, `nginxinc/nginx-unprivileged:1.27-alpine` |
| `agent/build/Dockerfile.bpf-builder` | `golang:1.26-bookworm` |
| chart | `postgres:17-alpine` |
| demo manifests | `redis:7-alpine`, `nginx:1.27-alpine`, `busybox:1.36` |

Multi-arch manifest-list digests, not per-platform ones, so arm64 still resolves. The reference
prototype under `poc-kind-topology/` is deliberately excluded — ADR-001 §12 instruction 3 forbids
modifying it, and holding it to a standard nobody may satisfy would be a permanently failing check.

Verified by removing one digest: the check fails on exactly that line and exits non-zero. All three
images still build with the digests in place.

## P5-T17 — actionable failure messages

**Status: DONE.** Each scenario in ADR-001 §7 was triggered for real rather than read off the
source.

| scenario | how it was triggered | what the operator sees |
|---|---|---|
| denied BPF permissions | agent pod with `privileged: false` | `remove memlock rlimit (needs CAP_SYS_RESOURCE or a privileged container): operation not permitted` |
| missing BTF | covered by the same load path | `load BPF objects (check BTF at /sys/kernel/btf/vmlinux and that the container is privileged)` |
| not running as a pod | agent run under plain Docker | `in-cluster config (the agent must run as a pod with a ServiceAccount)` |
| missing configuration | agent with no env | `BACKEND_INGEST_URL is required` |
| backend outage | `scale deploy/backend --replicas=0` | agent logs the URL, attempt number and visible backoff (16s → 30s), batch ids retained |
| PostgreSQL failure | observed during the P5-K6 rollout | `could not connect to PostgreSQL at postgresql://topology:***@…` — credential redacted, readiness drops to 503 |
| empty graph | fresh window with no traffic | "No data", "None observed yet.", and a panel explaining how to generate traffic |

### One message was not actionable, and is now

During the backend-outage test the UI displayed:

> Showing the last successful reading. **Bad Gateway**

"Bad Gateway" is the raw status text from the frontend's nginx when the backend has no endpoints.
It is accurate and tells a user nothing they can do. The backend always supplies its own `detail`,
so this only affects failures that never reached it — precisely the case where a user has least
context. Now:

> Showing the last successful reading. **The backend is not responding — it may still be starting
> up, or its database may be unreachable**

Verified in the browser against a real zero-replica backend. Covered by `tests/client-errors.test.ts`,
including that the backend's own `detail` still wins where it exists — a generic mapping must never
override a specific explanation.

The banner also raises the first letter of the message, since API details are lowercase fragments
meant to be composed into a sentence and this one follows a full stop.

What the outage state gets right, and is worth keeping: the last good topology stays on screen with
an explicit "showing the last successful reading" rather than being replaced by an error, a Retry
control is offered, and no stack trace, DSN or credential appears anywhere.

---

## ADR-008 D-8.7 — image scan and triage

Trivy 0.6x via container (nothing installed on the host), HIGH and CRITICAL only.
Reproduce with `make scan-images`.

"Fixable" means a fixed version exists upstream. An unfixable finding cannot be acted on by this
project at all, so the fixable count is the one that carries a decision.

### Before and after

| image | fixable HIGH/CRITICAL before | after | what changed |
|---|---:|---:|---|
| `topology-agent:dev` | 6 HIGH | **0** | `golang.org/x/net` 0.49→0.56, `golang.org/x/text` 0.33→0.39 |
| `topology-backend:dev` | 2 HIGH | **0** | pip removed from the runtime image |
| `topology-frontend:dev` | 31 HIGH, 2 CRITICAL | **10 HIGH, 0 CRITICAL** | nginx base 1.27-alpine → 1.29-alpine |
| `postgres:17-alpine` | 21 HIGH, 1 CRITICAL | unchanged | accepted, see below |

**Both project images are now clean of fixable HIGH/CRITICAL findings.**

### The two fixes worth explaining

**The agent's findings were in our own dependency tree.** Five HIGH CVEs in `golang.org/x/net` and
one in `golang.org/x/text`, both indirect dependencies pulled in by client-go. Updated and
re-verified: build, full test suite and lint all clean.

**The backend's findings were in code that did not need to be in the image.** `msgpack` and
`setuptools`, reported against the Python section, turned out to live in
`/usr/local/lib/python3.13/site-packages/pip/_vendor/` — pip's *vendored* copies, inherited from
the base image. The application runs from `/opt/venv`, built by uv in the builder stage, and never
invokes pip at runtime. So the fix was deletion rather than upgrade: code that is not present
cannot be exploited. Verified the application still imports afterwards; `site-packages` now holds
only a README.

### What was accepted, and why

**`postgres:17-alpine` — 21 HIGH, 1 CRITICAL, accepted.** These are Alpine base packages for which
fixes exist upstream but which the postgres image maintainers have not yet rebuilt with; there is
no newer `17-alpine` digest to move to, and changing the database major version to chase a base
image would be a worse trade than the finding it fixes.

The exposure is bounded and worth stating precisely: this database exists for the self-contained
demo. It is never published outside the cluster, it now sits behind a NetworkPolicy that admits
only the backend, it runs non-root with all capabilities dropped, and ADR-005/ADR-007 support
pointing at an external managed database instead — which is what a real deployment would do.

**The frontend's remaining 10 HIGH.** Alpine 3.23.4 base packages in an image that serves static
files and proxies to the backend. The upgrade from 1.27-alpine removed both CRITICALs and two
thirds of the HIGHs; the remainder have no further upstream base to move to today.

### Note on the unfixable majority

Roughly 90 further HIGH/CRITICAL findings across the four images have no upstream fix. They are
recorded by the scan and deliberately not listed individually here: an entry nobody can act on adds
length without adding information, and `make scan-images` reproduces the full list on demand.

## P5-T18 — privacy check on committed images and text

**Status: PASS.** ADR-001 §6 forbids persisting or exposing individual external IPs, and §9 extends
that to anything published with the report.

**Five committed screenshots**, each opened and inspected rather than assumed:
`phase-2-ui.png`, `phase-3-compare.png`, `phase-3-compare-nodes.png`, `phase-4-a11y.png`,
`phase-5-outage-ui.png`.

None shows an IP address, hostname outside the cluster, credential, token or DSN. What they do show
is workload names, namespaces, ports and connection counts. The external node appears only as the
single aggregated `EXTERNAL` label — which is the privacy design visible in the artefact rather
than merely asserted in a document.

**Committed text** was scanned for DSNs with real passwords, inline credentials, AWS keys and
private key blocks. Two files matched, both deliberately:

- `Makefile` — `TEST_PG_DSN ?= postgresql://postgres:test@localhost:5433/topology`, a localhost
  fixture for the optional PostgreSQL test run, and `--set postgresql.auth.password=throwaway` in
  the image-pull check, which never persists data. The demo install generates a password at run
  time and stores it only in a cluster Secret.
- `backend/tests/test_api.py` — the string
  `postgresql://admin:super-secret-password@db/topology` is the *input* to
  `test_unhandled_error_hides_traceback_dsn_and_password_but_keeps_request_id`. It exists to prove
  that value never reaches a response. Its presence is evidence the protection is tested, not a
  leak.

No public-range IP address appears anywhere under `docs/`.

---

## P5-T12 / P5-T13 — performance experiments

**Status: DONE.** `make experiments` (or `bash scripts/experiments.sh memory|load|latency|churn`).
Every target in ADR-001 §6 is measured. Three are met with headroom. **One is missed by orders of
magnitude, and that is the most important result in this phase.**

### Results against the stated targets

| target (ADR-001 §6) | measured | verdict |
|---|---|---|
| agent memory < 256 MiB per node | **35 MiB** (worst of three nodes) | **MET** — 13% of budget |
| 1,000 capture events/s/node | **1,325/s, zero kernel samples lost** | **MET** |
| graph query p95 < 500 ms at 500 nodes / 2,000 edges | **p95 62 ms** (p50 41 ms) | **MET** — 8× headroom |
| no UI freeze over 100 ms | **unusable beyond ~300 edges** | **MISSED** |

### Agent memory — and a measurement that was wrong first

The first reading was **547 MiB**, which would have failed the target outright. It was wrong: the
DaemonSet sets `hostPID`, so reading `/sys/fs/cgroup/memory.current` inside the pod reports the
*node's* usage, not the agent's. The pod has a 256 MiB limit and had never been OOMKilled, which is
what made the number suspect.

Measured properly — the agent process's `VmRSS` read from the node — it is **34–35 MiB** on all
three nodes. `scripts/experiments.sh` reads it that way and says why in a comment, because the
convenient reading is the misleading one.

### Capture throughput

Four parallel load generators for 30 s produced **43,725 events on one node ≈ 1,325/s**, above the
1,000/s target, with `topology_agent_kernel_samples_lost_total` unchanged at **0**. The drop counter
is the one that matters: a lost sample is a connection that silently never appears in the graph, and
zero drops at above-target rate is the claim worth making.

### Query latency, measured at the size the target names

The demo cluster produces ~13 nodes. Measuring p95 there (6 ms) and calling a 500-node target met
would be measuring the wrong thing, so `scripts/seed-scale.py` ingests a synthetic 500-node /
2,000-edge graph **through the real ingest endpoint** — same validation, same transaction, same
storage path as an agent.

At that size, untruncated: **p50 41 ms, p95 62 ms, max 64 ms** over 40 requests. The target is 500 ms.

### The UI does not survive its own stated ceiling

This is the finding that matters, and it was only visible because the latency work put a real
500-node graph in front of the browser.

| graph | time to first paint | main thread |
|---|---|---|
| 11 nodes / 6 edges | 1.06 s | 2 ms frame response |
| 102 nodes / 307 edges | 1.06 s | 2 ms frame response |
| 172 nodes / 1,002 edges | **> 250 s (timed out)** | — |
| 500 nodes / 1,908 edges | **> 379 s, page stopped responding** | — |

Up to roughly 300 edges the interface is indistinguishable from empty — a tenth of a second of
work, frames answered in 2 ms. Past that it does not degrade, it **stops**: at 1,000 edges the page
never painted within four minutes, and at 1,908 it stopped answering `evaluate` at all.

ADR-006's invariant was already known to be missed for a topology-changing poll — measured at 208 ms
in `frontend/tests/layout.test.ts`. That test measures `layoutGraph` alone, and it was measuring the
wrong thing: dagre is not the bottleneck. The cost is React Flow rendering ~2,500 DOM elements, each
edge carrying a text label. A unit test on the layout function could never have found this, which is
the general lesson — the isolated measurement was fast and the real one is unusable.

**ADR-001 §6's UI scale target is rejected with evidence.** The honest ceiling is roughly **100
nodes and 300 edges**, not 500 and 2,000. Recorded in `docs/limitations.md` §4.1 and tracked as
`P5-F18`; closing it means edge virtualisation, canvas rendering instead of DOM, or refusing to
render past a threshold and saying so — the API already returns a `truncated` flag the UI could act
on, which is the cheapest of the three.

### Pod churn

`scripts/experiments.sh churn` restarts the demo backend and asserts that replacing every pod
creates no new Pod-level node ids. Identity is the workload, not the pod.

## An operational note: a backward clock jump broke the cluster

Midway through these experiments the whole stack went unhealthy — CoreDNS `0/1`, the backend in
CrashLoopBackOff on `gaierror`, agents and frontend not ready. The cause was not the code: the host
clock jumped **backwards about three hours**, visible as a log line at `01:59:40Z` followed by one
at `22:59:50Z`, and as `<invalid>` restart ages in `kubectl`.

ServiceAccount tokens are JWTs. A backward jump puts their `iat`/`nbf` claims in the future, so the
API server rejects them — CoreDNS logged `Unauthorized` on every watch, DNS stopped resolving, and
everything that needed to reach another pod failed. Recovery was to restart CoreDNS and then the
workloads, which reissues tokens against the corrected clock.

Worth recording because the symptom (`gaierror` connecting to PostgreSQL) points nowhere near the
cause, and because it is a plausible thing to hit on a laptop that suspends.

---

## P5-T16 — clean checkout

**Status: DONE.** The repository was cloned to a fresh directory with no virtualenv, no
`node_modules`, no build cache, and everything built and tested from committed instructions only.

| step | result |
|---|---|
| `go build ./...` + `go test ./...` | clean — no clang needed, because the bpf2go bindings and object are committed deliberately |
| `make venv` + `make test-python` | 139 passed, 21 skipped |
| `npm ci` + typecheck, lint, test, build | clean, 55 tests |
| `make lint-helm` | 40 assertions |
| `verify-image-pinning.sh` | 16 assertions |
| `verify-privacy.sh` | 9 checks |
| `docker build` for all three images | all build |

### It found three defects that only exist on a clean checkout

**The BPF builder Dockerfile had never been committed.** `.gitignore` carried a bare `build/` for
Python artefacts, which also matches `agent/build/` — so `Dockerfile.bpf-builder` was silently
excluded from every clone. A fresh checkout could not run `make generate` at all. The committed
bindings mean nobody normally needs to, which is exactly why this stayed invisible: it only breaks
for someone changing the BPF C source. The rule is now anchored to the paths it was meant for.

**`verify-privacy.sh` failed on itself.** Its explanatory comments contained literal example DSNs,
so the check flagged its own source. It passed locally only because `git ls-files` was reading the
pre-edit version — the failure appeared the moment the file was committed. Fixed by removing the
literal syntax rather than exempting the file: a checker that has to exempt itself is one that could
hide a real leak later. Re-verified that it still catches a planted credential.

**`make contracts-check` on a venv-less checkout printed `Error 127`.** That is the shell's "command
not found" and says nothing about what to do. Every target that shells into the virtualenv now
depends on a `require-venv` guard:

```
The backend virtualenv is missing: …/backend/.venv/bin/python
Create it with:  make venv
```

ADR-001 §7 asks for actionable failure messages; that applies to the build as much as to the runtime.

---

## P5-T19 — security review

Reviewed by hand across the whole branch. `/security-review` could not run because this session's
working directory is the ADR staging folder rather than the repository, so the areas below were
worked through explicitly.

### Clean

| area | finding |
|---|---|
| committed credentials | none — `make verify-privacy`, 9 checks, verified to catch a planted one |
| RBAC | `get`/`list`/`watch` only, on every resource. No wildcard; the chart asserts this |
| privileged containers | exactly one (the agent), asserted by count so a second cannot appear unnoticed |
| SQL injection | asyncpg with `$n` placeholders throughout; see below for the one dynamic query |
| input validation | `extra="forbid"` on every model; ports bounded 1–65535; batch id length and ULID grammar enforced |
| XSS | no `dangerouslySetInnerHTML`, `innerHTML`, `eval` or `new Function` anywhere in `src/` |
| CORS | `allow_credentials=False`; origins come from configuration and the schema rejects a wildcard |
| network exposure | ClusterIP only. No Ingress, LoadBalancer or NodePort in the default render |
| error responses | no stack trace, DSN or credential — tested by `test_unhandled_error_hides_traceback_dsn_and_password_but_keeps_request_id` |
| dependency CVEs | both project images carry **zero fixable** HIGH/CRITICAL findings |

### The one dynamic query, examined

`PostgresRepository` builds the graph query's `WHERE` clause dynamically. Only `len(params)` — an
integer — is interpolated, to produce placeholder numbers:

```python
conditions.append(f"(s.namespace = ANY(${len(params)}) OR t.namespace = ANY(${len(params)}))")
...
rows = await conn.fetch(sql, *params)
```

Every user-controlled value goes into `params` and is bound. **Not injectable.** One behavioural
note that is not a vulnerability: the `query` filter is bound as `%value%`, so a `%` or `_` typed by
a user acts as a LIKE wildcard.

### Finding: ingestion accepted an unbounded batch — fixed

`IngestBatch.edges` had no upper bound. Ingestion is unauthenticated **by design** — ADR-001 lists
authentication as out of scope and relies on a NetworkPolicy to limit who can reach the endpoint —
which makes the request body the only place a bound can be enforced. Without one, a single call
could pin the backend for as long as validation and insertion took.

Now `Field(max_length=10_000)`. A real agent batch carries edges in the single or double digits, so
the limit is far above anything legitimate while refusing an obviously abusive request. Two tests
cover it: an over-limit batch is refused with 422, and a normal batch still succeeds.

The contract test caught the resulting schema change immediately — `maxItems: 10000` — which is the
guard working as intended. Contract regenerated and all three consumers re-verified.

### Accepted, with the reasoning stated

**Ingestion is unauthenticated.** Any workload that can reach the ingest port could submit
fabricated topology. This is a documented scope boundary, not an oversight, and the mitigations are
real: a NetworkPolicy admitting only agent and frontend pods, no external exposure by default, and
a bounded request body. It is recorded in `limitations.md` so a reader is not left to infer it.

**The agent is privileged.** Unavoidable — loading a BPF program requires CAP_BPF and CAP_PERFMON.
Bounded by read-only RBAC, no payload capture, no database credentials, and an asserted count of
exactly one privileged container.

---

## P5-T20 — Phase 5 gate

**Verdict: PASSED, with one criterion partially met and recorded as such.**

Run on 2026-08-23 against a cluster **destroyed and rebuilt from nothing** for this gate, so nothing
below depends on accumulated state.

| # | Criterion (ADR-001 §7 Phase 5) | Result | Evidence |
|---|---|---|---|
| 1 | A clean machine runs `make demo-up` without hand-editing manifests | **PASS** | full teardown then `make demo-up` in **8m 01s**, exit 0, nothing edited |
| 2 | `demo-traffic` and `demo-change` produce the expected topology and diff | **PASS** | `demo-verify` **8/8**, including exact burst counts and the `reporter → payment` change |
| 3 | Deleting and recreating backend or database pods preserves history | **PASS** | both pods deleted; the finished burst edges stayed at exactly **100**, see below |
| 4 | The agent reports from every node, in kind **and** kubeadm | **PARTIAL** | kind: 3/3 nodes. kubeadm: not possible on this host — §3.4 of `limitations.md` |
| 5 | `demo-down` removes only resources this project created | **PASS** | a control namespace survived the label-scoped deletion, see below |
| 6 | Every ADR requirement maps to a test, demo step, or documented limitation | **PASS** | 73 verified, 3 documented limitations, 0 unresolved |
| 7 | Measured results support or explicitly reject each performance target | **PASS** | 3 met with headroom, 1 rejected with evidence |
| 8 | CI passes from a clean checkout; the demo runs from committed instructions | **PASS** | fresh clone built and tested; found 3 defects, all fixed |
| 9 | Failure modes have actionable messages | **PASS** | every scenario triggered live; one was not actionable and was fixed |
| 10 | Screenshots expose no secrets or individual external IPs | **PASS** | `make verify-privacy`, 5 images OCR'd |

### How criterion 3 was actually tested

Comparing totals before and after a restart proves nothing: live traffic continues, so a total that
grows is indistinguishable from one that double-counted. The test pins something that **cannot
legitimately grow** — the `demo-traffic` Job had already finished, so its edges are fixed:

```
before   demo-traffic → backend:8080 = 100      demo-traffic → redis:6379 = 100
         (delete BOTH the postgresql-0 pod and the backend pod)
after    demo-traffic → backend:8080 = 100      demo-traffic → redis:6379 = 100
```

Unchanged, while the overall total moved 502 → 736 from ongoing traffic. History survived both pods
being replaced, with no inflation.

### How criterion 5 was actually tested

A namespace the project did not create was added first, as a control:

```
kubectl create namespace not-ours
helm uninstall topology -n topology          → release removed
kubectl delete namespace -l topology-demo=true → deleted: data, demo
kubectl get ns not-ours                       → Active
```

The demo namespaces are selected by the label this project sets, never by bare name, so a
pre-existing `demo` namespace belonging to someone else is untouched. The kind cluster is then
deleted by name — correct, because this project created it.

### Criterion 4 is partial, and stays partial

kind's nodes share one host kernel, so no kind-based cluster can validate separate-kernel operation,
and a kubeadm pair needs VMs this host cannot spare. The NetworkPolicy half of that validation *was*
completed under Calico. The remainder is an argument supported by unit tests rather than a
measurement, and `limitations.md` §3.4 says so along with the exact reproduction steps.

Ticking this criterion would have been the easy thing and the wrong one.

### One observation from the fresh install

The backend restarted **5 times** during `make demo-up` before settling. It is not a failure —
`helm --wait` completed and the stack is healthy — but it is noisy. The cause is the documented
design decision that a backend which cannot reach its database at startup exits rather than starting
unready, racing PostgreSQL's own startup. Kubernetes' restart backoff resolves it. An init container
waiting for the database would remove the noise; it is recorded rather than changed, because altering
a documented decision at the gate is worse than noting its cost.

---

## P5-F18 — the UI scale limitation, mitigated

The gate recorded the interface as unusable past roughly 300 edges, with the API meeting its target
comfortably. Of the three ways to close that — edge virtualisation, canvas rendering, or honouring
a render budget — the third is the cheapest and was implemented.

`applyRenderBudget` caps the canvas at **400 edges**, chosen from the measurements rather than
guessed: 307 edges rendered comfortably, 1,002 never painted. It keeps the **busiest** edges, not an
arbitrary slice, because a subset chosen by sort order would look like a complete graph while hiding
whichever relationships happened to fall off the end. Nodes left with no remaining edge are dropped
too — a node is only in the graph because an edge put it there.

Measured on the same 500-node / 1,939-edge graph that previously hung:

| | before | after |
|---|---|---|
| time to first paint | **never** (>379 s, page stopped responding) | **1,198 ms** |
| frame response | unresponsive | **16 ms** |
| rendered | nothing | 400 edges, 388 nodes |

The banner states exactly what was dropped:

> Showing the 400 busiest of 1,939 edges and hiding 112 workloads. Drawing them all would stop the
> browser responding. Narrow by namespace, search for a workload, or shorten the window.

![Capped render with both banners](./phase-5-render-budget.png)

### A layout defect this exposed

Adding a second banner revealed that every `.banner` was `position: absolute` at the same `top`.
Only one had ever been shown at a time, so they had never collided; with two, the render-budget
banner drew straight over the backend's truncation notice and hid it. Banners now stack in a flex
column. The bug predated this change and would have surfaced the first time an error and a
truncation happened together.

### Why it stays open

At 400 edges the graph is **responsive but not readable** — a dense column of overlapping labels.
The cap converts a hung tab into a navigable interface, which is worth having and is what the gate
needed, but it is not the same as making a large topology legible. That still needs edge
virtualisation or canvas rendering, and `P5-F18` remains open to say so.
