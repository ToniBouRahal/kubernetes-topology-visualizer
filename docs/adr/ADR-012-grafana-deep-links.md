# ADR-012: Grafana Deep Links from the Details Panel

- **Status:** Accepted for implementation
- **Date:** 2026-09-23
- **Parent:** ADR-001 §13 (deferred: "optional Prometheus and Loki detail-panel integrations" —
  taken into scope here, as an option) · §4.2 (never a mandatory dependency) · §5.6 (details panel)
- **Amends:** ADR-006 D-6.6 (what the details panel shows), ADR-007 D-7.5 (configuration surface
  — the `frontend.grafana.*` block and how it reaches the browser)
- **Related:** ADR-011 (the chart's optional monitoring; independent of this — either works alone)
- **Component path:** `frontend/src/config.ts`, `frontend/src/features/details/`,
  `charts/topology-visualizer/templates/frontend-*.yaml`
- **Owning phase:** Post-Phase-5 extension

## 1. Context

The details panel says what a workload talked to. The next question an operator asks is *why* —
what the workload was doing at the time — and the answer is in the cluster's own metrics and logs,
which live in Grafana on every cluster this tool is meant for. Today the reader copies the
namespace and name out of the panel and types them into Grafana by hand.

The panel already holds everything a Grafana URL needs. A `GraphNode` carries `namespace`, `kind`
and `name` as fields (not to be parsed out of the id — `contracts/ids.md` §2, enforced by
`tests/id-opacity.test.ts`), and a `NodeDetail` carries the `window` the counts were computed
over. Turning those into a link is string formatting.

ADR-001 §13 deferred this as "optional Prometheus and Loki detail-panel integrations". Two things
are narrower here than that phrase allows. The link is **navigation**: the browser never queries
Grafana, embeds it, or shares a session with it. And what it links to is **the cluster's**
telemetry about the workload — cAdvisor, kube-state-metrics, Loki — not anything this tool
collects. Both are stated in the UI so the reader does not mistake the source.

## 2. Decision

**D-12.1 — Off unless `frontend.grafana.url` is set, and then never a dead link.** With no URL the
panel renders no Grafana section at all. With a URL, each button appears only when the link it
would carry is buildable (D-12.3): there is no disabled button and no link to a page that cannot
exist. Nothing is installed, fetched or embedded; the link opens in a new tab and Grafana's own
login applies. ADR-001 §4.2 holds — Grafana is not a dependency of anything.

**D-12.2 — Configuration reaches the browser as `/config.json`, from a ConfigMap.** The frontend
image is static nginx and must stay one image for any release (ADR-007 D-7.1). The chart renders
`frontend.grafana.*` into a ConfigMap mounted at `/usr/share/nginx/html/config.json`; the app
fetches it once at start-up, `Cache-Control: no-store`, and treats a missing or malformed file as
"not configured". A checksum annotation rolls the frontend when the values change, for the same
reason the agent has one. Not an environment variable through envsubst: a URL contains characters
nginx substitution would need escaping for, and JSON from `toJson` is correct by construction.

**D-12.3 — Which nodes get which link.** A workload dashboard needs a workload; a log query needs
a pod name pattern. Both derive from the node's fields, never its id.

| Node kind | Metrics | Logs | Why |
|---|---|---|---|
| `Deployment`, `StatefulSet`, `DaemonSet`, `Job` | dashboard with `var-namespace`, `var-type`, `var-workload` | Loki `{namespace="…", pod=~"<name>-.*"}` | a workload the dashboard knows and pods named after it |
| `Pod` (no recognised owner) | — | Loki `{namespace="…", pod="<name>"}` | one pod, exact match; no workload row exists |
| `Service` (ADR-009 D-9.2 fallback) | — | — | zero or several workloads behind it; picking one would invent a fact |
| `External`, unresolved | — | — | not in the cluster |

The metrics link targets a dashboard by UID with those three variables. The default UID is
kube-prometheus-stack's *Kubernetes / Compute Resources / Workload*, which takes exactly them;
`frontend.grafana.workloadDashboardUid` points it at any dashboard using the same variable names.
The logs link opens Grafana Explore on `frontend.grafana.lokiDatasourceUid`; no UID, no button.

**D-12.4 — Links carry the selected window.** Both URLs pass the `NodeDetail.window` as `from`/`to`
so Grafana opens on the same period the counts describe. The panel's counts and Grafana's graphs
then answer the same question about the same minutes. Before the detail has loaded, the last hour.

**D-12.5 — The UI names the source.** One line under the buttons: *the cluster's own metrics and
logs for this workload, over the selected window — not collected by this tool.* PRODUCT.md's first
principle is that nothing in the UI implies more than was observed; a button that looks like it
opens *this tool's* view of the workload would break that in the other direction.

**D-12.6 — The pod-prefix match is a heuristic, and stated as one.** `pod=~"backend-.*"` also
matches a sibling named `backend-worker-…`. Matching on a pod label would be exact, but which
label a log shipper attaches is the operator's configuration, not this tool's knowledge. The prefix
works with any shipper; the overlap is recorded here and in `docs/limitations.md`. The regex is
escaped, so a name containing `.` does not become a wildcard.

## 3. Consequences

**Two clicks fewer per investigation, and the window travels with them.** That is the feature.

**No new request path.** `/config.json` is a static file nginx already serves under `location /`;
the only nginx change is a `no-store` header on it, so a values change is seen after the rollout
rather than after a browser cache expiry. The API, the backend and the contract are untouched.

**One image, any Grafana.** A URL change is a `helm upgrade`, not a rebuild.

**Not a session bridge.** The topology UI has no authentication of its own (`docs/limitations.md`
§4.4) and Grafana has; the new tab lands on Grafana's login if the reader is not already in.
Nothing here weakens either side.

**The default dashboard UID is a guess about the cluster.** It is right for kube-prometheus-stack
and wrong for anything else; the operator guide says so and names the value to change.

**`DetailsPanel` gains a prop.** `grafana: GrafanaConfig | null`, optional, defaulting to null so
every existing caller and test is unchanged.

## 4. Alternatives rejected

**Embed Grafana panels in the details panel (iframe).** Needs Grafana's `allow_embedding`, a shared
auth story, and a Grafana that is reachable from the browser on the same terms as this UI — three
things the operator has to get right for a panel that is one click away anyway. This is the
"integration" ADR-001 §13 deferred, and it stays deferred.

**Query Prometheus and Loki from this backend and render the numbers here.** Makes them
dependencies in fact (§4.2), turns this backend into a proxy for two other systems, and reproduces
what Grafana already draws better.

**Bake the Grafana URL into the image at build time.** One image per environment; ADR-007 D-7.1
decided against exactly that for the backend host.

**Derive the link from the node id.** The id is `k8s:<cluster>:<ns>:<kind>:<name>` and parsing it
is the trap `tests/id-opacity.test.ts` exists to catch; the fields are on the node.

**A per-Service link to whichever workload is behind it.** ADR-009 D-9.2 only emits a `Service`
node when the backing workload is unknown or ambiguous. There is nothing correct to link to.

## 5. Tests

| ID | Assertion | Where |
|---|---|---|
| T-12.1 | No config, or a config with no URL, yields no links for any node | `tests/grafanaLinks.test.ts` |
| T-12.2 | A workload node yields a metrics link carrying namespace, lower-cased type, name and the window; a `Pod` yields logs only with an exact match; `Service` and `External` yield nothing | `tests/grafanaLinks.test.ts` |
| T-12.3 | Regex metacharacters in a name are escaped in the Loki selector; a trailing slash on the URL is tolerated | `tests/grafanaLinks.test.ts` |
| T-12.4 | The panel renders the Grafana section only when it has a link; links open in a new tab with `rel="noopener noreferrer"`; the source line is present | `tests/DetailsPanel.test.tsx` |
| T-12.5 | A 404, a network error, malformed JSON, or a non-`http(s)` URL all parse to "not configured" | `tests/config.test.ts` |
| T-12.6 | The chart renders `config.json` from `frontend.grafana.*`, mounts it into the frontend, and the schema rejects a URL without an `http(s)` scheme | `scripts/verify-chart.sh` |

## 6. Tracker

- [x] **P6-F10** `config.ts`: fetch and parse `/config.json`, `useUiConfig` — D-12.2, T-12.5
- [x] **P6-F11** `grafanaLinks.ts`: node + config + window → links — D-12.3, D-12.4, D-12.6, T-12.1 – T-12.3
- [x] **P6-F12** `DetailsPanel`: Grafana section with source line — D-12.1, D-12.5, T-12.4
- [x] **P6-F13** nginx `no-store` on `/config.json` — D-12.2
- [x] **P6-K7** `frontend.grafana.*` values, schema, ConfigMap, mount and checksum — D-12.2, T-12.6
- [x] **P6-K8** `docs/operator-guide.md` section; `limitations.md` entry for the prefix heuristic; ADR-006, ADR-007, index and plan updated
