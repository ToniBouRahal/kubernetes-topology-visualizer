# Phase 2 — End-to-End Pipeline Verification

- **Date:** 2026-08-17
- **Scope:** `eBPF → agent → delivery → backend → graph API` on a live three-node kind cluster
- **Status:** pipeline proven; Phase 2 gate not yet complete (frontend outstanding)

This is the first run where captured kernel events reach the product API. Phase 1 proved capture
and resolution; this proves delivery and serving.

## Observed topology, via `GET /api/v1/graph?window=5m`

```text
demo/Deployment:backend        -> data/Service:redis      TCP:6379  x60
demo/Deployment:frontend       -> demo/Service:backend    TCP:8080  x81
kube-system/DaemonSet:kindnet  -> EXTERNAL                TCP:443   x49
kube-system/DaemonSet:kube-proxy -> EXTERNAL              TCP:443   x50
topology/DaemonSet:...-agent   -> topology/Service:...-backend TCP:8000 x93

summary: 9 nodes, 5 edges, 333 connections, truncated=false
namespaces: [data, demo, kube-system, topology]
```

Nothing in the demo workloads is instrumented and no sidecar is injected. The Deployments,
Services, and DaemonSets above were resolved from raw IP:port observations alone.

## The system observes itself

The fifth edge is the one worth pausing on:

```text
topology/DaemonSet:topology-visualizer-agent -> topology/Service:topology-visualizer-backend TCP:8000
```

That is the agent's own delivery traffic, captured by the agent, resolved to canonical identities,
shipped to the backend, and served back through the graph API. The observer appears in its own
output.

It is a genuine end-to-end proof rather than a curiosity: it exercises every stage of the pipeline
using traffic the pipeline itself generates. It also demonstrates that the collector does not
special-case or exclude its own connections — which is honest, and is the behaviour a reader of
the graph should expect.

For the report this doubles as a limitation worth stating: **the tool contributes to the topology
it measures.** The agent→backend edge is real traffic and will always be present. It is not noise
to be hidden, but a reader should know why it is there.

## Backend counters

```text
topology_backend_batches_accepted_total      24
topology_backend_batches_deduplicated_total   0
topology_backend_batches_rejected_total       0
topology_backend_stored_edge_buckets         10
```

Zero rejected across 24 batches means the agent's Go structs and the backend's Pydantic models
agree on the wire in practice, not just in the round-trip test. Zero deduplicated is expected:
nothing failed, so nothing was retried.

## Deployment defect found and fixed

The backend crash-looped on first deployment, and every local test had passed:

```text
SettingsError: error parsing value for field "cors_allowed_origins"
from source "EnvSettingsSource"
```

`pydantic-settings` treats `list[str]` as a complex type and runs `json.loads` on the raw
environment variable **before any validator runs**, so the ConfigMap's comma-separated
`CORS_ALLOWED_ORIGINS` raised during settings construction and the process never started. A
`field_validator` does not help — it is never reached. The fix is
`Annotated[list[str], NoDecode]`.

The reason the suite missed it is worth recording: **every test used default settings and never
exercised parsing from the environment**, which is the only path a container takes.
`tests/test_settings.py` now covers that path for every chart-templated variable in ADR-001 §5.7.

## Observation: Deployment and Service are distinct nodes

`GET /api/v1/nodes/{backend Deployment}` returns:

```text
incoming: []
outgoing: [(redis, 6379, 60)]
```

The incoming edge is empty because `frontend` connects to `demo/Service:backend`, not to
`demo/Deployment:backend`. Both are separate nodes, which is correct: the destination ladder
resolves to the Service when one fronts the pod, while the source collapses to the owning
workload.

It is correct but will surprise a user who clicks "backend" expecting to see both directions.
Two consequences:

- The Phase 4 detail panel should relate a workload to the Services that front it, so selecting
  either shows the full picture (ADR-006 D-6.6).
- `docs/limitations.md` should state it plainly rather than leaving a reader to infer it.

Neither is a defect in the current contract, so no ADR change is required.

## Environment

kind v0.32.0, Kubernetes v1.36.1, three nodes · agent and backend images side-loaded ·
in-memory adapter (Phase 2 only; PostgreSQL arrives in Phase 3, at which point restart
persistence becomes testable).

## Outstanding for the Phase 2 gate

| Criterion | Status |
|---|---|
| Traffic observed by eBPF appears in the API | **met** |
| Duplicate `batch_id` counted once | met (T-4.2, unit) — not yet forced end to end |
| Backend outage causes retries, not agent termination | covered by T-2.10 — not yet forced against the live cluster |
| Graph shows direction, port, count, first/last seen | **met** |
| Filters and search deterministic | met at the API; UI outstanding |
| Traffic visible **in the browser** within 20 s | frontend not built (`P2-F1`) |
| Transient API failure retains the last graph | frontend not built |
| Automated smoke test asserts the demo edges | `P2-T5` |
