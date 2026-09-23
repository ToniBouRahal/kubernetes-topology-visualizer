# ADR-011: Optional Prometheus Scrape and Grafana Dashboard

- **Status:** Accepted for implementation
- **Date:** 2026-09-23
- **Parent:** ADR-001 §4.2 (Prometheus and Loki stay optional, never mandatory) · §5.1 (the agent
  exposes Prometheus-format metrics) · §13 (deferred: optional Prometheus panels)
- **Amends:** ADR-007 D-7.4 (network posture — one opt-in ingress rule), D-7.5 (configuration
  surface — the `monitoring.*` block)
- **Amended by:** [ADR-013](ADR-013-bundled-observability.md) — §4's rejection of a bundled stack
  is superseded for a narrower bundle (plain Prometheus + Grafana, no operator); the dashboard
  ConfigMap moves to `templates/dashboards.yaml` so both paths share it
- **Component path:** `charts/topology-visualizer/`
- **Owning phase:** Post-Phase-5 extension

## 1. Context

The agent and the backend already speak Prometheus. ADR-001 §5.1 requires the agent to expose
Prometheus-format metrics; P1-A7 and P2-B7 delivered them, and the backend's `metrics.py` states
their purpose in one sentence: *a rejected batch and an absence of traffic look identical in a
graph; they must not look identical here.* Twenty-six counters and gauges exist so that silent data
loss becomes diagnosable.

Today the only documented way to read them is `kubectl port-forward` and `curl`
(`docs/operator-guide.md` §"What to watch"). That is enough for a demo and useless for an operator
who wants to know whether the ring buffer overflowed *last night*. The table in the operator guide
tells the reader what a bad value means; nothing shows the value over time or raises it without
someone looking.

Every cluster this tool is meant for already runs Prometheus and Grafana. What is missing is not a
metrics stack but the two small declarations that let an existing stack find these endpoints, and a
dashboard that reads them the way the operator guide says they should be read.

Two scope rules constrain this. ADR-001 §4.2 forbids Prometheus or Loki as *mandatory* dependencies.
ADR-001 §13 defers "optional Prometheus and Loki detail-panel integrations" — panels *inside the
frontend*. This ADR takes the smaller of the two integrations and leaves the frontend untouched.

## 2. Decision

**D-11.1 — Off by default, and no new dependency.** `monitoring.enabled` defaults to `false`, and
the default render is unchanged. The chart installs neither Prometheus nor Grafana at any setting:
it emits the declarations an *existing* Prometheus Operator (`monitoring.coreos.com/v1`) and a
Grafana with the dashboard sidecar discover on their own — the kube-prometheus-stack shape, which is
what most clusters run. If the CRDs are absent, `helm install` fails at apply time with
`no matches for kind "PodMonitor"`. That is the intended message: an actionable failure, not a
silently missing scrape.

**D-11.2 — One scrape declaration per exposing component.** A `PodMonitor` for the agent and a
`ServiceMonitor` for the backend.

The agent gets a `PodMonitor` rather than a Service plus `ServiceMonitor` because it is a DaemonSet
with no Service, and per-pod is the right unit anyway: a ring-buffer overflow is one node's problem,
and `topology_agent_events_filtered_foreign_node_total` is only meaningful per node. Adding a
headless Service purely to be scraped would put a Service in the topology that carries no
dependency. The backend has a Service and its `http` port already serves `/metrics`, so a
`ServiceMonitor` is the natural fit.

Both carry `monitoring.monitorLabels`, because the Prometheus Operator only picks up monitors its
selector matches (kube-prometheus-stack defaults to `release: <its release name>`), and a monitor
that renders but is never selected fails in the same silent way this ADR exists to prevent.

**D-11.3 — One dashboard, about the pipeline, not the topology.** The chart ships
`dashboards/topology-visualizer.json` as a ConfigMap labelled for the Grafana sidecar
(`grafana_dashboard: "1"` by default, both key and value configurable). The dashboard answers one
question: *can the graph be trusted right now?* Kernel samples lost, unresolved share, foreign-node
events, delivery queue depth, batches dropped or rejected, backend accept / dedupe / reject rates,
stored buckets, graph-query latency — the operator guide's table, over time, per node.

It does **not** draw the topology. Source of truth §25 says this project is not a Grafana
replacement; the reverse holds too. The graph is the deliverable and stays the only place the
topology is drawn. Panel copy says *connections*, never *requests* (ADR-001 §6; `PRODUCT.md`).

**D-11.4 — The dashboard may only reference metrics that exist.** Test T-11.3 extracts every
`topology_agent_*` and `topology_backend_*` name from the JSON and asserts each one appears in
`agent/cmd/agent/main.go` or `backend/app/api/metrics.py`. A renamed counter would otherwise leave a
panel that is empty for the same reason a healthy one reads zero — exactly the ambiguity the metrics
were added to remove.

**D-11.5 — The backend NetworkPolicy admits scrapes from one namespace, only when asked.** ADR-007
D-7.4 restricts backend ingress to agent and frontend pods. Under an enforcing CNI, enabling
monitoring alone would give Prometheus a target that reads `up == 0` and nothing else. When both
`monitoring.enabled` and `networkPolicy.enabled` are set, the backend policy gains one ingress rule:
from `monitoring.prometheusNamespace` (matched on `kubernetes.io/metadata.name`), optionally
narrowed by `monitoring.prometheusPodSelector`, on the backend port only. The agent carries no
NetworkPolicy and needs no rule.

One namespace, not the cluster: the counters do not carry payload or addresses, but the rate of
connection establishments across a cluster is still not something every pod should be able to read.

**D-11.6 — No new metrics, no frontend change.** If a panel needs a metric that does not exist, the
metric goes through its component's ADR; this ADR only exposes what is already emitted. Detail-panel
links from the UI into Prometheus or Loki remain deferred under ADR-001 §13.

## 3. Consequences

**An operator with kube-prometheus-stack is two flags away from graphs of the operator-guide
table.** `--set monitoring.enabled=true --set monitoring.monitorLabels.release=<name>`; the sidecar
imports the dashboard within its poll interval.

**The configuration surface grows by one block**, validated by `values.schema.json` like the rest
(D-7.5). `scripts/verify-chart.sh` gains tests T-11.1 – T-11.5 and CI renders both states.

**The kind demo does not change.** `ci/kind-values.yaml` leaves monitoring off, the demo cluster
runs no Prometheus, and `docs/demo-script.md` is untouched. Nothing in the fifteen-minute
walkthrough depends on this.

**Counters reset on restart** — stated in `metrics.py`, and it shapes the dashboard: every panel
uses `rate()` or `increase()`, never a raw counter, so a rollout reads as a dip rather than a
collapse. The one gauge shown raw is `topology_backend_stored_edge_buckets`, which is a level.

**Alerting is deliberately not included.** A `PrometheusRule` for
`topology_agent_kernel_samples_lost_total > 0` is the obvious next step and would be twenty lines.
ADR-001 §12 instruction 5 asks for the smallest implementation that satisfies the decision, and
this decision is about making the existing metrics reachable. Rules are recorded here as follow-up
work, not forgotten.

**A second privileged path does not appear.** The scrape targets are the same ports the health
probes already use, on the same containers, with no new listener and no new capability.

## 4. Alternatives rejected

**Bundle kube-prometheus-stack as a subchart.** Even behind a `condition`, a dependency in
`Chart.yaml` is fetched on every `helm dependency update`, drags ~30 CRDs into a demo cluster that
does not need them, and is mandatory in practice for anyone who has not already got the stack.
That is the dependency ADR-001 §4.2 forbids. The operator who has Grafana already has it.
*Superseded in part by ADR-013*, which bundles the plain `prometheus` and `grafana` charts — no
operator, no CRDs — behind one flag, and accounts for the fetch cost it accepts. The rejection of
kube-prometheus-stack itself stands.

**`prometheus.io/scrape` pod annotations instead of CRDs.** Those work only against a hand-written
scrape config that the Prometheus Operator does not ship by default. `PodMonitor` and
`ServiceMonitor` are what the operator actually discovers. Annotations can be added later in four
lines if a non-operator Prometheus turns out to matter; shipping both now is scope for a case nobody
has.

**Grafana panels inside the frontend.** This is the ADR-001 §13 item, and it is a different piece
of work: a Grafana URL in the chart, an iframe or a proxied query, and authentication between the
two UIs. A separate ADR, if it ever becomes worth it.

**A topology panel in Grafana (the node-graph panel).** Would need the backend to express the graph
as Prometheus series, and would reproduce the product in a weaker form — no time windows, no
comparison, no `EXTERNAL` collapse. The graph is the deliverable.

## 5. Tests

| ID | Assertion | Where |
|---|---|---|
| T-11.1 | The default render contains no `PodMonitor`, no `ServiceMonitor`, no dashboard ConfigMap, and the backend NetworkPolicy has no `namespaceSelector` | `verify-chart.sh` |
| T-11.2 | `monitoring.enabled=true` renders exactly one `PodMonitor` (agent, port `metrics`) and one `ServiceMonitor` (backend, port `http`, path `/metrics`), both carrying `monitorLabels`, plus one ConfigMap labelled for the sidecar whose payload parses as JSON | `verify-chart.sh` |
| T-11.3 | Every `topology_*` metric name in the dashboard exists in agent or backend source | `verify-chart.sh` |
| T-11.4 | With monitoring and NetworkPolicy both on, the backend policy admits `prometheusNamespace` on the backend port; the agent DaemonSet renders byte-identical to the monitoring-off render | `verify-chart.sh` |
| T-11.5 | The schema rejects a scrape interval that is not a duration and an empty Prometheus namespace | `verify-chart.sh` |

## 6. Tracker

- [x] **P6-K1** `monitoring.*` values block and schema — D-11.1
- [x] **P6-K2** `templates/monitoring.yaml`: `PodMonitor` for the agent, `ServiceMonitor` for the backend — D-11.2
- [x] **P6-K3** `dashboards/topology-visualizer.json` and its sidecar ConfigMap — D-11.3
- [x] **P6-K4** Backend NetworkPolicy scrape ingress, opt-in — D-11.5
- [x] **P6-K5** `verify-chart.sh` T-11.1 – T-11.5, including the metric-name check — D-11.4
- [x] **P6-K6** `docs/operator-guide.md` section; ADR-007, ADR index and `IMPLEMENTATION-PLAN.md` updated
