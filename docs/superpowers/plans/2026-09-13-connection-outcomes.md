# Connection Outcomes Implementation Plan

> **For agentic workers:** Use test-driven development and review each component before integration.

**Goal:** Display failed/aborted TCP setup attempts and measured establishment timing end to end.
**Architecture:** Extend existing observations with additive outcome counters and weighted timing;
track active opens in the BPF collector and retain the current identity and aggregation keys.
**Tech Stack:** C/eBPF, Go, FastAPI/Pydantic, PostgreSQL, React/TypeScript.
**Spec:** ../specs/2026-09-13-connection-outcomes.md

## Global constraints
IPv4 TCP only; no payload capture; successful connection_count semantics unchanged; no errno guesses.
failed_connection_count: nullable nonnegative integer; connect_latency_count and connect_latency_sum_us:
nonnegative integers defaulting to zero. Mean is derived, never averaged across averages.

## Tasks
- [x] Collector and Go agent: add failing decode, aggregation and outcome tests; update BPF to track
  SYN_SENT and terminal transitions; regenerate object using pinned builder; propagate counters
  through collector, aggregation and contract validation. Run go test ./... and privileged collector tests.
- [x] Backend and contracts: add API/storage validation tests and forward SQL migration; sum the three
  new fields in both repositories; expose details and graph metrics; exclude failure-only edges from
  success comparisons. Generate OpenAPI and TypeScript. Run Python tests, Ruff and contract checks.
- [x] Frontend: test words/styles for failed-only edges, unavailable timing, weighted grouped samples;
  render outcomes in graph labels and detail rows; preserve grouping, focus and display budget.
  Run npm test, npm run build and npm run lint.
- [x] Integration: inspect diffs, verify old fixtures, test real kernel capture and PostgreSQL where
  available, update operator/readme limitations, and report concrete checks and remaining limits.


## Verification results

- Go unit suites passed; BPF object regenerated with the pinned local builder.
- Seven privileged collector tests passed, including real success timing, refusal, cancelled
  pending connection, no reverse edge, connection reuse, stats, and no payload in events.
- Backend: 198 tests passed with PostgreSQL enabled, including historical migration.
- Frontend: 87 unit tests passed; production build and lint passed.
- Chromium smoke test passed with controlled API responses: failed-only graph edge, warning
  stroke, setup timing, node details and weighted namespace grouping.
- Generated OpenAPI check and diff whitespace check passed.
- No running Kubernetes deployment was changed; browser smoke used controlled API responses.
