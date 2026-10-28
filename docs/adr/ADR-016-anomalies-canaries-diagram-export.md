# ADR-016: Rule-Based Anomalies, Canary Workloads, and Diagram Export

- **Status:** Accepted for implementation
- **Date:** 2026-10-26
- **Parent:** ADR-001 §13 (deferred: "ML/prediction/anomaly detection" — taken into scope here as
  **deterministic rules only**, no learning or prediction) · §4.2 (no new mandatory dependency) ·
  §6 (privacy: nothing leaves the cluster)
- **Amends:** ADR-003 (two additive contract changes: `GraphNode.canary`, `GET /api/v1/anomalies`),
  ADR-004 (one new route, one background evaluator), ADR-006 (anomaly list, canary marking, export
  button), ADR-007 D-7.5 (`backend.canaries`, `backend.anomalies.*`), ADR-015 D-15.4 (`graph -o
  mermaid`, `anomalies` command)
- **Component path:** `backend/app/domain/anomalies.py`, `backend/app/api/`,
  `frontend/src/features/anomalies/`, `frontend/src/features/export/`, `cli/internal/`,
  `charts/topology-visualizer/`
- **Amended by:** ADR-018 — the "nothing calls out of the cluster" rule (§1, §3) and the
  rejection of webhooks (§4) no longer hold for chat alerts, which are opt-in and off by default
- **Owning phase:** Post-Phase-5 extension

## 1. Context

The graph answers *what talks to what*. Three follow-up questions keep coming up in the demo and
none of them needs new data — only a different reading of the edges already stored:

1. **What is unusual right now?** Compare mode answers this when someone thinks to open it and
   pick the right two periods. Nobody is watching compare mode at 03:00.
2. **Did anything touch something that nothing should touch?** A decoy workload that no
   legitimate client ever calls turns any connection to it into a high-confidence signal — the
   classic canary. This tool already sees every TCP connection to it, without the decoy running
   any detection code.
3. **Can I put this picture in a document?** Today the answer is a screenshot.

**Nothing in this ADR calls an external service.** No LLM, no webhook, no SaaS. Every rule runs in
the backend over PostgreSQL rows the backend already owns; every signal leaves the process only as
an API response, a log line, or a `/metrics` sample that the operator's own tooling may scrape.

## 2. Decision

### Anomalies

**D-16.1 — Five deterministic rules, one baseline.** For a window `[start, end)` the baseline is
the `backend.anomalies.baselineHours` immediately before it, `[start − B, start)`, default 24 h.
Each rule compares an edge (contract edge key, `contracts/ids.md` §4) across the two:

| Type | Severity | Fires when |
|---|---|---|
| `CANARY_CONTACT` | critical | the edge's target is a canary workload (D-16.4) — regardless of baseline |
| `NEW_EXTERNAL_EGRESS` | critical | target is `EXTERNAL` and the edge (same port) has no observation in the baseline |
| `NEW_EDGE` | warning | any other edge with no observation in the baseline |
| `FAILURE_SPIKE` | critical | ≥ `minAttempts` attempts in the window, failure ratio ≥ 20 %, and ≥ 20 percentage points above the baseline's ratio |
| `SLOW_SETUP` | warning | ≥ `minAttempts` timing samples in both periods, and the window's mean TCP setup ≥ 3 × the baseline's and ≥ 1 ms slower |

One edge may raise several types. The thresholds are settings with these defaults, not learned —
the reason string on every anomaly states the numbers compared and the threshold applied, as
`DiffEdge.reason` does.

**D-16.2 — An empty baseline suppresses the novelty rules.** If the baseline holds no edges at all
(fresh install, retention shorter than the baseline, a filter that excludes everything) every edge
would be "new", which is noise, not a finding. `NEW_EDGE` and `NEW_EXTERNAL_EGRESS` are then not
raised and `summary.baseline_observed` is false, which the UI states in words. A *partially*
covered baseline is not detected; that limit is recorded in `docs/limitations.md`.

**D-16.3 — Two ways out: a query and an evaluator.**

- `GET /api/v1/anomalies` takes the graph's window and filter parameters and returns the
  anomalies, the baseline window, the referenced nodes (so no client parses an id), and a summary.
  Sorted by edge key, then type; capped at `GRAPH_MAX_EDGES` after sorting.
- A background task in the backend, like the retention loop, evaluates the last 5 minutes every
  60 s with no filters. It sets `topology_backend_active_anomalies{type="…"}` gauges (every type
  always present, zero included, so an alert expression never sees a missing series) and logs one
  `WARNING` line per anomaly key that was not active on the previous pass. Alerting is the
  operator's existing Prometheus/Alertmanager or log pipeline; this tool sends nothing.

### Canaries

**D-16.4 — Canaries are named in the chart, matched on node fields.** `backend.canaries` is a list
of `namespace/name` or `namespace/Kind/name` selectors, templated to `CANARY_WORKLOADS`. A node
matches on its `namespace`, `kind` and `name` fields — never by parsing its id
(`contracts/ids.md` §2). `GraphNode` gains `canary: bool` (default false, additive, v1-compatible).

**D-16.5 — Contact is counted at ingest, not only at query time.** Every ingested observation whose
target matches a canary adds its attempts (successful + failed) to
`topology_backend_canary_connections_total` and logs a `WARNING` naming the source workload. Ingest
is the earliest point the backend knows, and a counter survives the window rolling past.

**D-16.6 — Only inbound contact counts.** A canary that *opens* connections is a different and
weaker signal (it may have a sidecar, a health check, DNS); it is drawn as a canary but raises
nothing.

### Diagram export

**D-16.7 — Mermaid, from the API response.** The export is a Mermaid `flowchart LR` built from the
`GraphResponse` the user is looking at — window and filters included, the canvas's grouping and
render budget not. Mermaid because it is text, diffs in git, and renders in GitHub, GitLab and most
documentation tools without this application. Nodes get synthetic ids (`n1`, `n2`, … in node-id
order), so no user-controlled string becomes Mermaid syntax; labels have `"`, `<`, `>`, `#`, `|`
and newlines escaped or replaced. One `subgraph` per namespace; `EXTERNAL` outside them. Edge label
`TCP:port · N` (plus `· F failed`); failed-only edges dashed; canary nodes styled with a
`canary` class. A comment header records the window and that counts are TCP connections, not
requests.

**D-16.8 — Two implementations, one golden file.** The browser builds it in TypeScript (a download
of `topology-<start>.mmd`); the CLI builds it in Go (`kubectl topology graph -o mermaid`). Both are
tested against the same fixture, `cli/testdata/graph.json`, and must produce byte-identical
`cli/testdata/graph.mmd` — the same drift defence as the contract itself.

## 3. Consequences

**No new dependency, no egress.** The backend NetworkPolicy is unchanged; nothing in this ADR opens
a connection from the cluster.

**One extra query per anomaly request,** over a period up to `baselineHours` long. At the default
that is a 24 h aggregate — the same cost as the graph's longest preset — and the evaluator pays it
once a minute. Operators with very large edge counts can lower `baselineHours`.

**These are rules, not intelligence.** A slow ramp that never crosses a threshold, or a new edge
that was also seen once yesterday, is not reported. The thresholds are chosen to be explainable in
one sentence, which is what an FYP evaluation can defend; the limitations entry says so.

**Canary names are configuration, not secrets.** Anyone who can read the topology can see which
nodes are canaries. Hiding that from viewers would need per-user authorisation (deferred by
ADR-014).

## 4. Alternatives rejected

**Statistical or ML detection (z-scores, seasonality, isolation forests).** ADR-001 §13 deferred
ML for a reason: with one-minute buckets and a few days of history there is not enough data to fit
anything honestly, and a score nobody can explain is not evidence.

**Webhooks, Slack, e-mail.** Outbound integrations are exactly what this ADR excludes; the
operator's Alertmanager already does routing, silencing and deduplication better.

**An LLM to summarise or interpret anomalies.** Excluded by the same rule: the topology does not
leave the cluster.

**Canary by pod label or annotation.** Needs the agent to carry labels into the batch — an
ingestion-contract change and an agent change for a list an operator writes once.

**SVG/PNG export.** Rendering is the canvas's job and a screenshot already exists; a picture does
not diff and cannot be edited.

## 5. Tests

| ID | Assertion | Where |
|---|---|---|
| T-16.1 | Each rule fires on its threshold and not one step below; an edge may raise several types | `backend/tests/test_anomalies.py` |
| T-16.2 | Empty baseline: no novelty anomalies, `baseline_observed` false; failure/canary rules still fire | `backend/tests/test_anomalies.py` |
| T-16.3 | Output sorted by edge key then type; unmeasured failures (null) never produce `FAILURE_SPIKE` | `backend/tests/test_anomalies.py` |
| T-16.4 | Canary selectors match on fields with and without kind; graph nodes carry `canary` | `backend/tests/test_anomalies.py`, `test_api.py` |
| T-16.5 | Ingesting an edge to a canary moves the counter and logs; a replayed batch does not | `backend/tests/test_api.py` |
| T-16.6 | `/metrics` exposes every anomaly type gauge, zero included | `backend/tests/test_api.py` |
| T-16.7 | TS and Go Mermaid output equal `cli/testdata/graph.mmd`; hostile labels cannot inject syntax | `frontend/tests/mermaid.test.ts`, `cli/internal/render/` |
| T-16.8 | The UI lists anomalies, marks canaries, and states an empty baseline | `frontend/tests/` |
| T-16.9 | Chart renders `CANARY_WORKLOADS` and the anomaly settings; schema validates them | `scripts/verify-chart.sh` |

## 6. Tracker

- [x] **A16-1** ADR-016, index and plan entry
- [x] **A16-2** Backend: canary matching, `GraphNode.canary`, ingest counter — D-16.4 – D-16.6, T-16.4, T-16.5
- [x] **A16-3** Backend: `anomalies.detect`, route, evaluator, gauges — D-16.1 – D-16.3, T-16.1 – T-16.3, T-16.6
- [x] **A16-4** Contract regenerated; TS client; CLI structs and fixture
- [x] **A16-5** Frontend: anomaly panel, canary marking, Mermaid export — D-16.7, T-16.7, T-16.8
- [x] **A16-6** CLI: `graph -o mermaid`, `anomalies` — D-16.8, T-16.7
- [x] **A16-7** Chart values, schema, ConfigMap; demo canary — T-16.9
- [x] **A16-8** Docs: operator guide, limitations §4.6 – §4.8, README
- [ ] **A16-9** Verification on kind: `make demo-canary` raises `CANARY_CONTACT` in the UI, API, log and gauge
