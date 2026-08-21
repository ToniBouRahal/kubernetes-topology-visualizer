# ADR-003: Ingestion and API Contract — The Cross-Language Spine

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §5.2 (identity), §5.3 (contract) · Source of truth §10
- **Component path:** `contracts/`
- **Owning phases:** Phase 0 (definition), Phase 2 (ingest + graph), Phase 3 (diff)
- **Consumers:** ADR-002 (Go producer), ADR-004/005 (Python), ADR-006 (TypeScript)

## 1. Why this is its own component

The batch schema, the canonical node ID grammar, and the edge key are expressed **three times** — as
Go structs, Pydantic models, and generated TypeScript. Nothing in the compiler or the test runner
notices when they drift. A one-character change to the ID grammar in the agent silently produces a
duplicate graph node in PostgreSQL that no test asserts against.

This is the highest-probability failure mode in the project, and it is invisible until the demo.
Giving the contract its own directory, its own ADR, and its own skill is the mitigation.

The prototype demonstrates the failure in miniature: the agent sends `{src, dst, port, protocol,
connections}` (`agent/main.go:41-47`) and the backend re-declares the same shape by hand
(`backend/main.py:26-31`). They agree only because one person wrote both in one sitting.

## 2. Decisions

### D-3.1 — OpenAPI is the single source of truth

FastAPI generates `contracts/openapi.json`. It is committed. The TypeScript client is generated from
it; Go structs are hand-written but validated against the committed example fixtures in CI. No
component may hand-declare a payload type that exists in the contract (ADR-001 §5.6).

### D-3.2 — Canonical node identity

```text
k8s:<cluster_id>:<namespace>:<kind>:<name>
external:EXTERNAL
```

- `kind` ∈ {`Service`, `Deployment`, `StatefulSet`, `DaemonSet`, `Job`, `Pod`}. No other value is
  accepted; the API rejects unknown kinds rather than storing them.
- IDs are **opaque**. Consumers must never parse an ID to derive a namespace or a label — the API
  returns `namespace`, `name`, and display `label` as separate fields (ADR-001 §5.2).
  The prototype's frontend does exactly this at `frontend/src/App.jsx:48` (`id.split('/')[0]`) and
  `backend/main.py:97-98`. Both patterns are forbidden.
- Exactly one external node exists per cluster: `external:EXTERNAL`. Remote IPs are never part of
  identity and are never persisted (ADR-001 §6).
- Renaming a workload creates a new identity. Pod replacement under the same workload does not.

### D-3.3 — Edge key

```text
(cluster_id, source_id, target_id, protocol, destination_port)
```

This exact tuple is the aggregation key in the agent, the primary-key suffix in `edge_buckets`, the
grouping key in graph queries, and the join key in diff. All four must be provably the same tuple —
T-3.4 asserts it.

### D-3.4 — Batch envelope

As specified in ADR-001 §5.3. Additional rules the schema must enforce:

| Field | Rule |
|---|---|
| `schema_version` | integer, currently `1`; unsupported value → **400** |
| `batch_id` | ULID, unique per agent batch, the idempotency key |
| `observed_at`, `first_seen`, `last_seen` | RFC 3339, UTC, timezone-aware; naive datetimes rejected |
| `connection_count` | integer ≥ 1 |
| `bytes_sent`, `bytes_received` | optional, integer ≥ 0, **absent** until the Phase 4 gate passes — absent and zero are different |
| `protocol` | `TCP` only in this release |
| `destination_port` | 1–65535 |
| edge count per batch | bounded; oversize → **413** or **422**, consistently and documented |
| request body size | bounded before parsing |

### D-3.5 — Status codes

| Code | Meaning |
|---|---|
| `202` | new batch accepted |
| `200` | `batch_id` already ingested — no state change, not an error |
| `400` | unsupported `schema_version` |
| `422` | malformed batch, invalid enum, bad timestamp |
| `413` | body over the configured limit |
| `503` | storage unavailable (agent retries) |

The `200`/`202` split is what makes retries safe and observable. An agent receiving `200` must treat
it as success and drop the batch (ADR-002 D-2.6.3).

### D-3.6 — Query contract

`GET /api/v1/graph` and `GET /api/v1/diff` per ADR-001 §5.3. Enforced invariants:

- exactly one of `window` or (`from`,`to`) — supplying both is `422`;
- `from < to`; span ≤ configured maximum; inverted or overlapping ranges rejected;
- `include_external` default `true`, `include_unresolved` default `false`;
- responses always carry `generated_at`, the **effective** filters (echoing defaults), nodes, edges,
  summary counters, and a truncation indicator;
- ordering is deterministic and explicit — sort by `(source_id, target_id, protocol,
  destination_port)`. Never rely on dict, set, or database iteration order. The prototype's
  `nodes_set` at `backend/main.py:78` is a Python `set`; its iteration order will reorder the graph
  between runs and break both layout stability (ADR-006) and snapshot tests.

### D-3.7 — Diff classification

For each logical edge across baseline and current periods:

| Class | Condition |
|---|---|
| `NEW` | present in current only |
| `REMOVED` | present in baseline only |
| `CHANGED` | present in both and \|Δ%\| ≥ `TOPOLOGY_DIFF_CHANGE_THRESHOLD_PERCENT` on connection count, or on byte volume when available |
| unchanged | returned only when `include_unchanged=true` |

A missing period is **zero, not null**. Every result carries baseline value, current value, absolute
delta, percentage delta when defined (undefined when baseline is 0 — report the reason, not
`Infinity`), and the classification reason string. Determinism is a hard requirement: same inputs,
same output, including at exact bucket boundaries.

### D-3.8 — Versioning

All product routes live under `/api/v1`. `/health/live`, `/health/ready`, and `/metrics` sit outside
the version prefix. Any breaking change requires `/api/v2` and a new ADR — never a silent edit to
v1 (ADR-001 §13 defers this).

### D-3.9 — Fixtures

`contracts/examples/` holds valid and invalid batches, graph responses, and diff responses. Every
rule above owns at least one invalid fixture that must be rejected. These fixtures are shared by Go
tests, Python tests, and frontend mocks — one corpus, three consumers.

## 3. Implementation guide

```text
contracts/
  openapi.json                 generated from FastAPI, committed, diffed in CI
  examples/
    batch.valid.json
    batch.invalid-*.json       one per rule in D-3.4
    graph.response.json
    diff.response.json
  ids.md                       the grammar in prose, with worked examples
```

Order of work in Phase 0:

1. Write `ids.md` — grammar, the six allowed kinds, the external ID, the edge key, opacity rule.
2. Define Pydantic models in `backend/app/domain/` (ADR-004 owns the file, this ADR owns the shape).
3. Export `openapi.json` via a `make contracts` target.
4. Write fixtures; wire `make test` to validate every fixture against the schema.
5. Generate the TypeScript client into `frontend/src/api/generated/` — never edited by hand.
6. Write the Go structs and a Go test that unmarshals `batch.valid.json` and re-marshals it to an
   equivalent document. This is the cheapest possible drift detector across the language boundary.

## 4. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`topology-contract`** (custom — spec below) | Any edit to a model, schema, ID construction, or query parameter in **any** of the three languages | This is the skill that exists specifically to prevent the drift described in §1. It is the single highest-value custom skill in the project. |
| **`skill-creator`** | Phase 0, once | Authors `topology-contract` and the other four custom skills. `/plugin install skill-creator@claude-plugins-official` |
| **`hookify`** | Phase 0, once | Installs the `contracts/openapi.json` → regenerate-TypeScript hook. A hook enforces this even when no skill is loaded; that difference matters over six phases. |
| **`context7`** | Writing Pydantic v2 validators or configuring the TS generator | Pydantic v1→v2 validator syntax and OpenAPI generator flags are exactly the kind of detail worth reading current docs for rather than recalling. |
| **`adr-guard`** (custom) | Any proposed field addition | New wire fields are scope changes. `bytes_*` is already specified as nullable-until-Phase-4; anything else needs an ADR. |

### Situational

- **`pyright-lsp`** / **`typescript-lsp`** / **`gopls-lsp`** — the contract is edited in all three
  languages; whichever is active applies.
- **`/code-review`** on the Phase 0 contract commit. A contract defect found in Phase 0 costs
  minutes; the same defect found in Phase 3 costs a migration.

### Do not use

`dataviz`, `frontend-design`, `artifact-*` — this component has no visual surface. Do not let a
"graph" ADR pull in charting guidance; these are API shapes.

### `topology-contract` skill specification

```yaml
name: topology-contract
description: >
  The canonical ingestion, graph, and diff contract for the topology visualizer. Load before
  editing any batch/graph/diff model, node ID construction, edge key, query parameter, or
  status-code mapping — in Go (agent/), Python (backend/), or TypeScript (frontend/).
```

Body must contain:

1. **The ID grammar** and the six allowed kinds, verbatim from D-3.2.
2. **The opacity rule** with both prototype anti-patterns quoted as counterexamples
   (`id.split('/')[0]`).
3. **The edge key tuple** and the four places it must be identical.
4. **The status-code table** from D-3.5, with the note that `200` means success for the agent.
5. **The determinism rule**: explicit sort keys everywhere; never iterate a `set` or `map` into a
   response.
6. **Zero ≠ null**: for diff, a missing period is zero; for bytes, absent means "not measured".
7. **The change procedure**: edit the Pydantic model → `make contracts` → regenerate TS → update Go
   structs → add or update a fixture → run all three test suites. Never edit `openapi.json` or
   `frontend/src/api/generated/` by hand.

## 5. Test requirements

| ID | Test | Owner |
|---|---|---|
| T-3.1 | Every fixture in `contracts/examples/` validates (or fails) as its filename declares | backend |
| T-3.2 | Each rule in D-3.4 has an invalid fixture producing the correct status code | backend |
| T-3.3 | Go round-trip of `batch.valid.json` is field-equivalent | agent |
| T-3.4 | The edge key tuple is identical in agent aggregation, DB primary key, and graph grouping | cross-cutting |
| T-3.5 | Generated TS types compile against `graph.response.json` and `diff.response.json` | frontend |
| T-3.6 | `openapi.json` in the working tree matches what FastAPI generates (CI fails on drift) | CI |
| T-3.7 | Unsupported `schema_version` → 400, malformed → 422, both distinctly | backend |
| T-3.8 | Ordering is stable across repeated identical queries | backend |

T-3.6 is the mechanical drift detector. Without it, the committed contract slowly becomes fiction.

## 6. Acceptance criteria

Phase 0: contract examples validate against the schema; canonical IDs, the `EXTERNAL` ID, and
edge-key rules exist in prose **and** in tests.

Phase 2: posting the same `batch_id` twice changes counts only once.

Phase 3: diff classification is deterministic at exact bucket boundaries, and every classification
carries its calculation.

## 7. Consequences

- **Generated code is committed.** `openapi.json` and the TS client live in the repo so CI can diff
  them. Regeneration is a build step, not an editing step.
- **Three languages, one truth, some duplication.** The Go structs remain hand-written; T-3.3 is the
  accepted mitigation rather than adding a Go code-generation toolchain the ADR does not call for.
- **Opaque IDs cost a little ergonomics.** Debugging is slightly harder than reading
  `namespace/name`, and the API must always return labels alongside IDs. The payoff is that
  workload renames and pod churn cannot corrupt identity.
- **Strictness surfaces as `422`s during bring-up.** That is the intended behaviour — a rejected
  batch with a clear reason is strictly better than a silently mis-shaped edge in the graph.

## 8. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). `[ ]` open · `[~]` in progress · `[x]` done ·
`[!]` blocked · `[-]` dropped with reason.

### Phase 0 — definition

- [x] **P0-S2** Author the `topology-contract` skill — §4
- [x] **P0-C1** `contracts/ids.md`: grammar, six kinds, EXTERNAL, edge key, opacity rule — D-3.2, D-3.3
- [x] **P0-C2** Pydantic v2 models for batch, graph, diff — D-3.4, D-3.6, D-3.7
- [x] **P0-C3** `make contracts` publishes `contracts/openapi.json` — D-3.1
- [x] **P0-C4** Valid + invalid fixtures, one per rule in D-3.4 — D-3.9
- [x] **P0-C5** Fixture validation tests — tests T-3.1, T-3.2, T-3.7
- [x] **P0-C6** Go round-trip test against `batch.valid.json` — test T-3.3
- [x] **P0-S7** Hook: `openapi.json` change regenerates the TypeScript client — README §8
- [x] **P0-T2** CI job fails on contract drift — test T-3.6

**Phase 0 gate:** fixtures validate against the schema; ID, EXTERNAL, and edge-key rules exist in
prose **and** in tests.

### Phase 2 — ingest and graph in use

- [ ] **P2-C7** Edge-key identity verified across agent, storage, and graph grouping — test T-3.4
- [x] **P2-C8** Generated TS types compile against the response fixtures — test T-3.5
- [ ] **P2-C9** Ordering stable across repeated identical queries — test T-3.8

**Phase 2 gate:** posting the same `batch_id` twice changes counts only once.

### Phase 3 — diff contract

- [ ] **P3-C10** Diff response shape: baseline, current, delta, percentage, reason — D-3.7
- [ ] **P3-C11** Zero-baseline percentage undefined and reported as a reason, never `Infinity`
- [ ] **P3-C12** Missing period treated as zero, not null — D-3.7

**Phase 3 gate:** diff classification deterministic at exact bucket boundaries, calculation visible.

### Standing invariants — re-verify at every phase gate

- [ ] IDs are never parsed by any consumer (no `split(':')` / `split('/')` anywhere)
- [ ] No hand-written payload type duplicates a generated one
- [ ] `openapi.json` in the tree matches what FastAPI generates
- [ ] `bytes_*` absent — not zero — until P4-X1 passes
