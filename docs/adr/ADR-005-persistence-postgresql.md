# ADR-005: Persistence — PostgreSQL Schema, Idempotency, and Retention

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §5.4 · Source of truth §11, §12
- **Component path:** `backend/app/persistence/`, `backend/migrations/`
- **Owning phases:** Phase 3 (primary), Phase 4 (nullable byte columns), Phase 5 (restart evidence)
- **Engine:** PostgreSQL, in-cluster StatefulSet with PVC; external `DATABASE_URL` supported

## 1. Scope

The repository implementations, the schema, migrations, the idempotent ingest transaction, query
shapes, indexes, and retention. The PostgreSQL *deployment* (StatefulSet, PVC, Secret) belongs to
ADR-007; the routes that call this layer belong to ADR-004.

## 2. Context

The prototype has no persistence at all — a module-level dict (`backend/main.py:43`) that a restart
erases. This component therefore has no reusable prior art, and it carries three of ADR-001's
Definition-of-Done items on its own: history survives restarts, retries do not double count, and
queries are deterministic at bucket boundaries.

SQLite was explicitly rejected (ADR-001 §11). PostgreSQL is mandatory from Phase 3 onward.

## 3. Decisions

### D-5.1 — Repository interface

Both implementations satisfy one protocol (ADR-004 D-4.1):

```python
class TopologyRepository(Protocol):
    async def ingest_batch(self, batch: Batch) -> IngestResult      # INGESTED | ALREADY_INGESTED
    async def query_edges(self, window: Window, filters: Filters) -> list[EdgeAggregate]
    async def get_node(self, node_id: str, window: Window) -> NodeDetail | None
    async def get_edge(self, edge_id: str, window: Window) -> EdgeDetail | None
    async def list_namespaces(self, window: Window) -> list[str]
    async def purge_expired(self, before: datetime) -> PurgeStats
    async def health(self) -> StorageHealth
```

`InMemoryRepository` is not a stub — it implements the same semantics, including idempotency and
bucketing, so the ADR-004 unit suite is meaningful. Where the two can diverge (ordering, boundary
inclusivity), a shared contract-test suite runs against **both**. That suite is the main defence
against "passes in unit tests, fails in the demo".

### D-5.2 — Schema

Per ADR-001 §5.4, with types fixed here:

```sql
CREATE TABLE ingest_batches (
  batch_id    TEXT PRIMARY KEY,
  cluster_id  TEXT        NOT NULL,
  agent_id    TEXT        NOT NULL,
  observed_at TIMESTAMPTZ NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
  id              TEXT PRIMARY KEY,          -- canonical ID, ADR-003 D-3.2
  cluster_id      TEXT NOT NULL,
  kind            TEXT NOT NULL,             -- CHECK against the six allowed kinds
  namespace       TEXT NULL,                 -- NULL for external:EXTERNAL
  name            TEXT NOT NULL,
  attributes_json JSONB NOT NULL DEFAULT '{}',
  first_seen      TIMESTAMPTZ NOT NULL,
  last_seen       TIMESTAMPTZ NOT NULL
);

CREATE TABLE edge_buckets (
  bucket_start     TIMESTAMPTZ NOT NULL,     -- truncated to the minute, UTC
  cluster_id       TEXT NOT NULL,
  source_id        TEXT NOT NULL REFERENCES nodes(id),
  target_id        TEXT NOT NULL REFERENCES nodes(id),
  protocol         TEXT NOT NULL,
  destination_port INTEGER NOT NULL,
  connection_count BIGINT NOT NULL,
  bytes_sent       BIGINT NULL,              -- NULL until Phase 4 gate
  bytes_received   BIGINT NULL,
  first_seen       TIMESTAMPTZ NOT NULL,
  last_seen        TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (bucket_start, cluster_id, source_id, target_id, protocol, destination_port)
);
```

`kind` carries a `CHECK` constraint over the six allowed values — the database refuses an
out-of-contract kind even if a future code path forgets to validate.

Indexes:

```sql
CREATE INDEX ON edge_buckets (cluster_id, bucket_start DESC);
CREATE INDEX ON edge_buckets (source_id, bucket_start DESC);
CREATE INDEX ON edge_buckets (target_id, bucket_start DESC);
CREATE INDEX ON nodes (cluster_id, namespace, kind);
```

Every graph and diff query is a bounded `bucket_start` range scan; the first index is the one that
carries the 500 ms p95 target.

### D-5.3 — The idempotent ingest transaction

This is the most important decision in the component. One transaction:

```sql
BEGIN;
  INSERT INTO ingest_batches (batch_id, cluster_id, agent_id, observed_at)
  VALUES (...)
  ON CONFLICT (batch_id) DO NOTHING;
  -- 0 rows affected  → duplicate: COMMIT and return ALREADY_INGESTED, touching nothing else

  INSERT INTO nodes (...) VALUES (...)
  ON CONFLICT (id) DO UPDATE
    SET last_seen  = GREATEST(nodes.last_seen,  EXCLUDED.last_seen),
        first_seen = LEAST   (nodes.first_seen, EXCLUDED.first_seen),
        attributes_json = EXCLUDED.attributes_json;

  INSERT INTO edge_buckets (...) VALUES (...)
  ON CONFLICT (bucket_start, cluster_id, source_id, target_id, protocol, destination_port)
  DO UPDATE SET
    connection_count = edge_buckets.connection_count + EXCLUDED.connection_count,
    bytes_sent       = COALESCE(edge_buckets.bytes_sent,     0) + COALESCE(EXCLUDED.bytes_sent,     0),
    bytes_received   = COALESCE(edge_buckets.bytes_received, 0) + COALESCE(EXCLUDED.bytes_received, 0),
    first_seen       = LEAST   (edge_buckets.first_seen, EXCLUDED.first_seen),
    last_seen        = GREATEST(edge_buckets.last_seen,  EXCLUDED.last_seen);
COMMIT;
```

Three properties, each of which a test must prove:

1. **The batch-id insert comes first and gates everything.** A duplicate returns before any counter
   moves. This is what makes agent retries safe (ADR-001 §6 Reliability).
2. **The whole thing is one transaction.** A crash between the batch record and the edge merge would
   otherwise make a retry either double-count or be rejected as already-ingested with nothing stored
   — both are silent corruption.
3. **The `COALESCE` on bytes preserves the null/zero distinction only at the column level.** Once
   any byte value is recorded for a bucket, that bucket's byte total becomes non-null. Until the
   Phase 4 gate passes, the agent sends no byte fields and the columns stay `NULL` — which is what
   the UI reads to decide whether to display byte-based intensity.

Node insertion must precede edge insertion in the same transaction to satisfy the foreign keys, and
nodes must be inserted in a deterministic order to avoid deadlocks between concurrent agents.

### D-5.4 — Bucketing

`bucket_start = date_trunc('minute', observed_at AT TIME ZONE 'UTC')`, computed in the domain layer,
not in SQL, so `InMemoryRepository` produces identical buckets.

Boundary semantics are fixed and documented: a window `[from, to)` includes buckets where
`bucket_start >= from AND bucket_start < to`. Half-open, lower-inclusive, everywhere — presets,
custom ranges, diff baselines, and retention. Phase 3 requires determinism at exact bucket
boundaries, and that is only achievable if a single convention is written down and tested (T-5.7).

### D-5.5 — Retention

A periodic task in the app lifespan deletes `edge_buckets` older than `RETENTION_HOURS` (default 24),
then deletes `nodes` no longer referenced by any surviving bucket. Deletion is batched with a `LIMIT`
loop so a large purge cannot hold a long transaction against ingestion. Emits
`retention_deletions_total`.

### D-5.6 — Migrations

Explicit, versioned, forward-only, run at startup before the app reports ready. Migration failure
must fail readiness loudly rather than starting a backend against a half-built schema. Every
migration is applied by CI against an empty database and against a database with seeded data.

### D-5.7 — Credentials

`DATABASE_URL` comes from a Kubernetes Secret and is never written to a committed values file
(ADR-001 §5.4). It must never appear in a log line, an error response, or an exception message —
including SQLAlchemy connection errors, which embed the DSN by default. Sanitise at the exception
handler boundary and test for it (T-5.11); this is a Phase 3 acceptance criterion.

## 4. Implementation guide

```text
backend/
  app/persistence/
    protocol.py        the Protocol from D-5.1
    memory.py          in-memory implementation
    postgres.py        SQLAlchemy Core / asyncpg implementation
    queries.py         SQL for graph, diff, details, retention
  migrations/          versioned, forward-only
  tests/
    contract/          runs against BOTH implementations
    postgres/          engine-specific: transactions, constraints, restart
```

Order of work in Phase 3:

1. Write the contract-test suite against `InMemoryRepository` first — it defines the semantics.
2. Migrations for the three tables, constraints, and indexes.
3. `postgres.py` `ingest_batch` with the D-5.3 transaction. Run the contract suite against it. It
   will fail on ordering and boundary details first; those failures are the point.
4. Query methods, then retention, then health/readiness.
5. Restart tests: `kubectl delete pod` for backend and database, assert history and counts.

Use a real PostgreSQL for the database tests (container or the kind-deployed instance). Do not mock
the driver — every property in D-5.3 depends on real `ON CONFLICT` and real transaction semantics.

## 5. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`pyright-lsp`** | All Python work | Keeps both repository implementations provably conformant to the Protocol — the type checker is what makes `InMemoryRepository` a trustworthy test double. |
| **`context7`** | Writing SQLAlchemy 2.x, asyncpg, or migration-tool code | SQLAlchemy 2.x async style differs sharply from 1.x patterns, and the ORM-vs-Core choice for upserts is version-sensitive. Read current docs. |
| **`topology-contract`** (ADR-003) | Any change to the edge key, ID column, or ordering | The primary key here *is* the edge key. If they diverge, the graph silently splits nodes. |
| **`k8s-demo-loop`** (ADR-007) | Restart and persistence testing | Pod-delete/recreate cycles against real PVCs are the only way to prove the Phase 3 and Phase 5 persistence criteria. |
| **`adr-guard`** (custom) | Any proposal to add a table, change the engine, or add HA | ADR-001 §11 rejected SQLite; §13 defers HA PostgreSQL. |

### Situational

- **`/code-review high`** on `postgres.py` and `queries.py` after Phase 3. Upsert and window
  boundary logic is exactly the kind of code where a careful read finds an off-by-one that tests
  written by the same author would not.
- **`/security-review`** — credential handling (D-5.7) and injection surface in filter parameters.
- **`/simplify`** — after both implementations settle, to collapse duplicated aggregation logic into
  the shared domain layer.

### Do not use

- **`cloud-sql-postgresql`, `aiven`, `cockroachdb`, `alloydb`** — these marketplace plugins target
  managed cloud databases. The decision here is an in-cluster StatefulSet plus an external
  `DATABASE_URL` option. Installing one of these will produce guidance for the wrong deployment
  model.
- **`dataviz`** — no visual output in this layer.
- **`terraform`** — provisioning is Helm's job (ADR-007).

## 6. Test requirements (ADR-001 §8 row 5)

| ID | Test |
|---|---|
| T-5.1 | Migrations apply to an empty database and to a seeded one; readiness fails when they fail |
| T-5.2 | Duplicate `batch_id` → `ALREADY_INGESTED`, zero counter movement |
| T-5.3 | Concurrent identical batches from two connections → counted exactly once |
| T-5.4 | Transaction rollback: injected failure after the batch insert leaves **no** batch row and no edge change |
| T-5.5 | Bucket upsert accumulates `connection_count` and takes min/max of first/last seen |
| T-5.6 | Byte columns stay `NULL` when the agent sends no byte fields |
| T-5.7 | Window boundary: an event exactly at `from` is included, exactly at `to` is excluded — for graph, diff, and retention |
| T-5.8 | Retention deletes expired buckets and orphaned nodes, keeps referenced nodes, emits counts |
| T-5.9 | Backend pod restart preserves committed history and does not inflate counts |
| T-5.10 | Database pod restart with PVC preserves history |
| T-5.11 | No log line, error response, or exception message contains the DSN or password |
| T-5.12 | The contract suite passes identically against both repository implementations |
| T-5.13 | Graph query for 500 nodes / 2,000 edges returns under 500 ms p95 locally |

## 7. Acceptance criteria

Phase 3: restarts preserve committed topology without inflating counts; last 5 m / 15 m / 1 h and
custom ranges work; database failure fails readiness with an actionable, credential-free error;
graph and diff are deterministic at exact bucket boundaries.

Phase 5: deleting and recreating backend or database pods preserves history through their PVCs, with
recorded evidence under `docs/evaluation/`.

## 8. Consequences

- **One-minute buckets fix the resolution floor.** Sub-minute queries cannot be more precise than
  the bucket, and a window shorter than a minute returns whole buckets. Document this in
  `docs/limitations.md` — the `1m` preset is a bucket, not a sliding window.
- **Retention bounds the history.** A 24-hour default means a comparison against yesterday is
  impossible unless `RETENTION_HOURS` is raised. This is a deliberate resource trade (ADR-001 §13
  defers long-term analytics), and the UI must say so rather than showing an empty baseline.
- **PostgreSQL raises the demo's resource footprint.** Accepted in ADR-001 §10; the kind values must
  set modest resource requests so a laptop can run the full stack.
- **Foreign keys couple node and edge writes.** Slightly more transaction work per batch, in
  exchange for making an orphaned edge structurally impossible.

## 9. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). `[ ]` open · `[~]` in progress · `[x]` done ·
`[!]` blocked · `[-]` dropped with reason.

### Phase 2 — interface first

- [x] **P2-B3** `TopologyRepository` protocol + `InMemoryRepository` with real semantics — D-5.1
- [ ] **P2-D0** Contract-test suite written against the in-memory implementation first — D-5.1

Writing the contract suite before any SQL exists is deliberate: it defines the semantics that
PostgreSQL must then match, rather than the reverse.

### Phase 3 — PostgreSQL

- [ ] **P3-D1** Migrations: three tables, `CHECK` on `kind`, foreign keys, four indexes — D-5.2, D-5.6 · test T-5.1
- [ ] **P3-D2** `ingest_batch` single transaction: batch-id gate → nodes → edge buckets — D-5.3 · tests T-5.2 – T-5.5 · **→ codex** (independent derivation of the upsert, then compare)
- [ ] **P3-D3** Bucketing in the domain layer; half-open `[from, to)` everywhere — D-5.4 · test T-5.7
- [ ] **P3-D4** Query methods: graph, node/edge detail, namespaces — D-5.1 · test T-5.13
- [ ] **P3-D5** Retention with batched `LIMIT` deletes + `retention_deletions_total` — D-5.5 · test T-5.8
- [ ] **P3-D6** Readiness gated on migrations and storage; DSN sanitised at the handler boundary — D-5.7 · test T-5.11
- [ ] **P3-D7** Contract suite passes identically against **both** repositories — D-5.1 · test T-5.12
- [ ] **P3-D8** Restart tests: backend pod, then database pod with PVC — tests T-5.9, T-5.10
- [ ] **P3-D9** Byte columns remain `NULL` while the agent sends no byte fields — test T-5.6

**Phase 3 gate** (ADR-001 §7): restarts preserve committed topology without inflating counts ·
5 m / 15 m / 1 h / custom ranges · database failure fails readiness with an actionable,
credential-free error · deterministic at exact bucket boundaries.

### Phase 4 — byte columns

- [ ] **P4-D10** Populate `bytes_sent` / `bytes_received` through the transaction — **only if** P4-X1 passes

### Phase 5 — evidence

- [ ] **P5-D11** Recorded restart/persistence evidence under `docs/evaluation/` — ADR-008 D-8.6
- [ ] **P5-D12** 500 nodes / 2,000 edges under 500 ms p95, measured and recorded — test T-5.13

### Standing invariants — re-verify at every phase gate

- [ ] The batch-id insert gates the transaction; a duplicate moves no counter — test T-5.2
- [ ] The DB primary key tuple is identical to the agent's aggregation key — ADR-003 test T-3.4
- [ ] No DSN or password in any log, error, or exception — test T-5.11
- [ ] Window boundaries are half-open and lower-inclusive everywhere — test T-5.7
