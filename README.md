# Kubernetes Runtime Topology Visualizer

Shows which workloads in a Kubernetes cluster **actually talk to each other**, by observing TCP
connections in the kernel with eBPF — not by reading manifests, and without modifying, instrumenting
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

## How it works

A privileged DaemonSet attaches a BPF program to `tracepoint/sock/inet_sock_set_state` and keeps
only *active opens* — `AF_INET`, TCP, and the `SYN_SENT → ESTABLISHED` transition. That last
condition is what stops a server from appearing to call its own clients. Addresses are resolved to
workload identities in the agent, aggregated into ten-second batches, and delivered to a backend
that stores them in one-minute buckets.

[`docs/architecture.md`](docs/architecture.md) has the diagrams.

## What it does not do

Stated plainly, because several of these were measured and then deliberately not shipped:

- **Connections are not requests.** Ten thousand requests over one pooled connection count as **1**.
- **Byte volume is not reported.** Measured, found exact but readable only at connection close —
  8 persistent connections carried 32 MB and reported nothing — and *declined*, because a
  byte-weighted graph would draw the busiest edges as the faintest.
- **The interface stops past roughly 300 edges.** The API handles 500 nodes / 2,000 edges at 62 ms
  p95; the supplied UI does not render it. Measured, not fixed.
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
| UI at that size | **unusable past ~300 edges** | *rejected with evidence* |

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
