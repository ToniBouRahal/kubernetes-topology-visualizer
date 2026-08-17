# ADR-007: Packaging and Deployment — Helm, kind, and the Demo Loop

- **Status:** Accepted for implementation
- **Date:** 2026-08-12
- **Parent:** ADR-001 §5.7 · Source of truth §17, §18, §19
- **Component path:** `charts/`, `kind/`, `demo/`, `scripts/`, `Makefile`
- **Owning phases:** Phase 0 (skeleton), Phase 1 (agent DaemonSet), Phase 5 (complete chart, multi-node)
- **Tooling on this machine:** Helm **v4.2.3**, kind v0.22.0, kubectl v1.31.1, Docker 28.5.2

## 1. Scope

The Helm chart, the kind cluster definition, demo workloads, the Make targets that drive the whole
loop, RBAC, security contexts, and the multi-node kubeadm validation. Component internals belong to
ADR-002 and ADR-004 through ADR-006.

## 2. Context

The prototype deploys via scattered per-component `k8s/` directories (`agent/k8s/`, `backend/k8s/`,
`frontend/k8s/`) applied by hand. ADR-001 §3 lists this as a deficiency. The manifests are still
useful as a **content** reference — the DaemonSet's host mounts, the RBAC verbs, the namespace — but
the deployment model is replaced entirely by one chart.

**Helm 4 is installed.** Author and validate against Helm 4 semantics and pin the same version in
CI. Do not assume Helm 3 behaviour for chart API version, subchart handling, or lint strictness;
verify with `context7` before writing the chart rather than after `helm lint` fails.

## 3. Decisions

### D-7.1 — One chart

```text
charts/topology-visualizer/
  Chart.yaml
  values.yaml
  values.schema.json          non-optional: it is what makes bad values fail fast
  templates/
    agent-daemonset.yaml
    agent-rbac.yaml           ServiceAccount + ClusterRole + binding
    backend-deployment.yaml
    backend-service.yaml
    frontend-deployment.yaml
    frontend-service.yaml
    configmaps.yaml
    secrets.yaml
    networkpolicies.yaml
    NOTES.txt                 how to reach the UI after install
  ci/kind-values.yaml
kind/cluster.yaml             three nodes: 1 control-plane, 2 workers
```

`helm lint` and `helm template ... -f ci/kind-values.yaml` are mandatory validation steps from
Phase 0, before any of it does anything useful.

### D-7.2 — PostgreSQL packaging

The chart must support both an in-cluster database for the self-contained demo and an external
`DATABASE_URL` (ADR-001 §5.4).

**Verify the subchart before depending on it.** Third-party PostgreSQL chart image sources have
changed distribution terms in the recent past, and a demo that cannot pull its database image is a
demo that fails on a clean machine — the exact failure Phase 5's "clean supported machine" criterion
targets. Check image pullability from a clean environment early. If a subchart's images are not
reliably pullable, template a minimal PostgreSQL StatefulSet in-chart against a pinned upstream
`postgres` image instead. Either choice is compatible with ADR-001; an unpullable image is not.

Credentials come from a Secret. `values.yaml` must never contain a real password, and
`values.schema.json` should make an empty password fail rather than default to one.

### D-7.3 — Agent DaemonSet

Privileged for the FYP (ADR-001 §5.7, source of truth §18). Required properties:

- `hostPID` and the host mounts the eBPF loader needs (`/sys/fs/bpf`, `/sys/kernel/debug` as
  required by the final implementation — mount the minimum that actually works, and document it);
- tolerations so the agent also runs on the control-plane node — otherwise the three-node
  "runs on every node" criterion silently fails;
- `NODE_NAME` via the downward API;
- resource requests/limits sized for the 256 MiB memory target (ADR-001 §6);
- liveness/readiness probes against the agent's health endpoints (ADR-002 D-2.7).

RBAC: `get`, `list`, `watch` only, and only for the resources ADR-002 D-2.3 actually watches — pods,
replicasets, deployments, statefulsets, daemonsets, jobs, services, endpointslices, namespaces.
Backend and frontend get **no** Kubernetes API permissions (ADR-001 §5.7). Do not grant a blanket
`*` ClusterRole "for now"; it will survive to submission.

### D-7.4 — Backend, frontend, and network posture

- Backend and frontend run as non-root with read-only root filesystems where compatible
  (ADR-001 §6). The agent is exempt by necessity — document that asymmetry.
- Ingestion is **not** exposed outside the cluster in the default manifests. Default frontend access
  for kind is port-forwarding.
- NetworkPolicies restrict ingest to agent pods and the database to the backend. Note that kind's
  default CNI does not enforce NetworkPolicy — ship them for correctness and multi-node validation,
  and say plainly in `docs/limitations.md` that they are unenforced in the kind demo rather than
  implying protection that is not there.
- All container images pinned by version or digest before the final release.

### D-7.5 — Configuration surface

Every variable in ADR-001 §5.7 is a chart value with a schema entry, a documented default, and a
single owning component:

| Variable | Owner |
|---|---|
| `CLUSTER_ID` | shared — must be identical for agent and backend, or IDs will not match |
| `BACKEND_INGEST_URL`, `AGENT_FLUSH_INTERVAL_SECONDS`, `AGENT_MAX_PENDING_BATCHES`, `INFRASTRUCTURE_PORTS` | agent (ADR-002) |
| `DATABASE_URL`, `RETENTION_HOURS` | persistence (ADR-005) |
| `GRAPH_MAX_NODES`, `GRAPH_MAX_EDGES`, `CORS_ALLOWED_ORIGINS`, `TOPOLOGY_DIFF_CHANGE_THRESHOLD_PERCENT` | backend (ADR-004) |

`CLUSTER_ID` deserves attention: it is embedded in every canonical node ID (ADR-003 D-3.2). A
mismatch between agent and backend values produces a graph that looks empty for no visible reason.
Template it from one chart value into both, and never allow two independent defaults.

### D-7.6 — Make targets

```make
demo-up        kind cluster + build + load + helm install + wait for readiness
demo-traffic   generate the known demo traffic
demo-change    apply the controlled topology change (a new dependency)
demo-verify    assert the expected edges and the expected diff via the API
demo-down      uninstall and delete ONLY resources this project created
kind-up/down   cluster lifecycle alone
images         build and load agent, backend, frontend
chart-lint     helm lint + helm template against ci/kind-values.yaml
```

`demo-up` must work on a clean supported machine with no manifest hand-editing (Phase 5). `demo-down`
must be surgical — it deletes the release and the kind cluster this project created, never a
namespace it did not create. Deleting a user's unrelated cluster during a demo teardown is
unrecoverable; scope every deletion by release name and cluster name.

### D-7.7 — Demo workloads

At least two namespaces with deterministic traffic producing known edges, including
`frontend → backend`, `backend → redis`, and `something → EXTERNAL`, plus a controlled change
scenario for `demo-change` that introduces exactly one new dependency. Determinism is the point: the
expected edge list is asserted by tests (ADR-008) and shown in the presentation script.

### D-7.8 — Multi-node validation

The same chart, unmodified, installs on a multi-node kubeadm cluster (Phase 5). Environment-specific
values — kernel version, BTF availability, CNI, storage class — are recorded under
`docs/evaluation/`, not patched into templates. If something must differ, it becomes a documented
value override.

## 4. Implementation guide

Phase 0: chart skeleton that lints and templates with placeholder images; three-node `kind/cluster.yaml`;
`chart-lint` and `kind-up`/`kind-down` targets.

Phase 1: agent DaemonSet, RBAC, ServiceAccount; verify a pod on every node including the
control-plane.

Phase 2: backend and frontend Deployments/Services, ConfigMaps, port-forward instructions in
`NOTES.txt`.

Phase 3: PostgreSQL, PVC, Secret, migration-aware readiness.

Phase 5: NetworkPolicies, probes, resource limits, security contexts, pinned digests, image scan and
triage, the full Make target set, multi-node validation, and recorded evidence.

## 5. Skills and plugins for this component

### Required

| Skill / plugin | When | Why |
|---|---|---|
| **`k8s-demo-loop`** (custom — spec below) | Every cluster interaction, from Phase 1 onward | This loop runs hundreds of times. Encoding it once removes the drift between what was run, what the Makefile says, and what the docs claim — which is precisely what Phase 5's "the complete demo succeeds using only committed instructions" criterion tests. |
| **`context7`** | Before writing chart templates or the kind config | Helm **4** is installed. Chart API version, subchart syntax, and lint strictness must be verified against current Helm 4 documentation, not recalled from Helm 3 habits. |
| **`hookify`** | Phase 0, once | Installs the write-scope guard: no `Write`/`Edit` outside the target repo, prototype read-only (ADR-001 §12 instruction 10). Also a good place for a guard against `kubectl delete` without a release/namespace scope. |
| **`/update-config`** (built-in) | Phase 0 | Adds `kubectl`, `helm`, `kind`, and `docker` permissions to project settings so the loop is not interrupted by prompts. |
| **`/fewer-permission-prompts`** (built-in) | After a few days of cluster work | Scans transcripts and proposes a real allowlist from actual usage — better than guessing the list up front. |
| **`adr-guard`** (custom) | Any proposal to add an Ingress, a service mesh, HA replicas, or a second cluster | ADR-001 §4.2 and §13. |

### Situational

- **`/security-review`** before Phase 5 on the chart: RBAC verbs, privileged context, secret
  handling, NetworkPolicy coverage.
- **`ebpf-agent-dev`** (ADR-002) — when the DaemonSet starts but the BPF program fails to attach,
  which is a node/mount problem more often than a code problem.
- **`github`** — Phase 5 release tagging and CI wiring (ADR-008 owns it).

### Do not use

- **No Kubernetes or Helm plugin exists** in the official marketplace. This was verified across all
  285 plugins. Use `kubectl`/`helm`/`kind` via Bash plus `k8s-demo-loop`.
- **`terraform`** — Helm is the packaging decision; there is no IaC layer in scope.
- **`cloud-sql-postgresql`, `aiven`, `alloydb`** — managed-cloud database plugins; wrong deployment
  model (ADR-005 §5).
- **`grafana-*`, `newrelic`** — ADR-001 §4.2 keeps external observability optional and non-mandatory.

### `k8s-demo-loop` skill specification

```yaml
name: k8s-demo-loop
description: >
  Build, deploy, and verify the topology visualizer in kind. Load before creating or
  resetting the cluster, building or loading images, installing or upgrading the Helm
  release, generating demo traffic, or diagnosing a pod that will not start.
```

Body must contain:

1. **The canonical loop**, matching the Makefile exactly: `kind create cluster --config kind/cluster.yaml`
   → `docker build` each image → `kind load docker-image` → `helm upgrade --install` with
   `ci/kind-values.yaml` → `kubectl rollout status` for each workload → port-forward → verify.
2. **The image-reload gotcha**: `kind load` with an unchanged tag does not restart pods. Either use
   a content-based tag or `kubectl rollout restart` after loading. This wastes more time than any
   other single mistake in this loop.
3. **Tool versions**: Helm 4.2.3, kind 0.22.0, kubectl 1.31.1 — and the note that these are pinned
   in CI, so a local upgrade must be mirrored there.
4. **Teardown scope**: `demo-down` removes only this project's release and cluster. Never
   `kubectl delete ns` on a namespace the chart did not create.
5. **Diagnostic ladder**: pod pending → describe for scheduling/tolerations; agent CrashLoop →
   check BTF and mounts before code; backend not ready → migrations or database first; empty graph →
   confirm `CLUSTER_ID` matches on both sides *before* suspecting the collector.
6. **Verification**: the exact `curl` against the graph API and the expected demo edge list, so
   "it works" is an assertion rather than an impression.

## 6. Test requirements (ADR-001 §8 row 7)

| ID | Test |
|---|---|
| T-7.1 | `helm lint charts/topology-visualizer` passes |
| T-7.2 | `helm template` with `ci/kind-values.yaml` renders valid Kubernetes YAML (validated by a schema checker in CI) |
| T-7.3 | `values.schema.json` rejects malformed values — missing `CLUSTER_ID`, empty database password |
| T-7.4 | Rendered agent RBAC contains only `get`/`list`/`watch` on the declared resources; backend and frontend have no Kubernetes RBAC |
| T-7.5 | An agent pod is Running on every node of the three-node cluster, control-plane included |
| T-7.6 | Probes: readiness fails while the database is down and recovers when it returns |
| T-7.7 | Secrets are referenced by name, never inlined in a values file |
| T-7.8 | PVC-backed persistence survives database pod deletion |
| T-7.9 | Rollout: `helm upgrade` with a changed image replaces pods and the graph recovers |
| T-7.10 | `demo-up` on a clean machine with no hand-edited manifests |
| T-7.11 | `demo-down` removes only this project's resources |
| T-7.12 | The same chart installs on the kubeadm cluster; environment-specific values recorded |

## 7. Acceptance criteria

Phase 0: `helm lint` passes; `helm template` with kind values renders valid YAML.

Phase 1: the agent runs on every node of the three-node kind cluster.

Phase 5: `make demo-up` on a clean machine; `demo-traffic` and `demo-change` produce the expected
topology and diff; PVC-backed history survives pod recreation; the agent reports from every node in
both kind and kubeadm; `demo-down` is surgical; failure modes for missing BTF, denied BPF
permissions, backend outage, PostgreSQL failure, and empty graph all give actionable messages.

## 8. Consequences

- **A privileged DaemonSet is a real security posture, not a detail.** It is documented in the chart,
  in `docs/limitations.md`, and in the report. Capability reduction is attempted after correctness
  and must not block the demonstration (ADR-001 §5.7).
- **kind does not enforce NetworkPolicy by default.** Shipping policies that the demo environment
  ignores is the right call for correctness on real clusters, provided the limitation is stated
  rather than implied away.
- **One chart raises the Phase 0 cost.** Deliberate: the alternative is the prototype's scattered
  manifests, which ADR-001 §3 already identified as a deficiency, and Phase 5 cannot pass without a
  single-command install.
- **Multi-node validation may surface environment differences** in kernel, CNI, or storage class.
  These are recorded as values and documented findings — genuine FYP evidence that the design is not
  kind-specific, not defects to hide.

## 9. Implementation tracker

Mirrors [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md). `[ ]` open · `[~]` in progress · `[x]` done ·
`[!]` blocked · `[-]` dropped with reason.

### Phase 0 — skeleton

- [x] **P0-K1** `git init` with the ADR-001 §5.8 directory structure
- [x] **P0-K2** Copy ADR-001, source-of-truth, and ADR-002–008 into `docs/` and `docs/adr/`
- [x] **P0-K3** Chart skeleton that lints and templates with placeholder images — D-7.1 · tests T-7.1, T-7.2
- [x] **P0-K4** `kind/cluster.yaml`, three nodes — D-7.1
- [x] **P0-K5** `values.schema.json` rejecting malformed values — D-7.2 · test T-7.3
- [x] **P0-S4** Author the `k8s-demo-loop` skill — §5
- [x] **P0-S7** Hooks: write-scope guard, scoped-deletion guard — README §8

**Phase 0 gate:** `helm lint` passes; `helm template` with `ci/kind-values.yaml` renders valid YAML.

### Phase 1 — agent on the cluster

- [x] **P1-K1** Agent DaemonSet, ServiceAccount, least-privilege RBAC, control-plane tolerations, host mounts — D-7.3 · test T-7.4
- [ ] **P1-K2** A pod Running on every node of the three-node cluster — test T-7.5

**Phase 1 gate:** the agent runs on every node, control-plane included.

### Phase 2 — full stack in kind

- [ ] **P2-K3** Backend + frontend Deployments, Services, ConfigMaps — §4
- [ ] **P2-K4** `NOTES.txt` with port-forward instructions — D-7.1
- [ ] **P2-K5** `CLUSTER_ID` templated from one value into both agent and backend — D-7.5

### Phase 3 — database

- [ ] **P3-K6** PostgreSQL: StatefulSet or subchart, PVC, Secret-based credentials — D-7.2 · tests T-7.7, T-7.8
- [ ] **P3-K7** Verify the database image pulls on a clean machine before depending on it — D-7.2
- [ ] **P3-K8** External `DATABASE_URL` path validated — D-7.2

### Phase 5 — completion and validation

- [ ] **P5-K9** Probes, resource limits, security contexts, non-root + read-only rootfs — D-7.4 · test T-7.6
- [ ] **P5-K10** NetworkPolicies shipped; kind non-enforcement documented — D-7.4
- [ ] **P5-K11** Make targets: `demo-up`, `demo-traffic`, `demo-change`, `demo-verify`, `demo-down` — D-7.6
- [ ] **P5-K12** Demo workloads across ≥2 namespaces + the controlled change scenario — D-7.7
- [ ] **P5-K13** `demo-up` on a clean machine with no hand-edited manifests — test T-7.10
- [ ] **P5-K14** `demo-down` removes only this project's resources — test T-7.11
- [ ] **P5-K15** Rollout: `helm upgrade` replaces pods and the graph recovers — test T-7.9
- [ ] **P5-K16** Multi-node kubeadm validation with the same chart — D-7.8 · test T-7.12
- [ ] **P5-K17** Pin all images by version or digest; scan and triage — D-7.4
- [ ] **P5-K18** Tag the submission release

**Phase 5 gate** (ADR-001 §7): clean-machine `demo-up` · expected topology and diff from
`demo-traffic` / `demo-change` · PVC history survives pod recreation · agent reports from every node
in kind **and** kubeadm · surgical `demo-down` · actionable failure messages.

### Standing invariants — re-verify at every phase gate

- [ ] Agent RBAC is `get`/`list`/`watch` only; backend and frontend have none — test T-7.4
- [ ] No real credential in any committed values file — test T-7.7
- [ ] Ingestion is not exposed outside the cluster by default — D-7.4
- [ ] `CLUSTER_ID` has exactly one source of truth in the chart — D-7.5
