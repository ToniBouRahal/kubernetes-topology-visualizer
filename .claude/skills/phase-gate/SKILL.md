---
name: phase-gate
description: >
  Verify and record completion of an ADR-001 implementation phase. Load before declaring any phase
  complete, before starting the next phase, and when asked whether the project can advance.
---

# Phase gate

ADR: `docs/adr/ADR-008-testing-ci.md` D-8.5. Tracker: `docs/IMPLEMENTATION-PLAN.md`.

## The rule

A phase is complete only when **all** of these hold, in order:

1. every acceptance criterion in ADR-001 §7 for that phase has a passing test or a recorded demonstration
2. `make verify` passes from a clean checkout
3. cluster E2E passes (phases 2+)
4. the exact commands **and their output** are recorded in `docs/evaluation/phase-N.md`
5. `docs/requirements-checklist.md` is updated
6. the repository is left runnable

## The stop rule

If any criterion fails: **report the failure and what it blocks. Do not start the next phase.**
Do not narrow a criterion so it passes. Do not compensate by delivering later work early.

## The honesty rule

Report which tests ran and which did not. Privileged eBPF tests and cluster E2E tests that were
skipped must be **named as skipped**, never silently omitted. ADR-001 §12 instruction 9: never
report a phase complete without running its tests and recording the commands used.

## Acceptance criteria by phase

### Phase 0 — foundation
- `make lint` and `make test` succeed on the service skeletons
- `helm lint charts/topology-visualizer` succeeds
- `helm template topology charts/topology-visualizer -f charts/topology-visualizer/ci/kind-values.yaml` renders valid YAML
- contract examples validate against the API schema
- all supported tool versions and local prerequisites documented

### Phase 1 — feasibility
- real unmodified demo workloads produce captured IPv4 TCP events
- `Frontend → Backend` and `Backend → Redis` become service-level edges
- multiple replicas collapse to one logical node and edge
- external traffic becomes `source → EXTERNAL`, never one node per IP
- accepted server sockets create no false reverse edges
- no payload bytes in BPF maps, Go structs, logs, or output
- the agent runs on every node of the three-node kind cluster

### Phase 2 — end-to-end
- traffic observed by eBPF appears in the browser within 20 seconds
- posting the same `batch_id` twice changes counts only once
- a temporary backend outage causes agent retries without agent termination
- the graph shows direction, TCP destination port, connection count, first seen, last seen
- filters and search produce deterministic visible results
- transient API failure leaves the last successful graph visible
- an automated smoke test checks the named expected demo edges

### Phase 3 — data model and history
- backend and agent restarts do not erase committed topology or inflate counts
- last 5 m, 15 m, 1 h, and a custom range are all inspectable
- a controlled deployment change appears as the expected `NEW` or `REMOVED` edge
- a threshold-crossing count change appears as `CHANGED` with its calculation visible
- database failure fails readiness with actionable errors and no credentials
- graph and diff are deterministic at exact bucket boundaries

### Phase 4 — completeness and feasibility
- the UI is usable at 1280×720 and all controls are keyboard reachable
- comparison status is understandable without colour alone
- edge thickness uses byte volume when available and connection count otherwise, metric named
- the byte-accounting decision is backed by a reproducible experiment under `docs/evaluation/`
- detail views show namespace, dependencies, ports, counts, optional bytes, first/last seen
- frontend unit/component tests pass in CI

### Phase 5 — handoff
- a clean supported machine runs `make demo-up` without hand-editing manifests
- `make demo-traffic` and `make demo-change` produce the expected topology and diff
- deleting and recreating backend or database pods preserves history through PVCs
- the agent reports from every node in both kind and kubeadm clusters
- `make demo-down` removes only resources created by the project
- all ADR requirements map to a test, demonstration step, or documented limitation
- measured results support or explicitly reject each performance target
- CI passes from a clean checkout; the demo succeeds from committed instructions only
- failure modes for missing BTF, denied BPF permissions, backend outage, PostgreSQL failure, and empty graph have actionable messages
- final screenshots and recordings expose no secrets or individual external IPs

## Recording template

Write `docs/evaluation/phase-N.md` containing: date, commit SHA, `make tools` output, each command
run with its verbatim output, the criterion-by-criterion verdict, tests skipped and why, and any
deviation recorded as a new ADR.
