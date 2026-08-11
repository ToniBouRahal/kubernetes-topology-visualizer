# Requirements-to-Tests Checklist

Task ID: `P0-T3` · ADR-008 D-8.4 · ADR-001 §12 instruction 7

Every ADR requirement resolves to **exactly one** of: an automated test, a demonstration step in
`demo-script.md`, or an explicit entry in `limitations.md`. Phase 5 requires all rows to be
resolved (`P5-T15`).

Maintained continuously. Reconstructing this at submission is strictly harder than growing it, and
it is the artifact that lets the report claim coverage with evidence.

Status: ✅ verified · 🔄 in progress · ⬜ not started · 📄 documented limitation

## Phase 0 — foundation and contracts

| Req | Source | Resolved by | Status |
|---|---|---|---|
| Canonical node ID grammar | ADR-001 §5.2, ADR-003 D-3.2 | `contracts/ids.md` §1 + `test_canonical_node_id_grammar`, `TestBuildNodeIDGrammar` | ✅ |
| Exactly six allowed kinds | ADR-003 D-3.2 | `test_allowed_kinds_are_exactly_six`, `TestAllowedKindsAreExactlySix` | ✅ |
| Single `EXTERNAL` node ID | ADR-001 §5.2 | `test_external_node_id_is_a_single_constant` | ✅ |
| IDs are opaque to consumers | ADR-003 D-3.2 | `contracts/ids.md` §2 + `topology-contract` skill | 🔄 enforced from Phase 2 |
| ID segments unambiguous | ADR-003 D-3.2 | `test_node_id_rejects_empty_or_ambiguous_segments`, `TestBuildNodeIDRejectsAmbiguousSegments` | ✅ |
| Edge key tuple | ADR-003 D-3.3 | `test_edge_id_encodes_the_edge_key` | 🔄 four-way equality at Phase 3 (T-3.4) |
| Batch envelope schema | ADR-003 D-3.4 | 20 fixtures + `manifest.json`; T-3.1, T-3.2 | ✅ |
| `202`/`200` idempotency split | ADR-003 D-3.5 | contract declares both; asserted at Phase 2 (T-4.2) | 🔄 |
| Unsupported version → `400` not `422` | ADR-003 D-3.5 | `test_unsupported_version_is_400_not_422` | ✅ |
| Malformed batch → `422` | ADR-003 D-3.5 | `test_malformed_batches_return_422` (17 cases) | ✅ |
| Edge-count limit → `413` | ADR-003 D-3.4 | `test_edge_count_limit_is_413` | ✅ |
| Timestamps RFC 3339 aware | ADR-003 D-3.4 | `batch.invalid-naive-timestamp.json` | ✅ |
| `connection_count` ≥ 1 | ADR-003 D-3.4 | `batch.invalid-connection-count-{zero,negative}.json` | ✅ |
| Bytes absent ≠ zero | ADR-003 D-3.4 | `TestBytesAbsentNotZero` | ✅ |
| OpenAPI is the contract source | ADR-003 D-3.1 | `scripts/export_openapi.py`, `make contracts` | ✅ |
| Committed contract must not drift | ADR-003 §5 | `test_committed_openapi_matches_application` (T-3.6) + CI `contracts` job | ✅ |
| Go/Python round-trip equivalence | ADR-003 T-3.3 | `TestRoundTripValidFixtures` | ✅ |
| Versioned `/api/v1` routes | ADR-001 §5.3 | all 8 routes present in `contracts/openapi.json` | ✅ |
| `helm lint` succeeds | ADR-001 §7 P0 | `scripts/verify-chart.sh` T-7.1 | ✅ |
| `helm template` renders valid YAML | ADR-001 §7 P0 | `scripts/verify-chart.sh` T-7.2 | ✅ |
| `values.schema.json` rejects bad values | ADR-007 D-7.2 | `scripts/verify-chart.sh` T-7.3 (8 cases) | ✅ |
| Agent RBAC get/list/watch only | ADR-007 D-7.3 | `scripts/verify-chart.sh` T-7.4 | ✅ |
| Backend/frontend have no RBAC | ADR-007 D-7.3 | `automountServiceAccountToken: false` assertion | ✅ |
| `CLUSTER_ID` has one source | ADR-007 D-7.5 | `scripts/check_cluster_id.py` | ✅ |
| Three-node kind cluster | ADR-001 §7 P0 | `kind/cluster.yaml` | ✅ |
| CORS never `*` | ADR-004 D-4.6 | schema rejection + `settings.cors_allowed_origins` | ✅ |
| Toolchain versions documented | ADR-001 §7 P0 | `docs/prerequisites.md`, `make tools` | ✅ |
| CI jobs for all components | ADR-001 §7 P0 | `.github/workflows/ci.yml` (6 jobs) | ✅ |

## Phase 1 — eBPF capture and resolution

| Req | Source | Resolved by | Status |
|---|---|---|---|
| Active-open filter, no false reverse edges | ADR-002 D-2.1 | T-2.3 | ⬜ |
| IPv4 TCP event capture | ADR-001 §5.1 | T-2.11 (privileged) | ⬜ |
| Ring buffer, not perf array | ADR-002 D-2.1 | T-2.11 | ⬜ |
| Lost kernel samples exposed | ADR-001 §6 | T-2.12 (privileged) | ⬜ |
| No payload bytes anywhere | ADR-001 §7 P1 | T-2.7 | ⬜ |
| Owner-reference collapse to workload | ADR-002 D-2.4 | T-2.5 | ⬜ |
| EndpointSlice + port → Service | ADR-002 D-2.4 | T-2.6 | ⬜ |
| Ambiguous Service preserved, never guessed | ADR-002 D-2.4 | T-2.6 | ⬜ |
| Replicas collapse to one node | ADR-001 §7 P1 | T-2.5 | ⬜ |
| External summarised, no per-IP nodes | ADR-001 §5.2 | T-2.6 | ⬜ |
| Unresolved ≠ external | ADR-002 D-2.4 | T-2.6 | ⬜ |
| Infrastructure ports filtered | ADR-002 §2 | T-2.8 | ⬜ |
| Ten-second aggregation, first/last seen | ADR-002 D-2.5 | T-2.9 | ⬜ |
| Agent on every node | ADR-001 §7 P1 | T-7.5 | ⬜ |
| Raw IP logging off by default | ADR-001 §6 | code review + `AGENT_DEBUG_RAW_EVENTS` | ⬜ |

## Phase 2 — end-to-end live product

| Req | Source | Resolved by | Status |
|---|---|---|---|
| Idempotent ingestion | ADR-004 D-4.3 | T-4.2, T-4.3 | ⬜ |
| Bounded queue, retry with jitter | ADR-002 D-2.6 | T-2.10 | ⬜ |
| Graceful shutdown flush | ADR-002 D-2.6 | T-2.10 | ⬜ |
| Nodes derived only from in-window edges | ADR-004 D-4.4 | T-4.9 | ⬜ |
| Deterministic ordering | ADR-003 D-3.6 | T-4.10, T-3.8 | ⬜ |
| Truncation indicator | ADR-004 D-4.4 | T-4.8 | ⬜ |
| Traffic visible in browser ≤ 20 s | ADR-001 §7 P2 | T-8.2 | ⬜ |
| Last graph retained on transient failure | ADR-006 D-6.6 | T-6.6 | ⬜ |
| Stale responses cannot overwrite newer | ADR-006 D-6.6 | T-6.10 | ⬜ |
| No stack trace or credential in errors | ADR-004 D-4.6 | T-4.11 | ⬜ |

## Phase 3 — persistence and history

| Req | Source | Resolved by | Status |
|---|---|---|---|
| Transactional idempotent ingest | ADR-005 D-5.3 | T-5.2 – T-5.5 | ⬜ |
| Retries do not double count | ADR-001 §9 | T-5.2, T-5.3 | ⬜ |
| One-minute buckets | ADR-005 D-5.4 | T-5.5 | ⬜ |
| Half-open `[from, to)` boundaries | ADR-005 D-5.4 | T-5.7 | ⬜ |
| Retention + orphan cleanup | ADR-005 D-5.5 | T-5.8 | ⬜ |
| History survives restarts | ADR-001 §9 | T-5.9, T-5.10 | ⬜ |
| Readiness fails on storage failure | ADR-005 D-5.6 | T-5.12 | ⬜ |
| No DSN in logs or errors | ADR-005 D-5.7 | T-5.11 | ⬜ |
| Both repositories behave identically | ADR-005 D-5.1 | T-5.12 | ⬜ |
| Diff NEW/REMOVED/CHANGED deterministic | ADR-003 D-3.7 | T-4.6 | ⬜ |
| Missing period = zero, not null | ADR-003 D-3.7 | T-4.6 | ⬜ |
| Zero-baseline percentage undefined | ADR-003 D-3.7 | T-4.6 | ⬜ |

## Phase 4 — completeness and byte feasibility

| Req | Source | Resolved by | Status |
|---|---|---|---|
| Layout stable across polls | ADR-006 D-6.2 | T-6.2 | ⬜ |
| Readable without colour | ADR-006 D-6.3 | T-6.5 | ⬜ |
| Keyboard reachable, WCAG AA | ADR-006 D-6.7 | T-6.9 | ⬜ |
| Usable at 1280×720 | ADR-001 §5.6 | manual + demo script | ⬜ |
| Intensity metric named explicitly | ADR-006 D-6.4 | T-6.5 | ⬜ |
| Byte accounting delivered **or** documented | ADR-001 §9 | `docs/evaluation/byte-accounting.md` | ⬜ |

## Phase 5 — packaging, validation, handoff

| Req | Source | Resolved by | Status |
|---|---|---|---|
| `make demo-up` on a clean machine | ADR-001 §9 | T-7.10 | ⬜ |
| Multi-node kubeadm validation | ADR-001 §9 | T-7.12 | ⬜ |
| Pod churn does not fragment identity | ADR-001 §9 | T-8.6 | ⬜ |
| `demo-down` is surgical | ADR-007 D-7.6 | T-7.11 | ⬜ |
| Performance targets measured | ADR-001 §6 | `docs/evaluation/` | ⬜ |
| Failure modes actionable | ADR-001 §7 P5 | P5-T17 | ⬜ |
| NetworkPolicy unenforced in kind | ADR-007 D-7.4 | `limitations.md` | 📄 planned |
| Connections ≠ requests | ADR-001 §10 | `limitations.md` | 📄 planned |
| One-minute bucket resolution floor | ADR-005 §8 | `limitations.md` | 📄 planned |
| Retention bounds comparison range | ADR-005 §8 | `limitations.md` | 📄 planned |
| Runtime-only topology misses silent deps | ADR-001 §10 | `limitations.md` | 📄 planned |
| Privileged DaemonSet required | ADR-001 §5.7 | `limitations.md` | 📄 planned |
| No screenshots expose secrets or IPs | ADR-001 §9 | P5-T18 | ⬜ |
