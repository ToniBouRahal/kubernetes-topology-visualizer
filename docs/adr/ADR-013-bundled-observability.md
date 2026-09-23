# ADR-013: Optional Bundled Prometheus and Grafana

- **Status:** Accepted for implementation
- **Date:** 2026-09-23
- **Parent:** ADR-001 §4.2 (Prometheus and Loki are never *mandatory*) · §5.7 (Helm packaging)
- **Amends:** ADR-011 §4 (supersedes the rejection of a bundled stack, for a narrower bundle),
  ADR-012 D-12.3 (the default metrics dashboard is now one this chart ships), ADR-007 D-7.4
  (image pinning extends to subchart images; one more opt-in ingress rule), D-7.5 (`observability.*`)
- **Component path:** `charts/topology-visualizer/` (`Chart.yaml` dependencies, `dashboards/`,
  `templates/dashboards.yaml`), `scripts/chart-deps.sh`, `Makefile`
- **Owning phase:** Post-Phase-5 extension

## 1. Context

ADR-011 made the metrics discoverable by a Prometheus and Grafana the operator already runs, and
ADR-012 linked the details panel into that Grafana. Both assume the stack exists. On the `kind` demo
it does not, so the evaluator watching the fifteen-minute walkthrough never sees the pipeline-health
dashboard or the panel's *Metrics* button do anything, and an operator trying the tool on a fresh
cluster has to bring their own stack before either feature can be judged.

The project's owner asked for Grafana as an **option in the chart**: one flag that produces a
working Grafana with the dashboards loaded and the panel wired up.

ADR-011 §4 rejected bundling — specifically *kube-prometheus-stack*: the operator, ~30 CRDs,
node-exporter, alertmanager, and a dependency fetched whether or not it is enabled. That reasoning
was about that stack. A bundle of the plain `prometheus` and `grafana` charts, behind a condition,
has none of the CRD cost and all of the demo value. This ADR takes that narrower option and
records what it costs.

The scope rule is unchanged: ADR-001 §4.2 forbids Prometheus as a *mandatory* dependency. A
subchart behind `condition: observability.enabled`, default `false`, renders nothing, runs nothing
and is not required for any accepted feature. What it does cost — a network fetch to build the
chart — is accounted for in D-13.5.

## 2. Decision

**D-13.1 — One flag.** `observability.enabled: false` by default. When true, the chart installs,
in the release namespace and pinned by digest:

| Component | Chart | Why it is there |
|---|---|---|
| Prometheus server | `prometheus-community/prometheus` 29.33.0 | scrapes the agent and backend; feeds both dashboards |
| kube-state-metrics | (subchart of the above) | pod readiness and restarts for the workload dashboard |
| Grafana | `grafana/grafana` 10.5.15 | the dashboards, with the sidecar that imports them |

and does **not** install alertmanager, pushgateway or node-exporter. The first two have no consumer
here; node-exporter needs host mounts on every node, and this chart already asks for one privileged
DaemonSet — it will not ask for a second for a feature that does not need it (ADR-007 D-7.4).

**D-13.2 — Scraping by annotation, because there is no operator.** The plain Prometheus chart
discovers targets through `prometheus.io/scrape`, `prometheus.io/port` and `prometheus.io/path`
annotations, not `PodMonitor` CRDs. When `observability.enabled`, the agent pod template and the
backend Service carry those annotations; otherwise they do not, so the default render stays
byte-identical (ADR-011 D-11.1 holds). This is the "annotations" path ADR-011 §4 declined to ship
for a case nobody had; the bundled Prometheus is that case. The two paths are for different
clusters: `monitoring.*` for an existing Prometheus Operator, `observability.*` for none. The schema
refuses both at once, because a `PodMonitor` on a cluster without the CRD fails the install.

**D-13.3 — The metrics dashboard the panel links to is one this chart ships.** ADR-012 D-12.3
defaulted `frontend.grafana.workloadDashboardUid` to kube-prometheus-stack's *Compute Resources /
Workload* dashboard, which a bundled Grafana does not have — the *Metrics* button would open a
404. The chart now ships `dashboards/topology-workload.json` (uid `topology-workload`): CPU, memory
working set, network and restarts per pod of the selected workload, from cAdvisor and
kube-state-metrics, which both the bundled Prometheus and kube-prometheus-stack collect under the
same metric names. It becomes the default UID everywhere — on an existing stack it arrives through
the same sidecar ConfigMap as the pipeline dashboard — and an operator who prefers the
kube-prometheus-stack dashboard sets the UID back. Pods are matched by name prefix, the same
heuristic as the *Logs* link, stated on the dashboard (`limitations.md` §4.5).

**D-13.4 — Grafana is wired to the bundled Prometheus, and nothing else is assumed.** The Grafana
subchart is given one provisioned datasource, `http://<release>-prometheus-server`, and the
dashboard sidecar enabled with the labels ADR-011 already uses. Admin password: the Grafana chart
generates one into a Secret; the operator guide gives the command to read it. No anonymous access,
no ingress, no persistence — a demo Grafana that loses its state on restart is correct, because
every dashboard it holds is re-provisioned from the ConfigMap.

The browser-facing URL for the panel's buttons is **not** derived: the chart cannot know how the
operator reaches Grafana (port-forward, ingress, a different host). `frontend.grafana.url` stays
explicit. `make demo-observability` sets it to `http://localhost:3000` and prints the matching
port-forward, because on the kind demo that is the only correct answer.

**D-13.5 — Dependencies are pinned and built, not vendored.** `Chart.yaml` pins both charts by
exact version; `Chart.lock` is committed (it was git-ignored; it is the reproducibility record and
belongs in the repository). The `.tgz` archives stay ignored and are fetched by
`scripts/chart-deps.sh` — idempotent, called by every script and Make target that renders or
installs the chart. This is the one cost that is *not* opt-in: `helm template` refuses a chart
whose declared dependencies are absent from `charts/`, even when their condition is false, so a
clean checkout now needs one network fetch (~200 KB) before the chart can be rendered at all. CI
already has the network; a developer without it cannot lint the chart. Stated here rather than
discovered.

**D-13.6 — Every bundled image is pinned by digest, and the pinning check proves it.**
ADR-007 D-7.4 pins third-party images by multi-arch manifest digest. The five images the bundle
pulls — Prometheus, its config reloader, kube-state-metrics, Grafana, the dashboard sidecar — are
pinned through the subcharts' `digest` / `sha` values. `scripts/verify-image-pinning.sh` renders
the chart with `observability.enabled` and fails on any `image:` that is not the project's own and
not `@sha256:`-pinned, so a subchart bump that drops a digest is caught rather than trusted.

**D-13.7 — The backend NetworkPolicy admits the bundled Prometheus.** It runs in the release
namespace, so ADR-011 D-11.5's cross-namespace rule does not cover it. When `observability.enabled`
and `networkPolicy.enabled`, one more ingress rule admits pods labelled as the Prometheus server, on
the backend port only. The agent has no policy and needs none.

## 3. Consequences

**The demo shows the whole story.** `make demo-observability` after `make demo-up`: the
pipeline-health dashboard is populated, and clicking a workload in the UI opens its CPU and memory
in Grafana for the same window. Both were invisible on the demo before.

**~450 MiB more on the kind cluster** (Prometheus ~250, Grafana ~120, kube-state-metrics ~40,
requests set lower). Acceptable on the laptop the demo targets; off by default so `make demo-up`
is unchanged.

**Rendering the chart needs the network once** (D-13.5). `make lint-helm` and `make demo-up` fetch
the dependencies through `chart-deps`; CI does the same. A developer who cannot reach
`prometheus-community.github.io` and `grafana.github.io` cannot run the chart tests. Recorded in
`docs/prerequisites.md`.

**Subchart images join the pinning and scan surface.** Five more digests to bump on upgrade, and
`make scan-images` does not scan them (it scans what the demo side-loads). Stated, not hidden.

**The workload dashboard uses the prefix heuristic** (D-13.3): a workload named `backend` also
shows `backend-worker` pods. Same limitation, same paragraph in `limitations.md`.

**No Loki.** The *Logs* button needs a Loki datasource UID and none is bundled, so on the demo it
does not appear. Bundling Loki plus a log shipper is a comparable amount of work again and a
different ADR, if wanted.

**Two scrape paths to keep in step.** A change to a metrics port or path must now update the
annotations (D-13.2) and the monitors (ADR-011). T-13.3 asserts the annotated port matches the
DaemonSet's `metrics` port so they cannot silently diverge.

## 4. Alternatives rejected

**kube-prometheus-stack as the bundle.** Rejected in ADR-011 §4 and still: the operator, the CRDs,
node-exporter on every node, alertmanager — for a demo that needs one scraper and one Grafana.

**Proxy Grafana through the frontend's nginx (`/grafana/`) so one port-forward serves both.**
Elegant, and it would let the chart derive the panel's URL. It also means the frontend image knows
about Grafana, Grafana runs under a sub-path, and the topology UI's own (absent) authentication
becomes the front door to Grafana's. Two port-forwards are the honest shape.

**Make the bundle the default.** ADR-001 §4.2 in one word.

**Vendor the `.tgz` archives.** Avoids the network fetch but commits binaries, and a bump becomes
a binary diff nobody reviews. The lock file pins; the fetch reproduces.

## 5. Tests

| ID | Assertion | Where |
|---|---|---|
| T-13.1 | The default render contains no Prometheus, kube-state-metrics or Grafana resources and no scrape annotations | `verify-chart.sh` |
| T-13.2 | `observability.enabled=true` renders a Prometheus server, kube-state-metrics and Grafana; no alertmanager, pushgateway or node-exporter | `verify-chart.sh` |
| T-13.3 | The agent pods and backend Service carry scrape annotations, and the annotated agent port equals the DaemonSet's `metrics` container port | `verify-chart.sh` |
| T-13.4 | Grafana's provisioned datasource points at the release's Prometheus service; the dashboard sidecar is on with the ADR-011 label | `verify-chart.sh` |
| T-13.5 | Both dashboards render in the sidecar ConfigMap; the workload dashboard's uid equals the frontend's default `workloadDashboardUid` | `verify-chart.sh` |
| T-13.6 | With NetworkPolicy on, the backend policy admits Prometheus server pods; the schema refuses `observability.enabled` together with `monitoring.enabled` | `verify-chart.sh` |
| T-13.7 | Every image in the `observability.enabled` render is digest-pinned except the project's own | `verify-image-pinning.sh` |

## 6. Tracker

- [x] **P6-K9** `Chart.yaml` dependencies, committed `Chart.lock`, `scripts/chart-deps.sh`, Make and script wiring — D-13.5
- [x] **P6-K10** `observability.*` values and schema; subchart values with five digests — D-13.1, D-13.6
- [x] **P6-K11** Scrape annotations on the agent pods and backend Service, opt-in — D-13.2
- [x] **P6-K12** `dashboards/topology-workload.json`; `templates/dashboards.yaml` serving both dashboards; frontend default UID — D-13.3
- [x] **P6-K13** Grafana datasource and sidecar wiring; NetworkPolicy rule — D-13.4, D-13.7
- [x] **P6-K14** `make demo-observability`; `verify-chart.sh` T-13.1 – T-13.6; `verify-image-pinning.sh` T-13.7
- [x] **P6-K15** Docs: operator guide, prerequisites, limitations, ADR-011/012/007 cross-references, index and plan
