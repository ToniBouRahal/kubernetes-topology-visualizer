# Phase 1 Gate Record

- **Date:** 2026-08-17
- **Phase:** 1 — Feasibility: eBPF capture and Kubernetes resolution (ADR-001 §7)
- **Verdict:** **PASSED** — all seven acceptance criteria demonstrated on a live three-node cluster
- **Commits:** `a68d4ba`, `2219371`, `f1d96bb`
- **Recorded per:** ADR-001 §12 instruction 9 · ADR-008 D-8.5

## Environment

| | |
|---|---|
| Cluster | kind v0.32.0, three nodes (1 control-plane, 2 workers) |
| Kubernetes | v1.36.1 (`kindest/node:v1.36.1`) |
| Node kernel | 6.8.0-136-generic, shared with the host — BTF available |
| Container runtime | containerd 2.3.1 |
| Agent image | `topology-agent:dev`, 113 MB, side-loaded with `kind load` |
| Go / client-go | 1.26.5 / v0.36.3 |

## Acceptance criteria

| # | Criterion (ADR-001 §7 Phase 1) | Result | Evidence |
|---|---|---|---|
| 1 | Real unmodified demo workloads produce captured IPv4 TCP events | **PASS** | 7,634 active opens across three agents; observed topology below |
| 2 | `Frontend → Backend` and `Backend → Redis` become service-level edges | **PASS** | Both present, resolved to `Service` destinations |
| 3 | Multiple replicas collapse to one logical node and edge | **PASS** | 2 frontend and 2 backend replicas, split across both workers, produce ONE edge each |
| 4 | External traffic becomes `source → EXTERNAL`, never one node per IP | **PASS** | Two `→ EXTERNAL` edges; no per-IP nodes anywhere |
| 5 | Accepted server sockets create no false reverse edges | **PASS** | No edge originates from `Service:backend` or `Service:redis` |
| 6 | No payload bytes in BPF maps, Go structs, logs, or output | **PASS** | T-2.7 structural assertion; emitted batches carry only L3/L4 metadata |
| 7 | The agent runs on every node of the three-node cluster | **PASS** | T-7.5, control-plane included |

## Observed topology

`SINCE=4m bash scripts/show-edges.sh` — 42 batches:

```text
demo/Deployment:backend           ->  data/Service:redis     TCP:6379   connections=63
demo/Deployment:frontend          ->  demo/Service:backend   TCP:8080   connections=90
kube-system/DaemonSet:kindnet     ->  EXTERNAL               TCP:443    connections=43
kube-system/DaemonSet:kube-proxy  ->  EXTERNAL               TCP:443    connections=56
```

Four things in that output are the whole point of the phase:

1. **Sources are workloads, destinations are Services.** `frontend` resolved through
   `Pod → ReplicaSet → Deployment`; `backend` was reached through its Service by matching the
   observed port against EndpointSlices. The asymmetric ladder works against real cluster data.
2. **Replicas collapsed.** frontend and backend each run two pods on two *different* nodes
   (`10.244.1.3`/`10.244.2.5` and `10.244.1.4`/`10.244.2.4`). The graph shows one node and one
   edge per relationship, with counts summed across nodes — the property a manifest-derived tool
   cannot demonstrate.
3. **`kindnet` and `kube-proxy` resolved to DaemonSets**, so owner resolution works for
   workloads nobody wrote for this project.
4. **What is absent matters most.** There is no `redis → backend` and no `backend → frontend`.
   Both of those receive connections continuously. A `newstate`-only filter would have produced
   them as false reverse edges. Their absence is the `oldstate == TCP_SYN_SENT` condition working
   in production.

## Per-agent metrics

| Agent | Raw events | Samples lost | Unresolved | Edges flushed |
|---|---|---|---|---|
| `agent-4ntg2` | 2,496 | **0** | 1,470 | 38 |
| `agent-dgtcx` | 2,544 | **0** | 1,494 | 35 |
| `agent-ggz9k` | 2,594 | **0** | 1,524 | 39 |

An earlier sample from one agent recorded 571 submitted events against **3,162 filtered
transitions**. That ratio is the four-condition filter rejecting five state changes for every one
it keeps — which is what it should do, since most TCP state transitions are not active opens.

Zero ring-buffer drops at this rate. The counter exists so that a drop would be *visible*; the
Phase 5 load experiment is where it will be pushed deliberately.

## Observation: the unresolved ratio

Roughly 59% of observations resolve to `unresolved` and are excluded from the graph. This is
correct behaviour, not a defect — but it is worth recording.

The dominant contributor is loopback traffic. Node-local components connect to `127.0.0.1`
constantly, and loopback is neither a pod address nor a routable one, so rule 7 classifies it as
unresolved rather than external. That is exactly the intended precedence: the alternative would be
reporting a node's internal plumbing as internet traffic.

Two consequences to carry forward:

- The `endpoints_unresolved` metric is not a useful health signal on its own at this ratio.
  Phase 2 should either exclude loopback before it reaches the counter, or split the counter by
  reason so a genuine resolution failure is distinguishable from routine loopback.
- The application graph itself is unaffected — none of this traffic reaches it.

## Commands run

```bash
make generate                      # bpf2go inside the pinned builder container
make verify                        # lint + test + contract drift
docker build -t topology-agent:dev agent/
kind create cluster --name topology --config kind/cluster.yaml
kind load docker-image topology-agent:dev --name topology
make agent-deploy                  # helm upgrade --install, agent only
make agent-verify                  # T-7.5
kubectl --context kind-topology apply -f demo/demo-workloads.yaml
SINCE=4m bash scripts/show-edges.sh
```

## Tests

98 Go assertions across four packages, all hermetic:

| Package | Covers |
|---|---|
| `internal/collector` | T-2.1 layout, T-2.2 byte order, T-2.3/T-2.4 source guards, T-2.7 no payload |
| `internal/resolver` | T-2.5 owner collapse, T-2.6 the seven-rule ladder incl. ambiguity and unresolved-vs-external |
| `internal/aggregate` | T-2.8 infrastructure ports, T-2.9 aggregation, determinism, batch validity |
| `internal/contract` | T-3.3 cross-language round trip |

## Privileged eBPF tests — RUN AND PASSED

Executed by the project owner on 2026-08-17 (they require an interactive sudo password, so they
cannot run unattended):

```text
$ make test-ebpf
cd agent && sudo -E go test ./... -tags=privileged -count=1 -run 'Privileged'
ok  github.com/fyp/kubernetes-topology-visualizer/agent/internal/collector  9.797s
```

The ~9.8 s runtime matches the suite's timed capture windows. Covers:

| Test | Property |
|---|---|
| `TestPrivilegedCapturesRealConnection` | T-2.11 — a real connection is captured from an uninstrumented process |
| `TestPrivilegedProducesNoFalseReverseEdge` | Exactly one event per connection, zero originating from the listening port |
| `TestPrivilegedReusedConnectionEmitsNoFurtherEvents` | Ten round trips over one connection emit one event — connections are not requests |
| `TestPrivilegedStatsAreReadable` | T-2.12 — kernel counters advance; the transition filter is demonstrably active |
| `TestPrivilegedEventsCarryNoPayload` | No event carries payload content, checked against a live program writing a known marker |

**The false-reverse-edge property is now established three independent ways:** a source-level
guard that runs in ordinary CI, a runtime assertion against a real attached program on the host
kernel, and the absence of reverse edges in the live three-node cluster. A regression would have
to defeat all three.

## Deviations

| # | Deviation | Rationale |
|---|---|---|
| 1 | Pod informer is cluster-wide, not node-scoped as ADR-002 D-2.3 suggests | Destination lookups routinely cross nodes — confirmed here, where frontend on one worker reaches backend pods on the other. Node-scoping would break them. A second node-scoped informer would double pod memory for no behavioural gain. |
| 2 | `nodes` added to the watched set and to RBAC (a tenth resource type) | Host classification needs every node's addresses, not just the local one. Read-only, few objects. |
| 3 | Agent runtime is `debian:bookworm-slim`, not distroless | The container is already privileged; keeping a shell aids on-node troubleshooting and removes little. Recorded for `docs/limitations.md`. |
| 4 | Generated bpf2go bindings and the `.o` are committed | Generation needs clang + libbpf, which only the builder container has. Committing keeps `go build`, `go test`, and CI working on a plain machine. |

None contradicts an ADR decision, so no new ADR is required (ADR-001 §12 instruction 6).

## Known issues carried into Phase 2

1. **kubectl 1.31.1 against a 1.36.1 server** — a five-minor skew, beyond the supported ±1. All
   operations used here worked, but kubectl should be aligned to 1.36 before Phase 5.
2. **Slow network** — the `kindest/node` image took roughly an hour to pull. Phase 5's
   "clean supported machine" timing claims should account for image pull time separately from
   install time.
3. **Codex delegation was misconfigured** — the plugin's task runner defaults to a read-only
   sandbox, so delegated write tasks silently produced nothing. Fixed by passing `--write` and
   dropping `--background`; recorded here because two Phase 1 delegations were lost to it.

## Next

Phase 2 — end-to-end live product. First task `P2-B1`: FastAPI layering and the app factory. The
agent's `emitBatch` log sink is replaced by the bounded, retrying delivery queue in `P2-A17`.
