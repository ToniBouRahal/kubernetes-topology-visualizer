# ADR-004: Backend Service — FastAPI Ingestion, Graph, and Diff

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §5.5 · Source of truth §10, §12, §15
- **Component path:** `backend/`
- **Owning phases:** Phase 2 (in-memory), Phase 3 (PostgreSQL + diff), Phase 4 (details)
- **Language / runtime:** Python 3.10+, FastAPI, Pydantic v2

## 1. Scope

Application layering, request handling, domain logic for graph assembly and diff computation,
logging, metrics, and lifecycle. The wire shapes belong to ADR-003; the storage engine and schema
belong to ADR-005.

## 2. What the prototype proved, and what must change

`backend/main.py` is 101 lines: a single module holding models, a dict store, a lock, and three
routes. It proved that the agent can POST and the browser can read. Everything structural about it
must go.

| # | Prototype behaviour | Location | Required behaviour |
|---|---|---|---|
| B1 | Single file: models + store + routes | whole file | API / domain / persistence layers (ADR-001 §5.5) |
| B2 | `allow_origins=["*"]` | `main.py:16-21` | CORS restricted to `CORS_ALLOWED_ORIGINS` |
| B3 | Module-level dict + `threading.Lock` | `main.py:43-44` | Repository interface with injected implementation |
| B4 | `/ingest`, `/graph`, `/health` unversioned | `main.py:49-101` | `/api/v1/*` plus `/health/live`, `/health/ready`, `/metrics` |
| B5 | No idempotency — repeated batches always add | `main.py:60-62` | `batch_id` dedupe; `200` vs `202` |
| B6 | `timestamp: str`, never parsed or used | `main.py:36`, `main.py:56` | RFC 3339 aware datetimes, validated |
| B7 | Iterates a `set` into the response | `main.py:78`, `main.py:95-99` | Deterministic explicit ordering (ADR-003 D-3.6) |
| B8 | Derives labels by splitting the ID | `main.py:97-98` | Labels are stored fields; IDs stay opaque |
| B9 | Unbounded response | `main.py:101` | `GRAPH_MAX_NODES` / `GRAPH_MAX_EDGES` + truncation indicator |
| B10 | `print`-style defaults, no request IDs | — | Structured JSON logs with request IDs |
| B11 | No `lastSeconds` bound, no `from`/`to`, no diff | `main.py:76` | Presets, custom ranges, max span, diff |
| B12 | Wall-clock read directly in handlers | `main.py:56`, `main.py:77` | Injected `Clock` — tests must not depend on real time |

## 3. Decisions

### D-4.1 — Layering

```text
backend/app/
  api/          routers, request/response models, dependency wiring, error mapping
  domain/       pure logic: graph assembly, diff classification, windowing, truncation
  persistence/  repository protocol + in-memory and PostgreSQL implementations
  settings.py   pydantic-settings, environment-driven
  main.py       app factory, lifespan, middleware, router registration
```

`domain/` imports nothing from `api/` or `persistence/` — it takes plain dataclasses and returns
plain dataclasses. Diff classification and window arithmetic are the two places where subtle bugs
hide, and both must be unit-testable with no I/O and no clock.

### D-4.2 — Dependency injection

Storage and clock are FastAPI dependencies (ADR-001 §5.5). Tests override them. A handler must never
call `datetime.now()` directly (B12) — bucket-boundary tests in Phase 3 are impossible otherwise.

### D-4.3 — Ingestion

```text
POST /api/v1/ingest/batches
  1. enforce body size limit before parsing
  2. validate schema_version           → 400 if unsupported
  3. validate payload                  → 422 with field-level detail
  4. enforce edge-count limit          → 413
  5. repository.ingest_batch(batch)    → transactional (ADR-005 D-5.3)
       returns ALREADY_INGESTED        → 200
       returns INGESTED                → 202
  6. storage unavailable               → 503, agent retries
```

Idempotency is decided by the repository inside the same transaction as the edge merge, never by a
pre-check in the handler — a read-then-write check is a race under two agents retrying at once.

### D-4.4 — Graph assembly

Nodes are derived **only** from edges present in the selected window (ADR-001 §5.5). A node with no
edge in the window does not appear, even if it exists in the `nodes` table. This keeps the graph an
honest statement about the window rather than a cumulative inventory.

Pipeline: resolve window → query buckets → aggregate by edge key → apply namespace/kind/query
filters → apply `include_external` / `include_unresolved` → derive node set → sort deterministically
→ truncate with indicator → attach summary counters and effective filters.

Truncation is applied **after** filtering and sorting so it is reproducible, and the response says
what was dropped and why.

### D-4.5 — Diff

Implemented in `domain/` as a pure function of two edge collections and a threshold, per ADR-003
D-3.7. Requirements that are easy to get wrong and must be tested explicitly:

- a missing period contributes zero, not null;
- percentage delta is undefined when baseline is zero — emit the reason string, never `Infinity` or
  `NaN` (both break JSON and the UI);
- the threshold comparison is `>=` on the absolute percentage, and the boundary case is tested;
- classification order is deterministic when an edge could satisfy multiple rules.

### D-4.6 — Observability and safety

- Structured JSON logs with a request ID from middleware, propagated into domain logs.
- `/metrics`: `batches_accepted`, `batches_deduplicated`, `batches_rejected`, `graph_queries_total`,
  `graph_query_duration_seconds`, `stored_edge_buckets`, `retention_deletions_total`.
- `/health/live` is process liveness only. `/health/ready` fails if migrations did not run or
  storage is unreachable (ADR-001 §5.4).
- Exception handlers map domain errors to status codes and return a stable error envelope. **Never**
  a raw stack trace, and never a database URL or credential in a message (Phase 3 acceptance
  criterion).
- Graceful shutdown via lifespan: stop accepting, finish in-flight requests, close the pool.
- Retention runs as a periodic task in the app lifespan (ADR-005 D-5.5).

### D-4.7 — Detail routes

`GET /api/v1/nodes/{node_id}` and `/api/v1/edges/{edge_id}` return, for the requested window:
namespace, kind, label, incoming and outgoing dependencies, ports, connection counts, optional
bytes, first seen, last seen. `node_id` is URL-encoded — the ID contains `:` separators, so route
definitions and the frontend client must both handle encoding. Unknown ID → `404`, not an empty
object.

## 4. Implementation guide

Phase 2 order:

1. `settings.py`, app factory, structured logging middleware, `/health/live`.
2. Pydantic models per ADR-003 → export `openapi.json`.
3. `RepositoryProtocol` + `InMemoryRepository` (a real implementation of the same interface, not a
   stub).
4. Ingest route with dedupe, then graph route with presets, then namespaces and detail routes.
5. `/metrics`, `/health/ready`, CORS from settings, error handlers.

Phase 3 adds the PostgreSQL repository behind the same protocol (ADR-005), custom `from`/`to`, and
diff. `InMemoryRepository` is retained permanently for fast unit tests — ADR-001 §7 Phase 3 is
explicit that it is removed from the *deployment*, not from the *test suite*.

## 5. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`pyright-lsp`** | All Python work | Enforces the layer boundaries in D-4.1 and catches Pydantic v2 typing errors that only surface at request time. `/plugin install pyright-lsp@claude-plugins-official` |
| **`topology-contract`** (ADR-003) | Editing any model, query parameter, or status mapping | The backend is where the contract is defined; drift starts here. |
| **`context7`** | Pydantic v2 validators, FastAPI lifespan, dependency overrides, pydantic-settings | The prototype is Pydantic v1-shaped. v2 validator and settings APIs differ substantially — read current docs rather than pattern-matching the prototype. |
| **`adr-guard`** (custom) | Any proposed new route or response field | ADR-001 §5.3 fixes the route list. SSE, auth, and multi-cluster are deferred (§13). |

### Situational

- **`/code-review high`** on `domain/` after diff lands. Diff classification is dense boundary logic
  and is where a review pays for itself.
- **`/simplify`** after Phase 3, when the in-memory and PostgreSQL repositories have both settled
  and duplication between them is visible.
- **`/security-review`** before Phase 5 — CORS, error leakage, request limits, credential handling.
- **`playwright`** — not for this component directly, but backend contract changes break the
  frontend E2E first; run ADR-006's suite after touching graph or diff responses.
- **`k8s-demo-loop`** (ADR-007) — for verifying `/health/ready` behaviour under a real failed
  database.

### Do not use

- **`dataviz`** — the backend returns numbers, not visual encodings. Traffic-intensity scaling is a
  frontend decision (ADR-006 D-6.4); putting it here would bake a presentation choice into the API.
- **`artifact-*`** — no artifact surface in this component.
- **`claude-api` skill** — no LLM involvement anywhere in this project.

## 6. Test requirements (ADR-001 §8 row 4)

| ID | Test |
|---|---|
| T-4.1 | Schema validation: every invalid fixture from ADR-003 D-3.9 → correct status code |
| T-4.2 | Idempotency: same `batch_id` twice → `202` then `200`, counts change once |
| T-4.3 | Concurrent duplicate `batch_id` from two clients → still counted once |
| T-4.4 | Window presets resolve to the correct absolute range against a frozen clock |
| T-4.5 | Custom `from`/`to`: inverted, overlapping, and over-max-span all rejected |
| T-4.6 | Diff: `NEW`, `REMOVED`, `CHANGED`, unchanged-suppressed, threshold boundary, zero-baseline percentage undefined |
| T-4.7 | Filters: namespace, kind, query substring, `include_external`, `include_unresolved` |
| T-4.8 | Truncation at `GRAPH_MAX_NODES`/`GRAPH_MAX_EDGES` sets the indicator and is reproducible |
| T-4.9 | Nodes derived only from in-window edges |
| T-4.10 | Deterministic ordering across repeated identical queries |
| T-4.11 | Error responses contain no stack trace and no credentials |
| T-4.12 | `/health/ready` fails when the repository reports unavailable |
| T-4.13 | Detail routes: URL-encoded IDs, unknown ID → 404 |

All run against `InMemoryRepository` with an injected fixed clock. ADR-005 owns the PostgreSQL-backed
equivalents.

## 7. Acceptance criteria

Phase 2: traffic appears in the browser within 20 s; duplicate `batch_id` counted once; graph shows
direction, port, connection count, first seen, last seen; filters and search are deterministic.

Phase 3: preset and custom ranges work; controlled changes classify as `NEW`/`REMOVED`/`CHANGED`
with the calculation visible; database failure fails readiness with an actionable, credential-free
error; queries are deterministic at exact bucket boundaries.

## 8. Consequences

- **Python for a hot ingest path.** Accepted deliberately in ADR-001 §11: the language-neutral
  contract between a Go collector and a Python API is part of what the FYP demonstrates. The
  performance target (500 nodes / 2,000 edges under 500 ms p95) is a query-side target, and
  aggregation happens in PostgreSQL, not in Python loops.
- **Two repository implementations forever.** Slight duplication, bought deliberately: unit tests
  stay in-memory and fast, so the Phase 3 database tests can be the slow minority.
- **Strict validation rejects real agent traffic during bring-up.** Intended. `batches_rejected`
  with a reason is a diagnosable failure; a silently coerced field is not.
- **No streaming.** Polling every five seconds is the accepted release (ADR-001 §5.6); SSE is
  deferred to §13 and needs its own ADR.

## 9. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). `[ ]` open · `[~]` in progress · `[x]` done ·
`[!]` blocked · `[-]` dropped with reason.

### Phase 2 — end-to-end live product

- [x] **P2-B1** API/domain/persistence layering, `settings.py`, app factory — D-4.1 (fixes B1)
- [x] **P2-B2** Structured JSON logging with request IDs — D-4.6 (fixes B10)
- [x] **P2-B3** `RepositoryProtocol` + `InMemoryRepository`, injected clock — D-4.2, ADR-005 D-5.1 (fixes B3, B12)
- [x] **P2-B4** `POST /api/v1/ingest/batches`: 202/200/400/422/413 — D-4.3 (fixes B4, B5) · tests T-4.1 – T-4.3
- [x] **P2-B5** `GET /api/v1/graph`: presets, filters, deterministic order, truncation — D-4.4 (fixes B7, B9, B11) · tests T-4.4, T-4.7 – T-4.10
- [x] **P2-B6** `/namespaces`, `/nodes/{id}`, `/edges/{id}` with URL-encoded IDs — D-4.7 (fixes B8) · test T-4.13
- [ ] **P2-B7** `/health/live`, `/health/ready`, `/metrics`, CORS from settings, error handlers, graceful shutdown — D-4.6 (fixes B2) · tests T-4.11, T-4.12
- [ ] **P2-T3** Backend API test suite against the in-memory repository — §6 · **→ codex**

**Phase 2 gate** (ADR-001 §7): traffic visible in the browser within 20 s · duplicate `batch_id`
counted once · graph shows direction, port, count, first/last seen · deterministic filters and
search.

### Phase 3 — history and diff

- [ ] **P3-B8** Custom `from`/`to`; reject inverted, overlapping, and over-max-span ranges — D-4.4 · test T-4.5
- [ ] **P3-B9** `GET /api/v1/diff` with NEW/REMOVED/CHANGED and visible calculation — D-4.5 · test T-4.6 · **→ codex** (pure function, boundary-heavy)
- [ ] **P3-B10** Swap the deployed repository to PostgreSQL; keep in-memory for unit tests — ADR-005 D-5.1
- [ ] **P3-B11** Retention task wired into the app lifespan — ADR-005 D-5.5

**Phase 3 gate:** preset and custom ranges work · controlled change classifies correctly with the
calculation visible · database failure fails readiness with a credential-free error · deterministic
at exact bucket boundaries.

### Phase 4 — detail completeness

- [ ] **P4-B12** Detail responses carry incoming/outgoing dependencies, ports, counts, optional bytes, first/last seen — D-4.7
- [ ] **P4-B13** Byte fields surfaced through graph and diff **only if** P4-X1 passes

### Standing invariants — re-verify at every phase gate

- [ ] No handler reads the wall clock directly (B12)
- [ ] No response contains a stack trace, DSN, or credential — test T-4.11
- [ ] Nodes derived only from in-window edges — test T-4.9
- [ ] Every list response has an explicit sort key — test T-4.10
