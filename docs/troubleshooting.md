# Troubleshooting

Symptoms, what actually causes them, and how to confirm it before changing anything.

Every entry here was hit during development. The ones that took longest were the ones where the
symptom pointed somewhere other than the cause, so each says how to *tell*, not just what to do.

---

## The graph is empty

An empty graph is the normal first state. Traffic has to happen, be aggregated (10 s), be delivered,
and be inside the selected window before anything appears — allow about 20 seconds.

**Confirm what's actually missing** by walking the pipeline outwards:

```bash
# 1. Is the agent seeing anything at all?
kubectl -n topology port-forward ds/topology-visualizer-agent 9090:9090
curl -s localhost:9090/metrics | grep -E 'raw_events_received|edges_flushed|unresolved'

# 2. Is the backend receiving batches?
kubectl -n topology logs -l app.kubernetes.io/component=backend | grep ingest

# 3. Is the window right?
curl -s 'localhost:8000/api/v1/graph?window=1h' | jq '.summary'
```

| what you see | what it means |
|---|---|
| `raw_events_received` is 0 | the BPF program is attached but nothing is connecting, or the workload uses connection pooling and opened its connections before the agent started |
| `raw_events_received` rises, `edges_flushed` is 0 | everything is being filtered — check `unresolved`, `filtered_infrastructure_port` and `filtered_foreign_node` below |
| `endpoints_unresolved` is most of the traffic | the informer cannot map the addresses; usually a pod that started seconds ago (see next section) |
| edges exist but the window shows nothing | the traffic is older than the window — widen it |

## Counts look too low, or a short-lived Job reports almost nothing

A pod's first connections are unresolvable until the agent's informer has seen its IP. A Job that
starts and immediately connects loses its opening seconds — measured at 13 of 20 connections when
connecting immediately, and 20 of 20 after a 25-second settle.

This is inherent to resolving identity from the API server. If you need an exact count, have the
workload wait before it starts connecting; `demo/demo-traffic.yaml` does exactly that.

## Counts look inflated — every edge is a multiple of the real number

On kind, and only on kind. The "nodes" are containers sharing one host kernel, so without filtering
every agent observes every connection cluster-wide and each is counted once per node.

```bash
curl -s localhost:9090/metrics | grep foreign_node
# topology_agent_events_filtered_foreign_node_total 304
```

A non-zero value means cross-node observation is happening and being filtered correctly. On a real
cluster, where each node has its own kernel, this reads **zero**. If it reads zero on kind, the
filter is not working and counts will be inflated by the node count.

## Edges that make no sense — `etcd → coredns`, `kube-apiserver → agent`

Kubelet health probes originate from the **node** address, and every host-network pod carries that
same address as its PodIP. Resolving it against the pod cache returns an arbitrary one of them.

Node addresses must resolve to `host` and be excluded. If you see these edges, source resolution is
consulting the pod cache before checking whether the address belongs to a Node.

## The agent will not start

| error | cause | fix |
|---|---|---|
| `remove memlock rlimit (needs CAP_SYS_RESOURCE or a privileged container)` | not privileged | `agent.privileged=true`; there is no unprivileged configuration that can load BPF |
| `load BPF objects (check BTF at /sys/kernel/btf/vmlinux …)` | kernel has no BTF | needs Linux 5.8+ built with `CONFIG_DEBUG_INFO_BTF`; there is no fallback |
| `in-cluster config (the agent must run as a pod with a ServiceAccount)` | run outside Kubernetes | it is a DaemonSet, not a CLI |
| `BACKEND_INGEST_URL is required` | missing configuration | set it; there is deliberately no default, because an agent delivering nowhere looks exactly like a quiet cluster |

## The backend crash-loops on startup

```
ConnectionError: could not connect to PostgreSQL at postgresql://topology:***@…: gaierror
```

`gaierror` is DNS, not the database. Check that PostgreSQL is running **and** that cluster DNS
works — those are different failures with the same symptom:

```bash
kubectl -n topology get pods -l app.kubernetes.io/component=database
kubectl -n kube-system get pods -l k8s-app=kube-dns
kubectl run dns --rm -it --image=busybox:1.36 --restart=Never -- \
  nslookup topology-visualizer-postgresql.topology.svc.cluster.local
```

A backend that starts while the database is unreachable exits rather than starting unready. That is
deliberate: an instance that has never connected cannot know whether its migrations have run, and
serving an empty graph from an unmigrated database is worse than failing loudly. A **running**
backend behaves differently — it degrades to a 503 readiness and recovers, without restarting.

## CoreDNS says `Unauthorized` and everything breaks at once

If the host clock jumps **backwards** — a laptop resuming from suspend, an NTP correction — every
ServiceAccount token becomes invalid, because they are JWTs whose `iat`/`nbf` claims are now in the
future. CoreDNS loses its watches, DNS stops resolving, and every component that needs another pod
fails at the same moment.

Tell-tale signs, none of which point at the clock:

```bash
kubectl get pods -A          # restart ages show "<invalid>"
kubectl -n kube-system logs -l k8s-app=kube-dns | grep Unauthorized
```

Recovery is to restart CoreDNS and then the workloads, which reissues tokens against the corrected
clock:

```bash
kubectl -n kube-system rollout restart deploy/coredns
kubectl -n topology rollout restart ds/topology-visualizer-agent \
  deploy/topology-visualizer-backend deploy/topology-visualizer-frontend
```

## The UI hangs on a large graph

Past roughly 300 edges the interface does not slow down — it stops. 172 nodes / 1,002 edges did not
paint within 250 seconds. This is a known limitation, not a misconfiguration; see
[`limitations.md`](limitations.md) §4.1. Narrow by namespace or shorten the window.

## The UI shows a stale graph with a banner

> Showing the last successful reading. The backend is not responding…

The last good topology deliberately stays on screen rather than being replaced by an error. The
backend is unreachable — usually restarting. Check `kubectl -n topology get pods`.

## `kind` cluster problems

| symptom | cause |
|---|---|
| `too many open files`, kube-proxy crash-looping | host inotify limits below kind's minimum — `make preflight` checks this |
| NetworkPolicies appear to do nothing | kind's default CNI ignores them entirely; this is expected, see `limitations.md` §3.3 |
| an image "exists" but a clean machine fails | `kind load` side-loads; an image with `imageID: import-…` was never pulled. `make verify-db-image` forces the registry path |

## Confirming privacy properties

```bash
# Raw address logging must be off unless explicitly enabled
kubectl -n topology logs -l app.kubernetes.io/component=agent | grep debug_raw_events
# "debug_raw_events": false

# No individual external IP is ever stored or returned
curl -s 'localhost:8000/api/v1/graph?window=1h' | jq '[.nodes[] | select(.kind=="External")]'
# exactly one node, named EXTERNAL
```
