# ADR-008: Testing, CI, Evidence, and Phase Gates

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §7 (phases), §8 (test matrix), §9 (definition of done), §12 (instructions 4, 7, 9)
- **Component path:** `.github/workflows/`, `Makefile`, `*/tests/`, `docs/evaluation/`
- **Owning phases:** all

## 1. Scope

The cross-cutting quality system: the test taxonomy, CI structure, the phase gate procedure, the
requirements-to-tests checklist, and the evaluation evidence that the FYP report depends on.
Component-level tests are specified in ADR-002 through ADR-007; this ADR decides how they run, when
they gate, and what evidence they leave behind.

## 2. Context

ADR-001 §12 makes three procedural demands that no individual component owns:

- instruction 4 — implement phases in order, stop advancing when acceptance criteria fail;
- instruction 7 — maintain a requirements-to-tests checklist as work progresses;
- instruction 9 — never report a phase complete without running its tests and recording the commands.

The prototype has no automated tests of any kind (ADR-001 §3). Every test in this project is new.
The risk is not that tests are hard to write — it is that under time pressure a phase gets declared
complete without them, and the Definition of Done in §9 quietly becomes unverifiable.

## 3. Decisions

### D-8.1 — Four test tiers

| Tier | Runs | Needs | Where |
|---|---|---|---|
| **Unit** | every commit | nothing | Go `internal/*`, Python `domain/` + in-memory repo, frontend units |
| **Integration** | every commit | containers (PostgreSQL) | backend + PostgreSQL, contract suite against both repositories |
| **Cluster E2E** | on demand + before every phase gate | kind | full stack, real traffic, Playwright assertions |
| **Privileged eBPF** | on demand + before release | root + kernel | ring-buffer capture, lost-sample counter |

ADR-001 §8 permits separating privileged eBPF tests from ordinary CI, but requires a documented local
command and a run before the final release. The same applies to cluster E2E: keep it out of the
per-commit path, but never out of the phase gate.

### D-8.2 — `make` is the single entry point

```make
lint         golangci-lint + ruff + eslint + helm lint
test         unit + integration
test-unit    no containers, no cluster — must stay fast
test-e2e     kind-based end-to-end
test-ebpf    privileged, documented, local
contracts    regenerate openapi.json + TS client, fail on drift
verify       lint + test + contracts — what CI runs
```

CI calls the same targets a developer calls. A CI job with its own inline command sequence drifts
from local behaviour and eventually passes something a developer cannot reproduce.

### D-8.3 — CI jobs

Go (build, vet, lint, unit) · Python (ruff, pyright, unit, integration with a PostgreSQL service) ·
Frontend (typecheck, lint, unit/component, build) · Contracts (regenerate and fail on drift — ADR-003
T-3.6) · Helm (lint, template, manifest schema validation) · Images (build all three; Phase 5 adds
scan and triage).

Pin toolchain versions in CI to match the documented local prerequisites, including **Helm 4.2.3**
(ADR-007). A CI/local Helm mismatch produces template output that lints in one place and not the
other.

Phase 5 requires CI to pass **from a clean checkout** — no cached state, no locally generated file
that is not committed.

### D-8.4 — The requirements-to-tests checklist

`docs/requirements-checklist.md`, maintained continuously, one row per ADR requirement:

```text
| Req ID | Source (ADR §) | Test ID / demo step / documented limitation | Status |
```

Every requirement resolves to exactly one of: an automated test, a demonstration step in
`docs/demo-script.md`, or an explicit entry in `docs/limitations.md`. Phase 5 requires that *all*
ADR requirements map this way — building the map incrementally is far cheaper than reconstructing it
at submission, and it is also the artifact that makes the FYP report defensible.

### D-8.5 — The phase gate procedure

A phase is complete only when, in order:

1. every acceptance criterion in ADR-001 §7 for that phase has a passing test or a recorded
   demonstration;
2. `make verify` passes from a clean checkout;
3. cluster E2E passes for phases that touch the deployed system (2 onward);
4. the exact commands and their output are recorded in `docs/evaluation/phase-N.md`;
5. the requirements checklist is updated;
6. the repository is left runnable — ADR-001 §7 requires this of *every* phase.

If a criterion fails, work does not advance. Report the failure and what it blocks; do not
compensate by starting the next phase's deliverables.

### D-8.6 — Evaluation evidence

`docs/evaluation/` holds environment details and raw results for: correctness, load, resource usage,
restart, retry, pod churn, history, and comparison experiments, plus the Phase 4 byte-accounting
spike outcome whichever way it lands.

Every performance target in ADR-001 §6 needs a repeatable script and captured results — 1,000
events/s/node, agent memory under 256 MiB, 500 nodes / 2,000 edges under 500 ms p95, no UI freeze
over 100 ms. Phase 5 requires measured results to **support or explicitly reject** each target.
Rejecting a target with evidence is an acceptable, honest outcome; leaving one unmeasured is not.

### D-8.7 — Security and privacy verification

Before release: dependency and image scan with triaged results; a check that no screenshot or
recording exposes a secret or an individual external IP (ADR-001 §6, §9); confirmation that raw
event logging is off by default; and confirmation that error responses carry no stack traces or
credentials.

## 4. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`phase-gate`** (custom — spec below) | At every phase boundary, without exception | Turns instructions 4, 7, and 9 into a procedure that actually runs, rather than three sentences in an ADR that get skipped when a phase feels finished. |
| **`playwright`** | Phase 2 onward | Owns the cluster E2E assertions: demo edges visible in the browser, controlled change visible in compare mode (ADR-006 T-6.11, T-6.12). |
| **`github`** | Phase 0 (workflows), Phase 5 (release) | CI authoring and the submission release tag. `/plugin install github@claude-plugins-official` |
| **`/code-review`** (built-in) | End of every phase, on that phase's diff | Use `/code-review high` for Phases 1–3 where correctness risk is concentrated. `/code-review ultra` is available for a deep multi-agent pass at a phase boundary — **you** trigger it; it is user-invoked and billed. |
| **`/security-review`** (built-in) | Before Phase 5 sign-off | Branch-level security review of the pending changes. |
| **`k8s-demo-loop`** (ADR-007) | Every E2E run | The gate's cluster steps must be the same steps the Makefile runs. |

### Situational

- **`claude-security`** — a deeper, tiered vulnerability sweep for the Phase 5 scan-and-triage
  deliverable, complementing the per-branch `/security-review`. Install only when Phase 5 starts.
- **`artifact-diagramming`** (built-in) — **permitted here and only here**: architecture and sequence
  diagrams for `docs/architecture.md` (Phase 5 deliverable). This is documentation, not the product
  UI. ADR-006 forbids artifact skills in `frontend/`.
- **`/simplify`** — after a phase passes, never before. Refactoring code that has not yet met its
  acceptance criteria makes failures harder to attribute.
- **`Explore` subagent** — for locating requirement coverage gaps across a repository that will span
  three languages plus charts.

### Do not use

- **`ralph-loop`** — the phase gates here are a stricter and more auditable control loop; a
  self-referential loop would blur exactly the boundaries D-8.5 exists to enforce.
- **`code-review` / `pr-review-toolkit` / `code-simplifier` plugins** — redundant with the built-in
  `/code-review` and `/simplify`.
- **`session-report`** — reports Claude Code session usage, not project metrics. It is not evidence
  for `docs/evaluation/`.
- **`/loop`, `/schedule`** — no recurring automation is in scope; phase gates are event-driven.

### `phase-gate` skill specification

```yaml
name: phase-gate
description: >
  Verify and record completion of an ADR-001 implementation phase. Load before declaring any
  phase complete, before starting the next phase, and when asked whether the project can
  advance.
```

Body must contain:

1. **The acceptance criteria for each phase 0–5**, copied verbatim from ADR-001 §7, as a checklist.
2. **The exact commands** to run per phase, and the rule that output is recorded in
   `docs/evaluation/phase-N.md` — a claim of completion without recorded commands is not completion
   (instruction 9).
3. **The stop rule**: if any criterion fails, report the failure and what it blocks. Do not start
   the next phase. Do not narrow the criterion to make it pass.
4. **The checklist update step** for `docs/requirements-checklist.md` (D-8.4).
5. **The runnable-repository check**: the phase must end with a working `make demo-up` (from
   Phase 2 onward) — ADR-001 §7 requires every phase to leave the repository runnable and tested.
6. **The honesty rule**: report which tests ran and which did not. Privileged eBPF and cluster E2E
   tests that were skipped must be named as skipped, not silently omitted.

## 5. Test matrix ownership

ADR-001 §8's eight rows map onto the component ADRs:

| §8 row | Owning ADR | Test IDs |
|---|---|---|
| eBPF | ADR-002 | T-2.1 – T-2.4, T-2.7, T-2.11, T-2.12 |
| Agent domain | ADR-002 | T-2.5, T-2.6, T-2.8, T-2.9 |
| Agent delivery | ADR-002 | T-2.10 |
| Backend API | ADR-004 | T-4.1 – T-4.13 |
| PostgreSQL | ADR-005 | T-5.1 – T-5.13 |
| Frontend | ADR-006 | T-6.1 – T-6.10 |
| Kubernetes | ADR-007 | T-7.1 – T-7.12 |
| End-to-end | this ADR | T-8.1 – T-8.6 below |
| Contract (added) | ADR-003 | T-3.1 – T-3.8 |

### End-to-end tests owned here

| ID | Test |
|---|---|
| T-8.1 | Known demo traffic produces exactly the expected directed edges, at service level, in the graph API |
| T-8.2 | The same edges are visible in the browser within 20 s (Playwright) |
| T-8.3 | `demo-change` produces the expected `NEW` edge in a compare query and in the UI |
| T-8.4 | A backend outage during traffic generation loses no committed edges and inflates no counts after recovery |
| T-8.5 | Backend and database pod restarts preserve history (crosses ADR-005 T-5.9/T-5.10 at full-stack level) |
| T-8.6 | Pod churn — deleting and rescheduling a demo workload pod — does not fragment workload identity in the graph |

T-8.6 deserves emphasis: it is a Definition-of-Done item in ADR-001 §9 and the clearest single
demonstration that runtime identity resolution works, because it is exactly what a manifest-derived
tool cannot do.

## 6. Acceptance criteria

Phase 0: `make lint` and `make test` succeed on the skeletons; CI jobs exist for Go, Python,
frontend, Helm, and images; contract examples validate; prerequisites documented.

Phases 1–4: each phase's §7 criteria verified and recorded per D-8.5.

Phase 5: CI passes from a clean checkout; the complete demo succeeds using only committed
instructions; all ADR requirements map to a test, demo step, or documented limitation; measured
results support or explicitly reject each performance target; final screenshots and recordings
expose no secrets or individual external IPs.

## 7. Consequences

- **Gates cost time at each boundary.** That is the trade ADR-001 §12 instruction 4 already made: an
  unverified phase compounds into the next one, and the compounding is discovered at the demo.
- **Privileged and cluster tests cannot run in ordinary CI.** They therefore need a documented local
  command and a recorded pre-release run — otherwise the highest-risk code is the least tested.
- **The requirements checklist is real ongoing work.** It is also the artifact that lets the FYP
  report claim coverage with evidence, and reconstructing it at the end is strictly harder.
- **Honest negative results are deliverables.** A rejected performance target or a failed
  byte-accounting spike, documented with method and evidence under `docs/evaluation/`, satisfies
  ADR-001. An unmeasured claim does not.

## 8. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). This tracker owns the **gates themselves** —
a phase is not complete until its gate row is ticked here.

### Phase 0 — foundation

- [x] **P0-T1** `Makefile` with pinned toolchain and `lint`/`test`/`contracts`/`verify` — D-8.2
- [x] **P0-T2** CI jobs: Go, Python, frontend, Helm, images, contract drift — D-8.3
- [x] **P0-T3** Scaffold `docs/requirements-checklist.md` — D-8.4
- [x] **P0-E5** `docs/prerequisites.md` with every tool version incl. Helm 4.2.3 — D-8.3
- [x] **P0-S5** Author the `phase-gate` skill — §4
- [x] **GATE 0** `make lint` + `make test` pass · `helm lint` · `helm template` · fixtures validate · prerequisites documented → record in `docs/evaluation/phase-0.md`

### Phase 1 — feasibility

- [x] **P1-T1** Privileged eBPF tests with a documented local command — D-8.1
- [ ] **GATE 1** captured events · service-level edges · replicas collapse · external summarised · no false reverse edges · no payload bytes · agent on every node → `docs/evaluation/phase-1.md`

### Phase 2 — end-to-end

- [ ] **P2-T3** Backend API suite — ADR-004 §6 · **→ codex**
- [ ] **P2-T4** Frontend unit/component suite — ADR-006 §6
- [ ] **P2-T5** Playwright E2E: T-8.1, T-8.2 · **→ chrome** for first bring-up
- [ ] **GATE 2** browser within 20 s · duplicate `batch_id` counted once · outage causes retries not termination · deterministic filters · last graph retained on failure · smoke test passes → `docs/evaluation/phase-2.md`

### Phase 3 — data model and history

- [ ] **P3-T7** Persistence, restart, boundary, retention, diff tests — T-5.9, T-5.10, T-8.4, T-8.5
- [ ] **GATE 3** restarts preserve history without inflation · presets + custom ranges · NEW/REMOVED/CHANGED with visible calculation · database failure fails readiness cleanly · deterministic at bucket boundaries → `docs/evaluation/phase-3.md`

### Phase 4 — completeness and feasibility

- [ ] **P4-T9** `docs/evaluation/byte-accounting.md` — method, results, decision, either outcome — D-8.6
- [ ] **P4-T10** Frontend component tests running in CI — D-8.3
- [ ] **GATE 4** 1280×720 usable · keyboard reachable · comparison readable without colour · named intensity metric · byte decision backed by a reproducible experiment · details complete → `docs/evaluation/phase-4.md`

### Phase 5 — handoff

- [ ] **P5-T12** Experiments: correctness, load, resource, restart, retry, pod churn, history, comparison — D-8.6 · test T-8.6
- [ ] **P5-T13** Raw results + environment details under `docs/evaluation/` — D-8.6
- [ ] **P5-T14** Architecture + sequence diagrams, limitations, operator guide, troubleshooting, demo script — `artifact-diagramming` permitted here only
- [ ] **P5-T15** `docs/requirements-checklist.md` complete: every requirement → test, demo step, or documented limitation — D-8.4
- [ ] **P5-T16** CI green from a clean checkout; demo succeeds from committed instructions only — D-8.3
- [ ] **P5-T17** Actionable failure messages: missing BTF, denied BPF permissions, backend outage, PostgreSQL failure, empty graph
- [ ] **P5-T18** Privacy check on all screenshots and recordings — D-8.7
- [ ] **P5-T19** `/security-review` + `claude-security` sweep; dependency and image scan triaged — D-8.7
- [ ] **GATE 5** every Phase 5 criterion in ADR-001 §7 → `docs/evaluation/phase-5.md`

### End-to-end tests owned here

- [ ] **T-8.1** Known traffic produces exactly the expected service-level directed edges
- [ ] **T-8.2** The same edges visible in the browser within 20 s
- [ ] **T-8.3** `demo-change` produces the expected `NEW` edge in compare, API and UI
- [ ] **T-8.4** Backend outage during traffic loses no committed edges and inflates no counts
- [ ] **T-8.5** Backend and database pod restarts preserve history
- [ ] **T-8.6** Pod churn does not fragment workload identity

### Standing rule

- [ ] No phase is reported complete without its commands recorded and its skipped tests named — ADR-001 §12 instruction 9
