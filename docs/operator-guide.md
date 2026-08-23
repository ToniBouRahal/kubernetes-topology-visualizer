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
| `backend.retentionHours` | `24` | Storage grows with distinct edges × buckets, not with traffic volume. |
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

### Capacity

Storage is proportional to **distinct edges × buckets retained**, not to traffic volume: a busy edge
and a quiet one occupy the same row. One minute-bucket per edge per minute, `retentionHours` deep.

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
