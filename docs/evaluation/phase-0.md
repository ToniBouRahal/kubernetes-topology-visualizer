# Phase 0 Gate Record

- **Date:** 2026-08-12
- **Phase:** 0 — Repository foundation and contracts (ADR-001 §7)
- **Verdict:** **PASSED**
- **Recorded per:** ADR-001 §12 instruction 9 · ADR-008 D-8.5

## Toolchain (`make tools`)

```text
TOOL           PINNED         LOCAL
go             1.26.5         1.26.5
node           24.19.0        24.19.0
python         3.13           3.13.14
helm           4.2.3          4.2.3
kind           0.32.0         0.32.0
kubectl        1.31.1         1.31.1
```

Kernel 6.8.0-136-generic · BTF present at `/sys/kernel/btf/vmlinux` · Docker 28.5.2 · clang 14 ·
bpftool 7.4.0.

## Acceptance criteria

| # | Criterion (ADR-001 §7 Phase 0) | Result | Evidence |
|---|---|---|---|
| 1 | `make lint` and `make test` succeed on the service skeletons | **PASS** | `make verify` exit 0 |
| 2 | `helm lint charts/topology-visualizer` succeeds | **PASS** | `scripts/verify-chart.sh` T-7.1 |
| 3 | `helm template ... -f ci/kind-values.yaml` renders valid Kubernetes YAML | **PASS** | 9 resources, parsed with PyYAML |
| 4 | Contract examples validate against the API schema | **PASS** | 20 fixtures, 51 Python tests |
| 5 | Tool versions and local prerequisites documented | **PASS** | `docs/prerequisites.md`, `make tools` |

## Commands run

```bash
make tools
make verify          # → lint (go, python, frontend, helm) + test (go, python, frontend) + contracts-check
bash scripts/verify-chart.sh
cd agent   && go test ./... -count=1 -v
cd backend && .venv/bin/python -m pytest -q
```

## Results

```text
go vet ./...                                    0 issues
ruff check .                                    All checks passed!
ruff format --check .                           10 files already formatted
chart verification                              25 passed, 0 failed
go test ./...                                   ok  (8 tests, 43 cases incl. subtests)
pytest -q                                       51 passed
export_openapi.py --check                       OK: contracts/openapi.json matches the application
```

### Chart verification detail

25 assertions: `helm lint`; renders 9 resources; output parses as YAML; no wildcard in the
ClusterRole; agent verbs are exactly `get`/`list`/`watch`; RBAC covers all nine watched resource
types; backend and frontend mount no ServiceAccount token; `CLUSTER_ID` resolves to a single value
across agent and backend; and 8 `values.schema.json` rejection cases (empty `clusterId`,
`clusterId` containing `:`, CORS `*`, in-cluster database with no password, multi-replica backend,
invalid `pullPolicy`, out-of-range port) plus one positive case.

### Contract corpus

20 fixtures in `contracts/examples/`, expectations declared in `manifest.json`:

- 3 valid (canonical, minimal, with byte fields)
- 1 → `400` (unsupported `schema_version` — deliberately not 422)
- 16 → `422` (non-ULID `batch_id`, `ReplicaSet` kind, unknown kind, UDP, naive timestamp,
  zero/negative `connection_count`, port out of range, negative bytes, reversed interval, extra
  field, missing `cluster_id`, empty `agent_id`, zero `interval_seconds`)

Consumed by both the Python suite and the Go round-trip suite — one corpus, two languages
already, three from Phase 2.

## Tests deliberately not run

| Test | Reason |
|---|---|
| T-2.11, T-2.12 (privileged eBPF) | No BPF program exists yet — Phase 1. `make test-ebpf` documented. |
| Cluster E2E (T-8.1 – T-8.6) | No deployable images yet — Phase 2. |
| Frontend suite (T-6.*) | Frontend not scaffolded — Phase 2 (P2-F1). |
| Helm install against a live cluster | Phase 0 is static validation only, by design. |

Nothing was skipped that Phase 0 requires.

## Deviations from plan

| # | Deviation | Rationale |
|---|---|---|
| 1 | Node **24.19.0** installed, not 22 | Node 24 (Krypton) is the current active LTS. The plan said 22; 24 satisfies every constraint (Vite 7 needs ≥ 20.19) and avoids an upgrade mid-project. |
| 2 | Toolchain installed **user-scoped**, no `sudo` | System Node 18 hosts `@openai/codex` and `@google/gemini-cli`. Replacing it risked breaking the Codex delegation the project depends on. Go → `~/.local/go`, Node → nvm, both symlinked into `~/.local/bin`. |
| 3 | `GET /metrics` absent from `openapi.json` | It returns Prometheus text, not JSON, and is conventionally outside the OpenAPI document. Implemented in Phase 2 (P2-B7); the route list in ADR-001 §5.3 is otherwise complete. |
| 4 | `P0-K3/K4/K5` completed directly rather than by the Codex delegation | The delegated task produced no files and left no session log after ~20 minutes. Taking it over was preferable to blocking the gate. Codex delegation remains planned for P1-A16, P2-T3, P3-B9, P3-D2, P4-A22. |
| 5 | Pre-existing content at the target path preserved | `poc-kind-topology/` (an earlier, unrelated POC) and an empty `l` file were gitignored rather than deleted — ADR-001 §12 instruction 10. |

None of these contradicts an ADR decision, so no new ADR is required (instruction 6).

## Notes carried into Phase 1

1. **`~/.bashrc` returns early for non-interactive shells**, so `nvm` never loads under `make` or
   CI. Resolved with `~/.local/bin` symlinks; documented in `docs/prerequisites.md`.
2. **VS Code runs as a snap** and points `XDG_DATA_HOME` inside the snap directory — installers
   must be given an explicit target directory.
3. **Helm 4 accepted `apiVersion: v2`** and linted the chart with no Helm 3/4 incompatibilities
   encountered. Only advisory output: `icon is recommended`.
4. The **`kind` → `Never` pull policy** in `ci/kind-values.yaml` assumes side-loaded images; the
   image-reload gotcha is captured in the `k8s-demo-loop` skill.

## Next

Phase 1 — eBPF capture and Kubernetes resolution. First task `P1-A1`: generate `bpf/vmlinux.h`
from BTF. The Phase 1 gate is defined in the `phase-gate` skill and `docs/IMPLEMENTATION-PLAN.md`.
