---
name: topology-contract
description: >
  The canonical ingestion, graph, and diff contract for the topology visualizer. Load before
  editing any batch/graph/diff model, node ID construction, edge key, query parameter, or
  status-code mapping — in Go (agent/), Python (backend/), or TypeScript (frontend/).
---

# Topology contract

Normative source: `contracts/ids.md`. ADR: `docs/adr/ADR-003-ingestion-contract.md`.

This contract is expressed **three times** — Go structs, Pydantic models, generated TypeScript —
and no compiler notices when they drift. That is the single highest-probability failure mode in
this project, and it stays invisible until the demo. This skill exists to prevent it.

## 1. Node identity — exactly two forms

```text
k8s:<cluster_id>:<namespace>:<kind>:<name>
external:EXTERNAL
```

Allowed kinds, exactly six:

```text
Service  Deployment  StatefulSet  DaemonSet  Job  Pod
```

`ReplicaSet` is **not** allowed. A pod owned by a ReplicaSet collapses to that ReplicaSet's own
owner, the Deployment. A pod with no recognised owner stays `Pod`.

`cluster_id` must be identical in the agent and the backend. It comes from one Helm value
templated into both. A mismatch produces two disjoint graphs that both look empty, with no error
anywhere.

Never persist or return an individual external IP — not as an ID, not as a label, not in
`attributes_json`. Privacy requirement, ADR-001 §6.

## 2. IDs are opaque — never parse them

The API returns `namespace`, `name`, `kind`, and `label` as separate fields. Use those.

```javascript
const ns = id.split('/')[0]          // WRONG — prototype App.jsx:48
const ns = node.namespace            // right
```

```python
label = nid.split("/")[-1]           # WRONG — prototype main.py:97
label = node.label                   # right
```

A workload named `foo:bar` breaks every naive split, and it breaks it *quietly*.

## 3. The edge key

```text
(cluster_id, source_id, target_id, protocol, destination_port)
```

Identical in four places, asserted by test T-3.4:

1. agent in-memory aggregation key
2. `edge_buckets` primary key suffix
3. graph query grouping key
4. diff join key

Not in the key: source port (ephemeral), PID (unreliable in softirq context), node name (an edge
is cluster-scoped).

## 4. Status codes

| Code | Meaning |
|---|---|
| `202` | new batch accepted |
| `200` | already ingested — no state change, **not an error** |
| `400` | unsupported `schema_version` (a retry can never help) |
| `422` | malformed batch |
| `413` | body or edge count over limit |
| `503` | storage unavailable — retry |

The agent treats `200` exactly like `202`: success, drop the batch. Treating it as failure causes
an infinite retry of something already stored.

Version checks live in the **route**, not the model — a `Literal[1]` would make FastAPI answer 422
where the contract requires 400.

## 5. Determinism

Every list in every response sorts by `(source_id, target_id, protocol, destination_port)`.

Never iterate a Python `set`, a Go `map`, or an unordered SQL result into a response. The prototype
does this at `backend/main.py:78`; the graph then reorders between identical requests, which breaks
UI layout stability and makes snapshot tests meaningless.

Truncate **after** filtering and sorting, so truncation is reproducible.

## 6. Zero is not null

| Case | Meaning |
|---|---|
| diff: edge absent from a period | **zero** — measured, no traffic |
| bytes: field absent | **not measured** — Phase 4 gate has not passed |

Percentage delta against a zero baseline is **undefined**: emit the reason string. Never `Infinity`
or `NaN` — both break JSON and render as garbage.

In Go, use `*int64` with `omitempty` for byte fields. A plain `int64` cannot distinguish absent
from zero.

## 7. Timestamps

RFC 3339, UTC, timezone-aware everywhere. A naive datetime is a `422`. Window boundaries are
half-open and lower-inclusive: `[from, to)` — in graph queries, diff periods, and retention alike.

## 8. Changing the contract — the full procedure

1. Edit the Pydantic model in `backend/app/domain/models.py`
2. `make contracts` — regenerates `contracts/openapi.json`
3. Regenerate the TypeScript client
4. Update the Go structs in `agent/internal/contract/`
5. Add or update a fixture in `contracts/examples/` **and** its `manifest.json` entry
6. Run all three suites: `make test`

**Never hand-edit** `contracts/openapi.json` or `frontend/src/api/generated/`. CI fails on drift
(test T-3.6).

Breaking changes require `/api/v2` and a new ADR — never a silent edit to v1.

## 9. Before you finish

- [ ] Does any consumer parse an ID? (must be no)
- [ ] Does every new list have an explicit sort key?
- [ ] Does a new optional numeric field distinguish absent from zero?
- [ ] Is there a fixture for each new validation rule, in `manifest.json`?
- [ ] Do the Go structs still round-trip `batch.valid.json`? (`cd agent && go test ./...`)
- [ ] Does `make contracts-check` pass?
