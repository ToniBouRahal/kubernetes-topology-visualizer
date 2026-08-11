# Canonical Identity, Edge Keys, and Contract Rules

**Normative.** This document defines identity for the whole system. It is implemented three times —
Go (`agent/`), Python (`backend/`), TypeScript (`frontend/`) — and nothing in any compiler notices
when those three drift apart. When this document and code disagree, this document is right and the
code is a bug.

- Governing ADR: [ADR-003](../docs/adr/ADR-003-ingestion-contract.md) D-3.2, D-3.3, D-3.5
- Parent: ADR-001 §5.2, §5.3
- Task IDs: `P0-C1`

---

## 1. Node identity grammar

Exactly two forms exist. There is no third.

```text
k8s:<cluster_id>:<namespace>:<kind>:<name>
external:EXTERNAL
```

### Field rules

| Field | Rule |
|---|---|
| `cluster_id` | From the `CLUSTER_ID` setting. **One value, templated into both agent and backend.** A mismatch produces two disjoint graphs that both look empty. |
| `namespace` | The Kubernetes namespace. Never empty for a `k8s:` ID. |
| `kind` | One of exactly six values (below). |
| `name` | The workload or Service name as reported by Kubernetes. |

### Allowed kinds — exactly six

```text
Service  Deployment  StatefulSet  DaemonSet  Job  Pod
```

`ReplicaSet` is **not** in this list. A pod owned by a ReplicaSet collapses to the ReplicaSet's own
owner, the Deployment. A pod with no recognised owner stays `Pod`.

Any other kind is rejected at the API boundary with `422`, and the database `CHECK` constraint
refuses it independently. Two layers, deliberately.

### The external node

```text
external:EXTERNAL
```

Exactly one per cluster. Every destination outside the cluster collapses into it.

**The remote IP is never part of identity and is never persisted.** Not as an ID, not as a label,
not in `attributes_json`. This is a privacy requirement (ADR-001 §6), not a storage optimisation.

### Worked examples

| Observation | Canonical ID |
|---|---|
| Pod `client-7d9f8b-x2jf` in `demo`, owned by ReplicaSet `client-7d9f8b`, owned by Deployment `client` | `k8s:kind-topology:demo:Deployment:client` |
| Its 3 replicas | the same single ID — replicas collapse |
| Service `backend` in `demo` | `k8s:kind-topology:demo:Service:backend` |
| StatefulSet `postgres` in `data` | `k8s:kind-topology:data:StatefulSet:postgres` |
| A bare pod `debug-shell` in `demo` with no owner | `k8s:kind-topology:demo:Pod:debug-shell` |
| Connection to `140.82.121.4:443` | `external:EXTERNAL` |
| Connection to an unresolvable `10.244.3.9` | *not* an ID — classified `unresolved`, counted, excluded by default |

---

## 2. IDs are opaque

Consumers **must not parse an ID** to recover any of its parts.

The API returns `namespace`, `name`, `kind`, and display `label` as **separate fields** alongside
the ID. Use those.

### Forbidden patterns

Both of these exist in the reference prototype and are exactly what this rule prohibits:

```javascript
// frontend/src/App.jsx:48 — WRONG
const ns = id.split('/')[0]
```

```python
# backend/main.py:97-98 — WRONG
parts = nid.split("/")
label = parts[-1] if len(parts) > 1 else nid
```

Correct:

```typescript
const ns = node.namespace   // a field, not a substring
```

### Why

Opacity is what lets identity change shape later without breaking every consumer. It also prevents
a whole class of silent bugs: a workload named `foo:bar` breaks every naive `split(':')`, and it
breaks it *quietly*, producing a plausible-looking wrong namespace.

---

## 3. Identity stability

| Event | Effect on identity |
|---|---|
| Pod deleted and rescheduled under the same Deployment | **No change.** Same ID. |
| Deployment scaled 1 → 5 | **No change.** One ID, one node. |
| Pod IP reused by a different workload | Resolved through the informer cache at observation time; no stale identity. |
| Workload renamed | **New identity.** The old node ages out of the retention window. |
| Namespace renamed | **New identity.** Same reason. |

"Pod churn does not fragment workload identity" is a Definition-of-Done item (ADR-001 §9) and is
tested by T-8.6. It is the clearest single demonstration that runtime resolution works, because it
is precisely what a manifest-derived tool cannot do.

---

## 4. The edge key

```text
(cluster_id, source_id, target_id, protocol, destination_port)
```

This exact tuple is used in **four** places, and all four must be provably identical:

| # | Place | Component |
|---|---|---|
| 1 | In-memory aggregation key | `agent/internal/aggregate` |
| 2 | Primary key suffix of `edge_buckets` | `backend/migrations` |
| 3 | Grouping key for graph queries | `backend/app/domain` |
| 4 | Join key for diff | `backend/app/domain` |

Test `T-3.4` asserts this equality directly. If these diverge, the graph splits one logical edge
into several — and it looks like a collection bug, not a key bug, which is why it is worth a
dedicated test.

Note what is **not** in the key: source port (ephemeral), PID (best-effort, softirq-unreliable),
node name (an edge is cluster-scoped, not node-scoped), and timestamps.

---

## 5. Direction

Edges are directed, client → server. Only active opens are recorded: the kernel transition
`TCP_SYN_SENT → TCP_ESTABLISHED`.

An accepted server socket also reaches `ESTABLISHED`, from `TCP_SYN_RECV`. Recording that would
produce a **false reverse edge**. The BPF program filters on both `oldstate` and `newstate` for this
reason (ADR-002 D-2.1). This is the single most important correctness rule in the collector.

---

## 6. Endpoint resolution

Source and destination resolve by **different** rules. They are not symmetric.

### Source

```text
source pod IP → Pod → follow ownerReferences → collapse Pod→ReplicaSet→Deployment
              → k8s:<cluster>:<ns>:<Kind>:<name>
```

A source **never** resolves to a Service. Services are destinations; a Service does not originate a
connection.

### Destination — first match wins

| # | Condition | Result |
|---|---|---|
| 1 | IP is a ClusterIP in the Service cache | that Service |
| 2 | IP is in EndpointSlice(s) **and** the observed port matches a declared Service target port | that Service |
| 3 | Multiple Services match | the destination **workload**, with candidate Service names as metadata |
| 4 | Pod IP, no Service match | the owning workload |
| 5 | Node/host IP | `host` metadata, excluded from the default graph |
| 6 | Routable non-cluster IP | `external:EXTERNAL` |
| 7 | Private/cluster IP, unresolved | `unresolved`, counter incremented |

Rule 3 is mandatory: **never pick a Service arbitrarily.** Preserving ambiguity is more honest than
fabricating certainty, and the UI can show it.

Rule 7 exists so a CNI timing race is not silently reported as internet traffic. Unresolved and
external are different states and must stay different.

---

## 7. Batch envelope

```json
{
  "schema_version": 1,
  "cluster_id": "kind-topology",
  "agent_id": "topology-agent/kind-worker",
  "batch_id": "01J8ZQ9X7K4M2N6P8R3T5V7W9Y",
  "observed_at": "2026-08-10T12:00:00Z",
  "interval_seconds": 10,
  "edges": [
    {
      "source": {
        "id": "k8s:kind-topology:demo:Deployment:client",
        "kind": "Deployment", "namespace": "demo", "name": "client"
      },
      "target": {
        "id": "k8s:kind-topology:demo:Service:backend",
        "kind": "Service", "namespace": "demo", "name": "backend"
      },
      "protocol": "TCP",
      "destination_port": 8080,
      "connection_count": 30,
      "first_seen": "2026-08-10T11:59:51Z",
      "last_seen": "2026-08-10T12:00:00Z"
    }
  ]
}
```

| Field | Rule |
|---|---|
| `schema_version` | Currently `1`. Unsupported → **400** (not 422 — it is a version problem, not a shape problem). |
| `batch_id` | ULID. Unique per agent batch. **This is the idempotency key.** |
| `observed_at`, `first_seen`, `last_seen` | RFC 3339, UTC, timezone-aware. A naive datetime is rejected. |
| `connection_count` | Integer ≥ 1. Zero is meaningless — an edge with no connections is not an edge. |
| `bytes_sent`, `bytes_received` | Optional, integer ≥ 0. **Absent until the Phase 4 gate passes.** Absent ≠ zero. |
| `protocol` | `TCP` only in this release. |
| `destination_port` | 1–65535. |
| edge count | Bounded per batch. |
| body size | Bounded before parsing. |

---

## 8. Status codes

| Code | Meaning |
|---|---|
| `202` | New batch accepted |
| `200` | `batch_id` already ingested — **no state change, and not an error** |
| `400` | Unsupported `schema_version` |
| `422` | Malformed batch: invalid enum, bad timestamp, out-of-range value |
| `413` | Body over the configured limit |
| `503` | Storage unavailable — the agent should retry |

**The agent treats `200` exactly like `202`:** success, drop the batch from the queue. Treating it
as an error causes an infinite retry of a batch that is already stored.

---

## 9. Determinism

Every list in every response has an **explicit sort key**:

```text
(source_id, target_id, protocol, destination_port)
```

Never iterate a Python `set`, a Go `map`, or an unordered SQL result into a response. The prototype
does exactly this at `backend/main.py:78`, and the consequence is that the graph reorders between
identical requests — which breaks layout stability in the UI and makes snapshot tests useless.

Truncation is applied **after** filtering and sorting, so it is reproducible.

---

## 10. Zero is not null

Two distinct cases, routinely confused:

| Case | Meaning |
|---|---|
| Diff: edge missing from a period | **Zero.** It was measured; there was no traffic. |
| Bytes: field absent | **Not measured.** Byte accounting has not passed its feasibility gate. |

A percentage delta against a zero baseline is **undefined**. Emit the reason string. Never emit
`Infinity` or `NaN` — both break JSON serialisation and both render as garbage in the UI.

---

## 11. Changing this contract

1. Edit the Pydantic model in `backend/app/domain/`
2. `make contracts` — regenerates `contracts/openapi.json`
3. Regenerate the TypeScript client
4. Update the Go structs in `agent/`
5. Add or update a fixture in `contracts/examples/`
6. Run all three test suites

**Never hand-edit** `contracts/openapi.json` or `frontend/src/api/generated/`. CI fails on drift
between the committed contract and what FastAPI generates (test T-3.6).

Any breaking change requires `/api/v2` and a new ADR. Never a silent edit to v1.
