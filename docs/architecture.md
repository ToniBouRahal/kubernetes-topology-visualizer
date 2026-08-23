# Architecture

How a TCP connection inside the cluster becomes an edge on a screen, and why each boundary is
where it is.

Design decisions live in `docs/adr/`; this describes what was built. Measured limits are in
[`limitations.md`](limitations.md).

---

## The system

```mermaid
flowchart TB
    subgraph node1["Node (one of N)"]
        direction TB
        wl1["Application pods<br/><i>unmodified, uninstrumented</i>"]
        kern1["Linux kernel<br/>tracepoint/sock/inet_sock_set_state"]
        agent1["topology-agent<br/><i>DaemonSet, privileged</i>"]
        wl1 -.->|"opens a TCP connection"| kern1
        kern1 -->|"ring buffer"| agent1
    end

    subgraph cp["Control plane"]
        api["kube-apiserver"]
    end

    subgraph backend_box["topology namespace"]
        be["backend<br/><i>FastAPI, 1 replica</i>"]
        db[("PostgreSQL<br/><i>StatefulSet + PVC</i>")]
        fe["frontend<br/><i>nginx + React</i>"]
    end

    user(["Browser"])

    agent1 -->|"get/list/watch<br/>pods, services, endpointslices"| api
    agent1 -->|"POST /api/v1/ingest/batches<br/>every 10s"| be
    be <-->|"asyncpg"| db
    user -->|"HTTP"| fe
    fe -->|"/api proxy"| be

    classDef priv fill:#3b1f1f,stroke:#c26,stroke-width:2px,color:#eee
    classDef store fill:#1f2b3b,stroke:#48a,color:#eee
    class agent1 priv
    class db store
```

The agent is the only privileged component and the only one that touches the kernel. It holds no
database credentials and its Kubernetes access is read-only. Everything downstream of it works with
already-resolved workload identities, never raw addresses.

## From a connection to an edge

```mermaid
sequenceDiagram
    autonumber
    participant App as Application pod
    participant K as Kernel tracepoint
    participant C as Collector (eBPF)
    participant R as Resolver (informers)
    participant A as Aggregator
    participant B as Backend
    participant D as PostgreSQL
    participant U as Browser

    App->>K: connect() → SYN_SENT → ESTABLISHED
    Note over K: Filtered in-kernel:<br/>AF_INET, TCP, and the<br/>active-open transition only
    K->>C: event {src, dst, dport} via ring buffer
    Note over C: No payload is ever read

    C->>R: resolve source IP, destination IP:port
    R-->>C: workload / Service identity
    Note over R: Node IP → host (excluded)<br/>Pod IP → owner walk → Deployment<br/>ClusterIP → Service

    C->>A: observation
    Note over A: Keyed by source · target ·<br/>protocol · port — counted, not stored

    loop every 10s
        A->>B: POST batch {batch_id, edges[]}
        B->>D: one transaction, upsert by batch_id
        Note over B,D: Replaying a batch_id changes nothing
        B-->>A: 202 new / 200 already ingested
    end

    U->>B: GET /api/v1/graph?window=5m
    B->>D: select buckets in [start, end)
    D-->>B: edges
    B-->>U: nodes derived from edges + summary
```

The two properties worth following through that diagram:

**Filtering happens as early as possible.** The four-condition check runs in the kernel, so a pod
receiving a connection never produces an event at all — which is why the graph shows no false
reverse edges. Payload is never read anywhere in the path.

**Identity is resolved at the edge, not in the database.** By the time an observation leaves the
agent it names a Deployment or a Service, never an IP. External traffic has already collapsed to a
single `EXTERNAL` node, so no individual external address is ever transmitted or stored.

## Where identity comes from

```mermaid
flowchart LR
    ip["Observed address"] --> q1{"A Node address?"}
    q1 -->|yes| host["host<br/><i>excluded from the graph</i>"]
    q1 -->|no| q2{"A ClusterIP?"}
    q2 -->|yes| svc["that Service"]
    q2 -->|no| q3{"A Pod IP?"}
    q3 -->|no| q4{"Routable off-cluster?"}
    q4 -->|yes| ext["external:EXTERNAL<br/><i>address discarded</i>"]
    q4 -->|no| unres["unresolved<br/><i>counted, not shown</i>"]
    q3 -->|yes| q5{"Backed by a Service<br/>on this port?"}
    q5 -->|"exactly one"| svc
    q5 -->|"several"| wl2["the workload<br/><i>candidates kept as metadata</i>"]
    q5 -->|none| wl["owner walk →<br/>Deployment / StatefulSet /<br/>DaemonSet / Job / Pod"]

    classDef drop fill:#2b2b2b,stroke:#777,color:#bbb
    class host,unres drop
```

Two rules in that ladder exist because of specific failures:

**Node addresses are checked first.** Every host-network pod carries the node's address as its
PodIP — on a control-plane node that is etcd, kube-apiserver, kube-scheduler, kube-proxy and the CNI
agent all at once, plus the kubelet. Looking that address up in the pod cache returns an arbitrary
one of them, which produced edges like `etcd → coredns:8080`. A node address identifies the node,
not a process on it.

**Ambiguity is preserved.** Where several Services select the same pod and port, the result is the
workload with the candidates as metadata — not a guess.

## Why these boundaries

| boundary | reason |
|---|---|
| kernel filter before userspace | an event never created costs nothing to discard, and the active-open condition is what prevents false reverse edges |
| identity resolved in the agent | the backend never sees an IP, so no address can leak into storage or an API response |
| aggregation before delivery | one edge per 10s interval instead of one message per connection |
| `batch_id` idempotency | a retried delivery cannot double-count, which is what makes at-least-once delivery safe |
| one-minute buckets in the domain layer | the in-memory and PostgreSQL adapters produce identical buckets, so the contract suite can run against both |
| nodes derived from edges at query time | a node cannot appear in a window where it had no traffic |

## Deployment shape

```mermaid
flowchart TB
    subgraph ns["namespace: topology"]
        ds["DaemonSet: agent<br/>privileged · hostPID<br/>RBAC: get/list/watch"]
        dep["Deployment: backend<br/>non-root · read-only rootfs<br/>no ServiceAccount token"]
        fe["Deployment: frontend<br/>non-root · read-only rootfs<br/>no ServiceAccount token"]
        sts["StatefulSet: postgresql<br/>non-root · PVC<br/>password from Secret"]
        np1["NetworkPolicy<br/>ingest ← agent, frontend"]
        np2["NetworkPolicy<br/>5432 ← backend"]
    end
    ds --> dep
    fe --> dep
    dep --> sts
    np1 -.->|guards| dep
    np2 -.->|guards| sts
```

Exactly one workload is privileged, and the chart asserts that. The backend and frontend mount no
ServiceAccount token at all. NetworkPolicies are on by default and were verified under Calico —
kind's default CNI ignores them, which is why they are off in the kind demo values rather than
implying a protection that cluster does not provide.
