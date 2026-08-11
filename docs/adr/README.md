# ADR Set — Dynamic Service Topology Visualization in Kubernetes Environments

- **Date:** 2026-08-12
- **Status:** Accepted for implementation
- **Implementation target:** `/home/it-laptop/kubernetes-topology-visualizer`
- **Reference prototype:** `/home/it-laptop/claude-test-fyp/kubernetes-topology-ebpf-demo` (read-only)
- **Authoritative scope:** `/home/it-laptop/claude-test-fyp/FYP_Project_Scope_Source_of_Truth.md`

## 1. How this ADR set is organised

`ADR-001` is the system-level architecture decision. `ADR-002` through `ADR-008` decompose it into
one ADR per component, each written as an **implementation guide**: what to build, what to reuse
from the prototype, what must change, which tests prove it, and **which Claude Code skills and
plugins to use while building it**.

| ADR | Component | Repo path | ADR-001 source | Owning phases | Tracker |
|---|---|---|---|---|---|
| [ADR-002](ADR-002-ebpf-node-agent.md) | eBPF node agent (Go) | `agent/` | §5.1, §5.2 | 1, 2, 4 | §9 |
| [ADR-003](ADR-003-ingestion-contract.md) | Ingestion & API contract | `contracts/` | §5.3 | 0, 2, 3 | §8 |
| [ADR-004](ADR-004-backend-service.md) | Backend service (FastAPI) | `backend/` | §5.5 | 2, 3, 4 | §9 |
| [ADR-005](ADR-005-persistence-postgresql.md) | Persistence (PostgreSQL) | `backend/app/persistence/`, `backend/migrations/` | §5.4 | 3 | §9 |
| [ADR-006](ADR-006-frontend-topology-ui.md) | Frontend topology UI (React) | `frontend/` | §5.6 | 2, 3, 4 | §9 |
| [ADR-007](ADR-007-packaging-deployment.md) | Packaging & deployment (Helm/kind) | `charts/`, `kind/`, `demo/`, `scripts/` | §5.7 | 0, 5 | §9 |
| [ADR-008](ADR-008-testing-ci.md) | Testing & CI | `.github/`, `*/tests/`, `Makefile` | §8, §7 | 0–5 | §8 |

**Progress tracking:** [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md) is the master checklist —
every task carries an ID like `P1-A2` (phase 1, agent, task 2). Each component ADR repeats its own
subset in the tracker section listed above, using the same IDs. Tick both.

**Authority order.** `FYP_Project_Scope_Source_of_Truth.md` wins over `ADR-001`, which wins over
`ADR-002`–`ADR-008`. A component ADR may add detail but must never widen scope. Any deviation
requires a new ADR in `docs/adr/` **before** implementation (ADR-001 §12 instruction 6).

## 2. Environment prerequisites (verified 2026-08-12 on the target machine)

Fix these before Phase 0. They block more work than any tooling choice.

| Requirement | Present | Action |
|---|---|---|
| Go 1.23+ | **absent** | Install. The prototype builds Go inside `golang:1.22-bullseye`, but ADR-001 §7 Phase 0 requires host `make test` for agent unit tests. |
| Node 20+ / 22 LTS | **v18.20.8 (EOL)** | Upgrade. Vite 7 requires Node 20.19+. |
| `golangci-lint` | absent | Install; required by `make lint`. |
| `uv` | absent | Recommended for reproducible Python envs. |
| Helm | **v4.2.3** | Author the chart for **Helm 4**, and pin the same version in CI. Do not assume Helm 3 behaviour. |
| kind | v0.22.0 (K8s 1.29) | Upgrade; kubectl is v1.31.1. Needed for the three-node cluster. |
| clang | 14 | Adequate for `bpf2go`; keep BPF compilation in the pinned container for reproducibility. |
| Python | 3.10.12 | Adequate for FastAPI + Pydantic v2. |
| Kernel 6.8 + BTF + Docker 28.5 + bpftool 7.4 | present | No action. Ring buffer and CO-RE are available. |

## 3. Plugin installation

The `claude-plugins-official` marketplace is already registered. Install with
`/plugin install <name>@claude-plugins-official`.

```text
/plugin install context7@claude-plugins-official        # version-accurate library docs — highest value
/plugin install gopls-lsp@claude-plugins-official       # ADR-002
/plugin install pyright-lsp@claude-plugins-official     # ADR-004, ADR-005
/plugin install typescript-lsp@claude-plugins-official  # ADR-006
/plugin install frontend-design@claude-plugins-official # ADR-006
/plugin install playwright@claude-plugins-official      # ADR-006, ADR-008
/plugin install hookify@claude-plugins-official         # ADR-003, ADR-007
/plugin install skill-creator@claude-plugins-official   # authors the five custom skills below
/plugin install github@claude-plugins-official          # ADR-008
/plugin install claude-security@claude-plugins-official # ADR-008, Phase 5 only
```

**There is no Kubernetes or Helm plugin.** All 285 plugins in the official marketplace were
checked. Cluster work goes through `kubectl`/`helm`/`kind` via Bash plus the custom
`k8s-demo-loop` skill defined in ADR-007. Do not go looking for one.

## 4. Custom skills to author

These carry the project's non-negotiable invariants across six phases of editing. Each is owned by
exactly one ADR, which contains its full specification. Author them with `skill-creator` into
`.claude/skills/` in the target repository.

| Skill | Owner ADR | Purpose |
|---|---|---|
| `topology-contract` | ADR-003 | Canonical ID grammar, edge key, status-code semantics; fires on any edit to ingestion/graph/diff models in Go, Python, or TypeScript. |
| `ebpf-agent-dev` | ADR-002 | `bpf2go` regeneration, the active-open filter, the no-payload-bytes invariant. |
| `k8s-demo-loop` | ADR-007 | The build → load → install → wait → verify cycle that becomes `make demo-up`. |
| `phase-gate` | ADR-008 | Runs a phase's acceptance commands, records them, updates the requirements-to-tests checklist. |
| `adr-guard` | this file (§5) | Blocks out-of-scope features and requires a new ADR before architectural deviation. |

### 5. `adr-guard` specification

```yaml
name: adr-guard
description: >
  Enforces ADR-001 scope boundaries. Load before implementing any feature that is not
  explicitly listed in an ADR's decisions, and before changing an architectural choice
  (transport, storage engine, event source, identity grammar, deployment model).
```

Body asserts:

1. Check the request against ADR-001 §4.2 (out of scope) and §13 (deferred). If it matches — IPv6,
   UDP/DNS, multi-cluster, SSE/WebSocket, NetworkPolicy generation, auth, ML, mandatory
   Prometheus/Loki, HA Postgres — stop and report the section that excludes it.
2. Check against source-of-truth §25 explicit non-goals.
3. If the change contradicts a decision in ADR-002–ADR-008, write a new numbered ADR in
   `docs/adr/` recording context, decision, and consequences **before** editing code.
4. Never widen scope to "make it more complete". ADR-001 §12 instruction 5: prefer the smallest
   implementation that satisfies the ADR.

## 6. Cross-component skill matrix

| Skill / plugin | 002 agent | 003 contract | 004 backend | 005 storage | 006 frontend | 007 packaging | 008 testing |
|---|---|---|---|---|---|---|---|
| `context7` | ● | ● | ● | ● | ● | ● | ○ |
| `topology-contract` (custom) | ● | ● | ● | ● | ● | ○ | ● |
| `adr-guard` (custom) | ● | ● | ● | ● | ● | ● | ● |
| `ebpf-agent-dev` (custom) | ● | | | | | ○ | ○ |
| `k8s-demo-loop` (custom) | ○ | | ○ | ● | ○ | ● | ● |
| `phase-gate` (custom) | ○ | ○ | ○ | ○ | ○ | ○ | ● |
| `gopls-lsp` | ● | ○ | | | | | ○ |
| `pyright-lsp` | | ○ | ● | ● | | | ○ |
| `typescript-lsp` | | ○ | | | ● | | ○ |
| `frontend-design` | | | | | ● | | |
| `dataviz` (built-in) | | | | | ● (scoped) | | |
| `playwright` | | | ○ | | ● | ○ | ● |
| `hookify` | ○ | ● | ○ | ○ | ○ | ● | ○ |
| `github` | | | | | | ○ | ● |
| `/code-review` (built-in) | ● | ● | ● | ● | ● | ○ | ● |
| `/security-review` (built-in) | ● | ○ | ● | ● | ○ | ● | ● |
| `claude-security` | ○ | | ○ | ○ | ○ | ○ | ● (Phase 5) |
| `codex:rescue` | ● | | ○ | ○ | ○ | ○ | ○ |
| `/simplify` (built-in) | ○ | | ○ | ○ | ○ | | |
| `artifact-diagramming` (built-in) | | | | | | | ● (docs only) |

● primary — load it by default for this component ○ situational

## 7. Skills that must NOT be used, and why

| Skill / plugin | Why not |
|---|---|
| `artifact-design`, `artifact-capabilities` | For Claude-published artifact pages, not the product's React UI. Using them on `frontend/` produces the wrong architecture. (`artifact-diagramming` is allowed for `docs/architecture.md` diagrams only.) |
| `cloud-sql-postgresql`, `aiven`, `cockroachdb` | Managed-cloud database plugins. The target is an in-cluster PostgreSQL StatefulSet plus an external `DATABASE_URL` option. |
| `terraform` | No infrastructure-as-code in scope; Helm is the packaging decision (ADR-001 §5.7). |
| `grafana-*`, `newrelic`, `langfuse-observability` | ADR-001 §4.2 keeps Prometheus/Loki optional and forbids new mandatory observability dependencies. |
| `ralph-loop` | The phase acceptance gates in ADR-001 §7 are a stricter and more auditable control loop. |
| `code-simplifier`, `pr-review-toolkit`, `code-review` (plugin) | Redundant with built-in `/simplify` and `/code-review`. |
| `serena` | Overlaps the three LSP plugins already selected. |
| `dataviz` for graph layout | Scoped to palette, legend, and traffic-intensity encoding only. Node-link layout is Dagre's job (ADR-006). |

## 8. Recommended hooks

Configure via `/update-config` or `hookify`:

1. **PreToolUse deny** — block `Write`/`Edit` outside `/home/it-laptop/kubernetes-topology-visualizer`,
   and make the reference prototype read-only. Enforces ADR-001 §12 instruction 10.
2. **PostToolUse on `contracts/openapi.json`** — regenerate the TypeScript client and fail loudly if
   `frontend/src/api/` is stale. Enforces ADR-003.
3. **PostToolUse on `*.go` / `*.py`** — run `gofmt` / `ruff format` on the edited file.
4. **PreToolUse warn** — flag any diff adding a payload-byte field or logging a raw external IP.
   Enforces ADR-001 §6 privacy constraints.

Run `/fewer-permission-prompts` once after Phase 0 so `kubectl`, `helm`, `kind`, and `docker`
read-only calls stop prompting.
