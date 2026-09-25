# Kubernetes Runtime Topology Visualizer

Shows which workloads in a Kubernetes cluster **actually talk to each other**, by observing TCP
connection outcomes in the kernel with eBPF — not by reading manifests, and without modifying, instrumenting
or injecting anything into the applications being observed.

A manifest tells you what was declared. This tells you what happened.

```
Deployment/frontend  →  Deployment/backend   TCP:8080   (586 connections)
Deployment/backend   →  StatefulSet/redis    TCP:6379   (344)
Deployment/backend   →  EXTERNAL             TCP:80     (57)
Deployment/reporter  →  Deployment/payment   TCP:6380   (443)   ← nothing declared this
```

## Try it

```bash
make preflight     # kernel, BTF and inotify checks
make demo-up       # kind cluster + build + install + demo workloads  (~8 min)
make demo-traffic  # open exactly 100 connections
make demo-verify   # assert the graph matches, counts included
```

Then `kubectl -n topology port-forward svc/topology-visualizer-frontend 8080:8080`.

[`docs/demo-script.md`](docs/demo-script.md) is a fifteen-minute walkthrough.

## Explore the topology

**Live** shows the last few minutes and refreshes every 5 seconds; the strip under the header says
**LIVE**. **History** shows a fixed period in the past: pick a start and a length (up to the backend's
retention, 60 days by default), step through it with **Earlier** and **Later**, and return with
**Back to live**. History never refreshes on its own, is marked **HISTORY · NOT LIVE** in amber, and
the details panel, namespace list and graph all read the same period. The details panel separates
when a component was seen in that period from when it was seen across all stored history.

In Live and History views, choose **Namespaces** to collapse workloads into namespace nodes.
Click a namespace to expand its workloads; use **Collapse <namespace>** or **Collapse all** to
return to the overview. Graphs initially exceeding 400 edges start grouped; smaller graphs start
in **Workloads** view.

Select a workload on the graph or in the workload list, then choose **Focus selected workload**
to show its direct incoming and outgoing relationships. **Exit focus** returns to the previous
view. Grouped edges sum connection counts by direction, protocol, and destination port; self-loops
represent traffic within a collapsed namespace. External nodes stay separate. These views use only
the API's returned observation window and filters, and the display cap still applies after grouping
or focusing. Comparison mode retains its existing workload view.

## How it works

A privileged DaemonSet attaches a BPF program to `tracepoint/sock/inet_sock_set_state` and keeps
only *active opens* — `AF_INET`, TCP, and outcomes leaving `SYN_SENT`:
`ESTABLISHED` counts as success; `CLOSE` counts as failed/aborted setup. Accepted server sockets
are excluded. A bounded map records setup start times so successful events can carry measured
TCP establishment duration. Addresses are resolved to
workload identities in the agent, aggregated into ten-second batches, and delivered to a backend
that stores them in one-minute buckets.

[`docs/architecture.md`](docs/architecture.md) has the diagrams.

## Connection outcomes

Graph labels and node details separate successful establishments from **failed/aborted** setup
attempts. Failed-only relationships remain visible, with dashed warning edges. Measured successful
connections also show **mean TCP setup** time; namespace groups preserve weighted timing.

These are observed TCP outcomes, not HTTP failures or request latency. A pending attempt appears
only when it succeeds or closes. Refusals, timeouts and application cancellations share the
failed/aborted category; no specific cause is inferred. Failures before entering `SYN_SENT`
(such as an immediate routing error), IPv6 and DNS are outside capture. Historical failure data
is labeled unmeasured; zero successful timing samples do not imply zero latency. See
[measurement limitations](docs/limitations.md#17-connection-outcomes-and-setup-timing).

Upgrade the backend before agents: its forward migration adds outcome columns while keeping old
agent batches valid. Deploy the updated frontend to display the new fields. Comparison mode still
compares successful establishments only.

## What it does not do

Stated plainly, because several of these were measured and then deliberately not shipped:

- **Connections are not requests.** Ten thousand requests over one pooled connection count as **1**.
- **Byte volume is not reported.** Measured, found exact but readable only at connection close —
  8 persistent connections carried 32 MB and reported nothing — and *declined*, because a
  byte-weighted graph would draw the busiest edges as the faintest.
- **Large graphs need aggregation to be readable.** The canvas stays responsive at 2,000 edges, but
  graphs over 400 edges open grouped by namespace, with expansion and direct-neighbour focus. Past
  300 edges the layout trades crossing minimisation for speed, and a topology change at 2,000 edges
  blocks the page for about 0.7 s while the layout re-runs.
- **A dependency that did not communicate in the window does not exist.** That is the cost of the
  property that makes this useful.
- IPv4 TCP only. Encrypted payloads are opaque by design. Individual external IPs are never stored.

Full accounting with numbers in [`docs/limitations.md`](docs/limitations.md).

## Measured

| | measured | target |
|---|---|---|
| agent memory | **35 MiB** per node | < 256 MiB |
| capture throughput | **1,325 events/s**, zero kernel drops | 1,000/s |
| graph query p95 | **62 ms** at 500 nodes / 2,000 edges | < 500 ms |
| UI at that size | first paint **1.1 s**, clicks **< 100 ms**; layout on a topology change **~0.7 s** | no freeze > 100 ms: *met except layout* |

Reproduce with `make experiments`. Raw results and method in [`docs/evaluation/`](docs/evaluation/).

## Repository

| path | |
|---|---|
| `agent/` | Go + eBPF DaemonSet: capture, identity resolution, aggregation, delivery |
| `backend/` | FastAPI ingest, graph and diff API over PostgreSQL |
| `frontend/` | React topology UI |
| `charts/` | Helm chart |
| `contracts/` | Normative identity rules and the OpenAPI schema both sides are generated from |
| `demo/` | Uninstrumented demo workloads and the controlled change |
| `docs/adr/` | Architecture decisions, one per component |
| `docs/evaluation/` | Phase gates and experiment results |

## Documentation

- [Architecture](docs/architecture.md) — diagrams and why the boundaries sit where they do
- [Operator guide](docs/operator-guide.md) — installing and running it somewhere real
- [Troubleshooting](docs/troubleshooting.md) — symptoms, causes, and how to tell them apart
- [Limitations](docs/limitations.md) — what it cannot do, measured
- [Demo script](docs/demo-script.md) — the walkthrough

## Requirements

Linux 5.8+ with BTF (`/sys/kernel/btf/vmlinux`), Kubernetes 1.28+, Docker, and a privileged
DaemonSet. The agent is the only privileged component; everything else runs non-root with a
read-only root filesystem and no capabilities.
