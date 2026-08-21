# Implementation Plan — Master Progress Tracker

- **Status:** Awaiting go-ahead
- **Created:** 2026-08-12
- **Target repository:** `/home/it-laptop/kubernetes-topology-visualizer`
- **Governing ADRs:** [ADR-001](ADR-001-runtime-topology-visualizer.md) §7 phases · [ADR-002](ADR-002-ebpf-node-agent.md) – [ADR-008](ADR-008-testing-ci.md)

## How to use this file

Every task has a stable ID: `P<phase>-<component><n>`.

| Letter | Component | ADR | Component tracker |
|---|---|---|---|
| `E` | Environment prerequisites | — | this file |
| `S` | Skills, plugins, hooks | [README](README.md) | this file |
| `A` | eBPF node agent | [ADR-002](ADR-002-ebpf-node-agent.md) | §9 |
| `C` | Ingestion & API contract | [ADR-003](ADR-003-ingestion-contract.md) | §8 |
| `B` | Backend service | [ADR-004](ADR-004-backend-service.md) | §9 |
| `D` | Persistence | [ADR-005](ADR-005-persistence-postgresql.md) | §9 |
| `F` | Frontend | [ADR-006](ADR-006-frontend-topology-ui.md) | §9 |
| `K` | Packaging & deployment | [ADR-007](ADR-007-packaging-deployment.md) | §9 |
| `T` | Testing & CI | [ADR-008](ADR-008-testing-ci.md) | §8 |

The same IDs appear in each component ADR's tracker section. Ticking a box here and in the
component ADR keeps both views true. **Mark a task `[x]` only when its named test or gate passes** —
ADR-001 §12 instruction 9.

Status markers: `[ ]` not started · `[~]` in progress · `[x]` done · `[!]` blocked · `[-]` dropped
with a recorded reason.

> **Current position:** Phase 1 **complete** — gate passed 2026-08-17, `docs/evaluation/phase-1.md`.
> All seven acceptance criteria demonstrated on a live three-node kind cluster.
> **Current position:** Phase 3 **complete** — gate passed 2026-08-21, `docs/evaluation/phase-3.md`.
> PostgreSQL persistence, historical windows, and compare mode all demonstrated on the cluster.
> Next: Phase 4 — `P4-F12` detail panels, then the byte-accounting spike.
> Next: Phase 3 — `P3-D1` migrations, then `P3-D2` the ingest transaction.

### Delegation legend

`→ codex` marks tasks handed to the Codex subagent via `/codex:rescue`. These are chosen because
they are self-contained, have crisp acceptance tests, or benefit from an independent second
derivation. `→ chrome` marks work that needs live browser debugging (user runs `/chrome` first).

---

## Phase 0 — Repository foundation and contracts

**ADR-001 §7 Phase 0.** Gate: `make lint` and `make test` succeed · `helm lint` passes ·
`helm template` renders valid YAML · contract fixtures validate · prerequisites documented.

### Environment

- [x] **P0-E1** Install Go 1.23+ on the host — [README](README.md) §2
- [x] **P0-E2** Upgrade Node to 22 LTS (currently 18.20.8, EOL) — [README](README.md) §2
- [x] **P0-E3** Install `golangci-lint` and `uv` — [README](README.md) §2
- [x] **P0-E4** Upgrade kind (0.22.0 → current) for the three-node cluster — [README](README.md) §2
- [x] **P0-E5** Record every tool version in `docs/prerequisites.md`, including Helm 4.2.3 — ADR-008 D-8.3

### Skills, plugins, hooks

- [x] **P0-S1** Install plugins: `context7`, `gopls-lsp`, `pyright-lsp`, `typescript-lsp`, `frontend-design`, `playwright`, `hookify`, `skill-creator`, `github` — [README](README.md) §3
- [x] **P0-S2** Author `topology-contract` skill — ADR-003 §4
- [x] **P0-S3** Author `ebpf-agent-dev` skill — ADR-002 §5
- [x] **P0-S4** Author `k8s-demo-loop` skill — ADR-007 §5
- [x] **P0-S5** Author `phase-gate` skill — ADR-008 §4
- [x] **P0-S6** Author `adr-guard` skill — [README](README.md) §5
- [x] **P0-S7** Install hooks: write-scope guard, openapi→TS regeneration, formatter, privacy warn — [README](README.md) §8

### Repository and contracts

- [x] **P0-K1** `git init` the target repo with the ADR-001 §5.8 directory structure — ADR-007
- [x] **P0-K2** Copy ADR-001, the source-of-truth doc, and ADR-002–008 into `docs/` and `docs/adr/`
- [x] **P0-T1** `Makefile` with pinned toolchain and `lint`/`test`/`contracts`/`verify` targets — ADR-008 D-8.2
- [x] **P0-C1** Write `contracts/ids.md`: ID grammar, six kinds, EXTERNAL ID, edge key, opacity rule — ADR-003 D-3.2, D-3.3
- [x] **P0-C2** Pydantic v2 models for batch, graph, and diff — ADR-003 D-3.4, D-3.6, D-3.7
- [x] **P0-C3** `make contracts` publishes `contracts/openapi.json` — ADR-003 D-3.1
- [x] **P0-C4** Valid + invalid fixtures, one per rule in D-3.4 — ADR-003 D-3.9
- [x] **P0-C5** Fixture validation tests — test T-3.1, T-3.2
- [x] **P0-C6** Go struct round-trip test against `batch.valid.json` — test T-3.3
- [x] **P0-K3** Helm chart skeleton that lints and templates — ADR-007 D-7.1, tests T-7.1, T-7.2
- [x] **P0-K4** `kind/cluster.yaml`, three nodes — ADR-007 D-7.1
- [x] **P0-T2** CI jobs: Go, Python, frontend, Helm, images, contract drift — ADR-008 D-8.3, test T-3.6
- [x] **P0-T3** Scaffold `docs/requirements-checklist.md` — ADR-008 D-8.4
- [x] **P0-T4** Phase 0 gate: run and record in `docs/evaluation/phase-0.md` — ADR-008 D-8.5

---

## Phase 1 — Feasibility: eBPF capture and Kubernetes resolution

**ADR-001 §7 Phase 1.** Gate: real workloads produce captured events · service-level edges ·
replicas collapse · external summarised · no false reverse edges · no payload bytes · agent on every
node.

- [x] **P1-A1** Generate and commit `bpf/vmlinux.h` from BTF — ADR-002 §4
- [x] **P1-A2** BPF program: four-condition filter incl. `oldstate == TCP_SYN_SENT` — ADR-002 D-2.1, test T-2.3
- [x] **P1-A3** Switch to `BPF_MAP_TYPE_RINGBUF` — ADR-002 D-2.1 (C2)
- [x] **P1-A4** Lost-event counter map incremented on failed `bpf_ringbuf_reserve` — ADR-002 D-2.2, test T-2.12
- [x] **P1-A5** `bpf2go` build in the pinned container, `/usr/include/asm` symlink preserved — ADR-002 §4
- [x] **P1-A6** Go ring-buffer reader; addresses stay `[4]byte` end to end — ADR-002 D-2.1 (C4), tests T-2.1, T-2.2
- [x] **P1-A7** Lost-counter poller exports `kernel_samples_lost` — ADR-002 D-2.2
- [x] **P1-A8** Informers for all nine resource types; source Pod informer node-scoped, destinations cluster-wide — ADR-002 D-2.3
- [x] **P1-A9** Source resolution: owner-reference walk, Pod→ReplicaSet→Deployment collapse — ADR-002 D-2.4, test T-2.5
- [x] **P1-A10** Destination resolution: ClusterIP → EndpointSlice+port → ambiguous → workload → host → EXTERNAL → unresolved — ADR-002 D-2.4, test T-2.6
- [x] **P1-A11** Canonical ID construction — ADR-003 D-3.2 · **must load `topology-contract`**
- [x] **P1-A12** Infrastructure port filtering from `INFRASTRUCTURE_PORTS` — ADR-002 §2, test T-2.8
- [x] **P1-A13** Ten-second aggregation with `first_seen`/`last_seen` — ADR-002 D-2.5, test T-2.9
- [x] **P1-A14** Structured batch logging; raw IPs gated behind `AGENT_DEBUG_RAW_EVENTS` — ADR-002 D-2.7 (C9)
- [x] **P1-A15** Agent Dockerfile, two-stage — ADR-002 §4
- [x] **P1-A16** Unit tests T-2.1 – T-2.9 — ADR-002 §6 · **→ codex** (self-contained, fixture-driven)
- [x] **P1-K1** Agent DaemonSet, ServiceAccount, least-privilege RBAC, tolerations for control-plane — ADR-007 D-7.3, test T-7.4
- [x] **P1-K2** Deploy to three-node kind; verify a pod on every node — test T-7.5
- [x] **P1-T1** Privileged eBPF tests T-2.11, T-2.12 with documented local command — ADR-008 D-8.1
- [x] **P1-T2** Phase 1 gate: run and record in `docs/evaluation/phase-1.md`

> **Codex delegation for this phase:** if the verifier rejects the BPF program, hand the full
> verifier log to `/codex:rescue` before iterating blindly — ADR-002 §5. Verifier errors are the
> single best use of an independent second model in this project.

---

## Phase 2 — End-to-end live product

**ADR-001 §7 Phase 2.** Gate: traffic in the browser within 20 s · duplicate `batch_id` counted
once · backend outage causes retries not termination · graph shows direction/port/count/first/last ·
deterministic filters · transient failure retains last graph · smoke test passes.

### Backend

- [x] **P2-B1** API/domain/persistence layering, settings, app factory — ADR-004 D-4.1
- [x] **P2-B2** Structured JSON logging with request IDs — ADR-004 D-4.6
- [x] **P2-B3** `RepositoryProtocol` + `InMemoryRepository` with real semantics — ADR-005 D-5.1
- [x] **P2-B4** `POST /api/v1/ingest/batches` with 202/200/400/422/413 — ADR-004 D-4.3, tests T-4.1, T-4.2
- [x] **P2-B5** `GET /api/v1/graph`: presets, filters, deterministic order, truncation — ADR-004 D-4.4, tests T-4.7 – T-4.10
- [x] **P2-B6** `/api/v1/namespaces`, `/nodes/{id}`, `/edges/{id}` — ADR-004 D-4.7, test T-4.13
- [x] **P2-B7** `/health/live`, `/health/ready`, `/metrics`, CORS from settings, error handlers, graceful shutdown — ADR-004 D-4.6, tests T-4.11, T-4.12

### Agent delivery

- [x] **P2-A17** ULID `batch_id` + immutable batch handoff (fixes prototype C7) — ADR-002 D-2.6
- [x] **P2-A18** Bounded queue, drop-oldest, `batches_dropped` metric — ADR-002 D-2.6, test T-2.10
- [x] **P2-A19** Retry with exponential backoff and jitter; treat 200 as success — ADR-002 D-2.6
- [x] **P2-A20** Graceful shutdown with final flush and bounded drain — ADR-002 D-2.6
- [x] **P2-A21** `/healthz`, `/readyz`, `/metrics` on the agent — ADR-002 D-2.7

### Frontend

- [x] **P2-F1** Vite + TypeScript scaffold; generated client from `openapi.json` — ADR-006 D-6.1
- [x] **P2-F2** React Flow canvas + Dagre layout (verify `@xyflow/react` vs `reactflow` first) — ADR-006 §5
- [x] **P2-F3** Polling with `AbortController`; retain last good graph on error — ADR-006 D-6.6 (F7, F8), tests T-6.6, T-6.10
- [x] **P2-F4** Time presets, namespace filter, search — ADR-006 D-6.5, test T-6.3
- [x] **P2-F5** EXTERNAL node with non-colour cues — ADR-006 D-6.3
- [x] **P2-F6** Node and edge detail panel — ADR-006 D-6.6, test T-6.8
- [x] **P2-F7** Loading, empty, error states — ADR-006 D-6.6, test T-6.7

### Deployment and tests

- [x] **P2-K3** Backend + frontend Deployments, Services, ConfigMaps, `NOTES.txt` — ADR-007 §4
- [x] **P2-T3** Backend API tests T-4.1 – T-4.13 against the in-memory repo — **→ codex** (crisp fixtures, high volume)
- [x] **P2-T4** Frontend unit/component tests T-6.1, T-6.3, T-6.6 – T-6.8
- [x] **P2-T5** Playwright E2E: expected demo edges visible within 20 s — tests T-8.1, T-8.2 · **→ chrome** for first bring-up debugging
- [x] **P2-T6** Phase 2 gate: run and record in `docs/evaluation/phase-2.md`

---

## Phase 3 — Production data model and historical topology

**ADR-001 §7 Phase 3.** Gate: restarts preserve history without inflation · presets and custom
ranges · controlled change appears as NEW/REMOVED · threshold change appears as CHANGED with visible
calculation · database failure fails readiness without leaking credentials · deterministic at bucket
boundaries.

- [x] **P3-D1** Migrations: three tables, CHECK on `kind`, foreign keys, four indexes — ADR-005 D-5.2, D-5.6, test T-5.1
- [x] **P3-D2** `PostgresRepository.ingest_batch` — the single transaction — ADR-005 D-5.3, tests T-5.2 – T-5.5 · **→ codex** (independent derivation of the upsert, then diff against mine)
- [x] **P3-D3** Bucketing in the domain layer; half-open `[from, to)` everywhere — ADR-005 D-5.4, test T-5.7
- [x] **P3-D4** Query methods: graph, details, namespaces — ADR-005 D-5.1, test T-5.13
- [x] **P3-D5** Retention task with batched deletes — ADR-005 D-5.5, test T-5.8
- [x] **P3-D6** Readiness on migrations + storage; DSN sanitisation everywhere — ADR-005 D-5.7, test T-5.11
- [x] **P3-D7** Contract suite passing against **both** repositories — ADR-005 D-5.1, test T-5.12
- [ ] **P3-K4** PostgreSQL in the chart: StatefulSet/subchart, PVC, Secret — ADR-007 D-7.2, tests T-7.7, T-7.8
- [ ] **P3-K5** Verify the database image pulls on a clean machine — ADR-007 D-7.2
- [x] **P3-B8** Custom `from`/`to` with span and inversion validation — ADR-004 D-4.4, test T-4.5
- [x] **P3-B9** `GET /api/v1/diff` with NEW/REMOVED/CHANGED and visible calculation — ADR-004 D-4.5, test T-4.6 · **→ codex** (pure function, boundary-heavy, ideal for independent implementation against the same tests)
- [x] **P3-F8** History picker: presets + custom range — ADR-006 D-6.5, test T-6.4
- [x] **P3-F9** Compare mode with non-colour diff cues — ADR-006 D-6.3, test T-6.5
- [x] **P3-T7** Persistence, restart, boundary, retention, diff tests — tests T-5.9, T-5.10, T-8.4, T-8.5
- [x] **P3-T8** Phase 3 gate: run and record in `docs/evaluation/phase-3.md`

---

## Phase 4 — Product completeness and traffic-volume feasibility

**ADR-001 §7 Phase 4.** Gate: usable at 1280×720 · keyboard reachable · comparison readable without
colour · thickness uses a named metric · byte decision backed by a reproducible experiment · detail
views complete · frontend tests in CI.

- [ ] **P4-F10** Detail panels: incoming/outgoing dependencies, ports, counts, timestamps — ADR-006 D-6.6, test T-6.8
- [ ] **P4-F11** Layout position cache; stable across polls — ADR-006 D-6.2, test T-6.2
- [ ] **P4-F12** Accessibility pass: keyboard, focus rings, WCAG AA contrast — ADR-006 D-6.7, test T-6.9 · **→ chrome** (live contrast and focus-order inspection)
- [ ] **P4-F13** Legend, truncation banner, capped-log intensity with the metric named — ADR-006 D-6.4
- [ ] **P4-A22** Bounded byte-accounting spike on a separate branch — ADR-002 D-2.8 · **→ codex** (bounded, well-specified investigation)
- [ ] **P4-X1** **Decision gate** — if reliable: propagate nullable bytes through collection, ingest, PostgreSQL, graph/diff, UI, tests. If not: keep connection count and document the failure mode.
- [ ] **P4-T9** `docs/evaluation/byte-accounting.md` — method, results, decision, either outcome — ADR-008 D-8.6
- [ ] **P4-T10** Frontend component tests running in CI — ADR-008 D-8.3
- [ ] **P4-T11** Phase 4 gate: run and record in `docs/evaluation/phase-4.md`

---

## Phase 5 — Helm packaging, multi-node validation, and FYP handoff

**ADR-001 §7 Phase 5.** Gate: `make demo-up` on a clean machine · `demo-traffic`/`demo-change`
produce expected topology and diff · PVC history survives pod recreation · agent reports from every
node in kind **and** kubeadm · `demo-down` is surgical · all requirements mapped · measured results
support or reject each target · CI passes from clean checkout · failure modes actionable · no
secrets or external IPs in media.

- [ ] **P5-K6** Complete chart: probes, resource limits, security contexts, NetworkPolicies, non-root + read-only rootfs — ADR-007 D-7.4, test T-7.6
- [ ] **P5-K7** Make targets: `demo-up`, `demo-traffic`, `demo-change`, `demo-verify`, `demo-down` — ADR-007 D-7.6, tests T-7.10, T-7.11
- [ ] **P5-K8** Demo workloads across ≥2 namespaces + the controlled change scenario — ADR-007 D-7.7
- [ ] **P5-K9** Multi-node kubeadm validation with the same chart — ADR-007 D-7.8, test T-7.12
- [ ] **P5-K10** Pin all images by version or digest; scan and triage — ADR-007 D-7.4, ADR-008 D-8.7
- [ ] **P5-T12** Experiments: correctness, load, resource, restart, retry, pod churn, history, comparison — ADR-008 D-8.6, test T-8.6
- [ ] **P5-T13** Raw results + environment details under `docs/evaluation/` — ADR-008 D-8.6
- [ ] **P5-T14** Architecture and sequence diagrams, `limitations.md`, operator guide, troubleshooting, `demo-script.md` — **`artifact-diagramming` permitted here only** — ADR-008 §4
- [ ] **P5-T15** `docs/requirements-checklist.md` complete: every ADR requirement → test, demo step, or documented limitation — ADR-008 D-8.4
- [ ] **P5-T16** CI green from a clean checkout; full demo from committed instructions only — ADR-008 D-8.3
- [ ] **P5-T17** Actionable failure messages: missing BTF, denied BPF permissions, backend outage, PostgreSQL failure, empty graph — ADR-001 §7
- [ ] **P5-T18** Privacy check on all screenshots and recordings — ADR-008 D-8.7
- [ ] **P5-T19** Security review of the full branch — `/security-review` + `claude-security`
- [ ] **P5-K11** Tag the submission release — ADR-008 §4
- [ ] **P5-T20** Phase 5 gate: run and record in `docs/evaluation/phase-5.md`

---

## Definition of Done cross-check

Tick only when the corresponding ADR-001 §9 item is demonstrable, not merely implemented.

- [ ] Clean kind deployment from committed Helm and Make commands — P5-K7, P5-T16
- [ ] Validated on multi-node kubeadm — P5-K9
- [ ] Agent observes real TCP without app changes or sidecars — P1-T2
- [ ] Pod churn does not fragment workload identity — P5-T12 / test T-8.6
- [ ] Service destinations resolved via EndpointSlices and ports — P1-A10 / test T-2.6
- [ ] PostgreSQL persists history across backend and database restarts — P3-T7 / tests T-5.9, T-5.10
- [ ] Agent retries do not double-count — P3-D2 / tests T-5.2, T-5.3
- [ ] UI supports presets, custom history, comparison, filters, details — P3-F8, P3-F9, P4-F10
- [ ] Controlled changes classified deterministically — P3-B9 / test T-4.6
- [ ] Byte volume delivered end to end **or** documented with evidence — P4-X1, P4-T9
- [ ] Automated tests validate the expected demo topology — P2-T5 / tests T-8.1, T-8.3
- [ ] Metrics and logs expose collection or delivery failure — P1-A7, P2-A21, P2-B7
- [ ] Security and privacy constraints documented and enforced — P5-T18, P5-T19
- [ ] Known limitations stated honestly in docs and report — P5-T14
