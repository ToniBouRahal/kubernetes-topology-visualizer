# ADR-009: Destinations Resolve to Workloads, Not Services

- **Status:** Accepted for implementation
- **Date:** 2026-09-12
- **Parent:** ADR-001 §5.2 (identity) · Source of truth §10
- **Amends:** ADR-002 D-2.4 (destination ladder), ADR-003 D-3.2 (which kinds appear in practice),
  `contracts/ids.md` §6
- **Component path:** `agent/internal/resolver/`
- **Owning phase:** Post-Phase-5 correction

## 1. Context

The destination ladder in ADR-002 D-2.4 resolves a destination to the **Service** in front of a
workload. The source ladder resolves to the **workload** itself and never to a Service. Both rules
are individually defensible, and together they produce a graph that cannot connect.

The demo topology is a chain: `frontend` calls `backend`, `backend` calls `redis`. What the system
reports is:

```text
k8s:…:demo:Deployment:frontend  →  k8s:…:demo:Service:backend      component 1
k8s:…:demo:Deployment:backend   →  k8s:…:data:Service:redis        component 2
```

`Service:backend` and `Deployment:backend` are different IDs, so the two observations never meet at
a shared node. Four nodes, two edges, two disconnected components — for a topology that is in
reality one path. Every multi-hop dependency in any cluster splits the same way, at every hop.

This is not a layout problem and no layout algorithm can repair it: the break is in the identity,
before the graph is ever drawn. It defeats the single question the tool exists to answer — *what
actually depends on what* — because a transitive dependency is exactly what the reader cannot see.

The Service was never the thing being depended on. It is a routing indirection: a ClusterIP and a
set of iptables rules with no process behind it. A dependency is on the workload that serves the
request. Recording the indirection as the endpoint records the address rather than the recipient.

## 2. Decision

**D-9.1 — A destination resolves to the workload that serves it.** The destination ladder follows
a Service through its EndpointSlices to the backing workload and returns that workload's identity,
by the same owner-reference collapse the source ladder already uses. Source and destination now
agree on what a node is, so an observation that ends at `backend` and one that starts at `backend`
share an ID and the graph connects.

**D-9.2 — A Service node is the honest fallback, not the normal case.** A `Service` identity is
still emitted when, and only when, the workload behind it cannot be determined:

| Condition | Result |
|---|---|
| The Service has exactly one distinct backing workload | that workload |
| The Service has no ready endpoints (scaled to zero, informer lag) | the Service |
| The Service's endpoints resolve to several distinct workloads | the Service |

The last row follows the rule already established for the ambiguous-Service case in
`contracts/ids.md` §6: preserving ambiguity is more honest than fabricating certainty. A Service
fronting two workloads is a real fan-out, and collapsing it onto an arbitrary one of them would
invent a dependency that was never observed.

**D-9.3 — `Service` stays in the six allowed kinds.** D-9.2 still emits it, so nothing changes in
the ID grammar, the wire schema, the database `CHECK` constraint, or the generated clients. This
ADR changes *which identity an observation is assigned*, not *what an identity may look like*.

**D-9.4 — The Service that carried the traffic is not recorded.** The wire contract has no
attribute channel on a node or an edge, so preserving "this went via Service `backend`" would mean
a new field in the batch envelope, the Pydantic models, `contracts/openapi.json`, the generated
TypeScript, the Go structs, and a column and migration on `edge_buckets`. That is disproportionate
to the defect being fixed, and ADR-001 §12 instruction 5 asks for the smallest implementation that
satisfies the decision. The fact is dropped deliberately and recorded here as a known loss, not
overlooked. Anyone who needs it should raise its own ADR.

## 3. Consequences

**The graph becomes connected and transitive dependencies become visible.** This is the point.
`frontend → backend → redis` is one path, and a reader can follow it.

**Fewer nodes.** Every Service that fronts exactly one workload stops being a node of its own. The
demo graph loses two nodes and the picture gets smaller rather than more complete-looking — which
is the correct direction, because the two nodes it loses carried no dependency information.

**The rendered ceiling moves in our favour.** `docs/limitations.md` §4.1 records that the UI stops
rendering usefully past ~300 edges. Collapsing the Service hop reduces node count on real
topologies without reducing what the graph says.

**"Which Service was used" is no longer answerable.** Stated in D-9.4. For a cluster that routes
several Services to one workload, the graph now shows the dependency but not the route.

**One more informer read per ClusterIP destination.** Resolving a ClusterIP now needs the Service's
endpoint addresses rather than stopping at the Service name. This is a cache read on the hot
resolution path; the agent's measured 35 MiB / 1,325 events-per-second headroom (ADR-001 §6
targets, `docs/evaluation/`) absorbs it, and `make experiments` re-measures it.

**Historical data spans the change.** Rows stored before this change carry Service target IDs;
rows after carry workload IDs. A diff whose baseline period predates the change and whose current
period follows it will report the Service edge as `REMOVED` and the workload edge as `NEW`, once,
for as long as the retention window holds both. This is accurate — the identities genuinely
differ — and it ages out. It is not worth a migration for a demonstrator with a bounded retention
window.

## 4. Alternatives rejected

**Merge the Service into its workload in the frontend only.** Leaves the contract untouched, but
the API keeps returning a disconnected graph, so every other consumer — `demo-verify`, the diff
endpoint, anything reading `/api/v1/graph` — still sees the split. It moves a correctness problem
into the presentation layer, where the next consumer will rediscover it.

**Draw a `Service → workload` link so the picture connects.** That link is read from declared
cluster configuration, not observed in the kernel. This project's entire claim is that it reports
what happened rather than what was declared (`README.md`, ADR-001 §1). Mixing one declared edge
into the graph to improve its shape would undermine the claim the graph exists to support.

**Leave it, and explain the split when presenting.** The split is defensible in a sentence, and
indefensible in a picture. The graph is the deliverable.

## 5. Tests

| ID | Assertion | Component |
|---|---|---|
| T-9.1 | A ClusterIP destination resolves to the backing workload, not the Service | agent |
| T-9.2 | A Service with no ready endpoints still resolves to the Service | agent |
| T-9.3 | A Service whose endpoints span several workloads resolves to the Service | agent |
| T-9.4 | A pod-IP destination behind one Service resolves to that pod's workload | agent |
| T-9.5 | Source and destination produce the SAME ID for the same workload — the property the whole ADR exists for | agent |
| T-9.6 | `make demo-verify` asserts `frontend → backend → redis` as one connected chain | demo |

## 6. Tracker

- [x] **P6-A1** `Caches.EndpointsForService` — the backing addresses of a Service — D-9.1
- [x] **P6-A2** `ResolveDestination` follows a ClusterIP to its workload — D-9.1
- [x] **P6-A3** Fallback to the Service on zero or ambiguous backing workloads — D-9.2
- [x] **P6-A4** `contracts/ids.md` §6 destination ladder rewritten — D-9.1, D-9.2
- [x] **P6-A5** Resolver tests T-9.1 – T-9.5 — D-9.1, D-9.2
- [x] **P6-A6** `scripts/demo-verify.sh` asserts the connected chain — T-9.6
