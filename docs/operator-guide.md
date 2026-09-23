# Operator guide

Installing, configuring and running the system somewhere other than the kind demo.

For what it cannot do, read [`limitations.md`](limitations.md) first — several entries there change
whether this is the right tool for a given cluster.

---

## Requirements

| | |
|---|---|
| Kernel | Linux 5.8+ with BTF at `/sys/kernel/btf/vmlinux` (`CONFIG_DEBUG_INFO_BTF`). No fallback exists. |
| Kubernetes | 1.28+. The agent uses EndpointSlices, not the deprecated Endpoints API. |
| Privileges | The agent runs privileged. Nothing else does. |
| Storage | A PVC for the in-cluster database, or an external PostgreSQL. |

`make preflight` checks the kernel, BTF and host inotify limits before creating a cluster.

## Installing

```bash
helm install topology charts/topology-visualizer \
  --namespace topology --create-namespace \
  --set clusterId=prod-eu-west-1 \
  --set postgresql.enabled=true \
  --set postgresql.auth.password="$(openssl rand -base64 24)"
```

`clusterId` is **not** cosmetic: it is part of every node identity, so changing it later makes all
existing history unreachable. It may not contain a colon, and the schema rejects one that does.

### Pointing at an existing database

```bash
kubectl create secret generic topology-db --from-literal=database-url='postgresql://user:pw@host:5432/topology'

helm install topology charts/topology-visualizer \
  --namespace topology --create-namespace \
  --set clusterId=prod-eu-west-1 \
  --set postgresql.enabled=false \
  --set externalDatabaseUrlSecret=topology-db
```

Migrations run at backend startup and the backend refuses to serve until they have. A DSN never
appears in a log line or an API response — it is redacted to `postgresql://user:***@host`.

## Configuration worth knowing about

| value | default | why you might change it |
|---|---|---|
| `clusterId` | `kind-topology` | **Set this.** It is part of every identity. |
| `agent.flushIntervalSeconds` | `10` | Longer means fewer, larger batches and a slower graph. |
| `agent.infrastructurePorts` | control-plane ports | Ports excluded as infrastructure noise rather than topology. |
| `agent.debugRawEvents` | `false` | **Leave off.** Logs source and destination addresses, including external ones. |
| `backend.retentionHours` | `1440` (2 months) | Storage grows with distinct edges × buckets, not with traffic volume — see Capacity for the per-edge cost and the volume size it implies. |
| `networkPolicy.enabled` | `true` | Verified under Calico. Inert on a CNI that ignores NetworkPolicy. |
| `backend.replicaCount` | `1` | The schema rejects more; horizontal scaling is untested, not forbidden. |

## Verifying an install

```bash
make agent-verify      # an agent pod Running on every node, including control-plane
make verify-db-image   # the database image PULLS rather than relying on a side-loaded copy
make demo-verify       # expected edges present, replicas collapsed, counts exact
```

An agent missing from one node means that node's traffic is invisible, and the graph will look
plausible while being incomplete — which is why coverage is asserted rather than assumed.

## Day-to-day operation

### What to watch

```bash
kubectl -n topology port-forward ds/topology-visualizer-agent 9090:9090
curl -s localhost:9090/metrics
```

| metric | what a bad value means |
|---|---|
| `topology_agent_kernel_samples_lost_total` | **> 0 is data loss.** The ring buffer overflowed; connections silently never appeared. |
| `topology_agent_endpoints_unresolved_total` | High relative to observations: the informer cannot map addresses — often churn, sometimes RBAC. |
| `topology_agent_batches_dropped_total` | The delivery queue overflowed; the backend was unreachable for longer than the queue could absorb. |
| `topology_agent_events_filtered_foreign_node_total` | Should be **0** on a real cluster. Non-zero means nodes share a kernel (kind), and counts would otherwise be inflated. |
| `topology_agent_delivery_queue_depth` | Persistently rising: delivery is slower than aggregation. |

The backend exposes `/health/live` and `/health/ready`. Readiness reflects migrations *and* storage,
so it drops to 503 during a database outage and recovers without a restart.

### Prometheus and Grafana (optional)

The port-forward above is the zero-dependency way to read the metrics, and it stays the default.
If the cluster already runs the Prometheus Operator and a Grafana with the dashboard sidecar — the
kube-prometheus-stack shape — the chart can hand them the endpoints instead
([ADR-011](adr/ADR-011-optional-prometheus-grafana.md)):

```bash
helm upgrade --install topology charts/topology-visualizer -n topology \
  --set monitoring.enabled=true \
  --set monitoring.monitorLabels.release=kube-prometheus-stack   # whatever your Prometheus selects on
```

That renders a `PodMonitor` for the agent (one target per node), a `ServiceMonitor` for the
backend, and a ConfigMap carrying the **Topology Visualizer — pipeline health** dashboard, which
the sidecar imports within its poll interval. With `networkPolicy.enabled`, the backend policy also
admits scrapes from `monitoring.prometheusNamespace` (default `monitoring`); without that rule an
enforcing CNI would show the backend as `up == 0`.

The dashboard is the table above over time and per node — whether the graph can be trusted right
now — not the topology. The graph stays the only place the topology is drawn. Nothing installs
Prometheus or Grafana: if the CRDs are absent, the install fails with `no matches for kind
"PodMonitor"`, which is the intended message rather than a scrape that silently never happens.

### Grafana links from the details panel (optional)

The other direction: selecting a workload in the UI offers **Metrics** and **Logs** buttons that
open the cluster's own Grafana for that workload, over the window the panel is showing
([ADR-012](adr/ADR-012-grafana-deep-links.md)). Navigation only — nothing is embedded or queried,
the tab lands on Grafana's own login if you are not signed in, and the panel says the numbers
there are the cluster's, not this tool's.

```bash
helm upgrade --install topology charts/topology-visualizer -n topology \
  --set frontend.grafana.url=https://grafana.example.com \
  --set frontend.grafana.lokiDatasourceUid=<uid>          # omit for no Logs button
```

*Metrics* opens `frontend.grafana.workloadDashboardUid` with `var-namespace`, `var-type` and
`var-workload`. The default UID is kube-prometheus-stack's *Kubernetes / Compute Resources /
Workload*; on any other Grafana, set it to a dashboard that uses those three variable names.
*Logs* opens Explore on the Loki datasource with `{namespace="…", pod=~"<workload>-.*"}` — a
prefix match, so a workload named `backend` also matches `backend-worker` pods
(`limitations.md`). Standalone pods get an exact-match logs link and no metrics link; `Service`
and `EXTERNAL` nodes get neither, because there is no single workload behind them.

The values reach the browser as `/config.json` from a ConfigMap; a change is a `helm upgrade`,
which rolls the frontend, not an image rebuild.

### Capacity

Storage is proportional to **distinct edges × buckets retained**, not to traffic volume: a busy edge
and a quiet one occupy the same row. One minute-bucket per edge per minute, `retentionHours` deep.

Measured on the running demo rather than estimated — 190-byte average tuple, and about 700 bytes a
row once the primary key and three indexes are counted:

```text
≈ 1 MB per distinct edge per day      (1,440 minute-buckets × ~700 B)
≈ 60 MB per distinct edge for the 1440-hour default
```

So the shipped 2Gi database volume holds roughly **30 distinct edges for two months**, or 900 edges
for a day. The demo topology has 8. Past that, raise `postgresql.persistence.size` — and raise it
*before* installing: a StatefulSet's `volumeClaimTemplates` are immutable, so changing the value on
a running release does nothing and the volume can only grow by recreating it.

Only edges that actually carried traffic in a minute occupy a bucket for it, so an idle cluster
costs nothing and the figures above are the busy-case ceiling.

Measured on the demo cluster: agent ~35 MiB RSS per node, 1,325 events/s sustained with no kernel
drops, graph query p95 62 ms at 500 nodes / 2,000 edges.

**The interface is the binding constraint, not the backend.** Past roughly 300 edges the UI stops
rendering (`limitations.md` §4.1). On a cluster larger than that, the API remains usable while the
supplied UI does not.

## Security posture

- One privileged container (the agent). The chart asserts the count, so a second cannot appear unnoticed.
- Agent RBAC is `get`/`list`/`watch` only. Backend and frontend mount no ServiceAccount token at all.
- Backend, frontend and database run non-root with `RuntimeDefault` seccomp and all capabilities dropped.
- Two NetworkPolicies: ingest reachable only from agent and frontend pods, the database only from the backend.
- No packet payload is read. No individual external IP is persisted or returned.
- Database credentials come from a Secret; a DSN never reaches a log or a response.

The agent is exempt from seccomp deliberately — `RuntimeDefault` restricts `bpf()` and
`perf_event_open()`, which is what it exists to call. Do not "fix" this; the chart asserts its absence.

## Upgrading

```bash
helm upgrade topology charts/topology-visualizer -n topology --reuse-values
```

History survives: it lives in PostgreSQL, keyed by identities that do not change across restarts.
Verified by pinning the *oldest* bucket and confirming it is unchanged — new traffic lands in recent
buckets, so a total alone cannot distinguish growth from double-counting.

Do not change `clusterId` on an upgrade. Existing rows carry the old value and become unreachable.

## Uninstalling

```bash
helm uninstall topology -n topology
kubectl delete pvc -n topology -l app.kubernetes.io/name=topology-visualizer   # history, deliberately kept by default
```

`helm uninstall` leaves the PVC, so history survives a reinstall. Delete it explicitly when you mean
to lose the data.


## Upgrading for connection outcomes

Deploy the updated backend first. Migration `002_connection_outcomes.sql` adds nullable failure
counts and zero-sample timing columns; historical rows remain unmeasured. Existing agent batches
are accepted. Then roll out the updated agents and frontend. New agents emit additive fields in
schema version 1 that older backends reject, so do not upgrade agents first or roll the backend
back while those agents are active.

Monitor `topology_agent_setup_tracking_missed_total` (a terminal outcome without a recorded start,
including eviction or attachment boundaries) and `topology_agent_setup_tracking_failed_total`
(failed tracking map updates), alongside `topology_agent_kernel_samples_lost_total`. Missing starts
remove timing samples but do not suppress terminal outcome counts. Setup tracking uses a bounded
65,536-entry LRU map per agent.

The UI says failed/aborted deliberately: cancellation and failure share the same terminal state.
Inspect application logs or other network diagnostics for a cause. Successful setup timing includes
only measured samples; it is neither an HTTP latency metric nor a failure-rate denominator.
