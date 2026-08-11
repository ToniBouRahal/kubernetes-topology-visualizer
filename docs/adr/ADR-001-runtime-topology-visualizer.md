# ADR-001: Architecture for Dynamic Service Topology Visualization in Kubernetes Environments

- **Status:** Accepted for implementation
- **Date:** 2026-08-10
- **Decision owners:** FYP project team
- **Implementation target:** `/home/it-laptop/kubernetes-topology-visualizer`
- **Reference prototype:** `/home/it-laptop/claude-test-fyp/kubernetes-topology-ebpf-demo`
- **Authoritative scope:** `/home/it-laptop/claude-test-fyp/FYP_Project_Scope_Source_of_Truth.md`

## 1. Decision summary

Build a self-hosted Kubernetes topology visualizer that discovers actual TCP communication at runtime rather than inferring communication only from manifests.

The system will use:

- a privileged Go agent deployed as a DaemonSet to collect IPv4 TCP connection events with eBPF;
- Kubernetes informers in the agent to resolve network endpoints to namespaces, workloads, pods, and Services;
- batched, idempotent HTTP ingestion into a Python FastAPI backend;
- PostgreSQL for durable topology history and comparison;
- a React, TypeScript, and React Flow frontend for an interactive, time-windowed graph;
- a Helm chart for Kubernetes installation, with `kind` values for repeatable demonstration.

The first release is a single-cluster observability tool. It must prove that live workload-to-workload and workload-to-external TCP relationships can be collected, persisted, queried, and visualized with a reproducible local demo.

## 2. User-visible outcome

After installation, a user can open a browser and see a directed graph of recent Kubernetes communication. The user can:

- select a preset or custom historical time window;
- compare a baseline period with a current period and see `NEW`, `REMOVED`, and `CHANGED` edges;
- filter by namespace and workload;
- search for a node;
- inspect an edge to see protocol, destination port, connection count, first seen, and last seen;
- distinguish Services, logical workloads, standalone pods, and the summarized `EXTERNAL` destination;
- pause or refresh live updates;
- generate known demo traffic and see the expected topology appear without editing application manifests or adding sidecars.

## 3. Context

Kubernetes manifests describe desired resources, but they do not show which components communicate at runtime. Logs and metrics can expose fragments of this behavior, while service meshes usually require proxies or application changes. The project needs a low-instrumentation method that works across workloads and produces an understandable service dependency graph.

The reference prototype already proves the basic data path:

```text
Linux TCP state transition
  -> eBPF program
  -> Go node agent
  -> Kubernetes IP resolution
  -> FastAPI ingestion
  -> React Flow graph
```

`FYP_Project_Scope_Source_of_Truth.md` is authoritative. If this ADR and that document ever conflict, the source-of-truth document wins until the ADR is amended. This ADR converts that product scope into concrete implementation decisions; it does not narrow away PostgreSQL, historical comparison, Helm packaging, or multi-node validation.

The prototype is not the final implementation because it:

- treats all unknown addresses as one `EXTERNAL` node;
- resolves primarily to pod or Service names, not stable workload identities;
- stores all graph state in backend memory;
- has no retry-safe ingestion contract;
- may count server-side TCP state transitions as reverse traffic;
- resets an aggregation batch before delivery is confirmed;
- has no API versioning, pagination, schema migration, or retention job;
- allows unrestricted CORS;
- uses scattered manifests and manual build/deploy commands;
- has no automated tests or CI acceptance path.

## 4. Scope

### 4.1 In scope

- One Kubernetes cluster per installation.
- Linux worker nodes with BTF and eBPF support.
- Runtime TCP connection establishment events for IPv4.
- Directional client-to-server edges.
- Resolution of pods to their top-level workload owner where possible:
  `Deployment`, `StatefulSet`, `DaemonSet`, `Job`, or standalone `Pod`.
- Resolution of destination pod IPs through EndpointSlices to Kubernetes Services.
- External destinations aggregated into one `EXTERNAL` node.
- Durable historical storage, configurable retention, and custom time-range queries.
- Deterministic topology comparison across two selected periods.
- Interactive topology, filters, node/edge detail, traffic intensity, health state, and empty/error states.
- A deterministic `kind` demo with multiple namespaces and known traffic paths.
- Unit, API, frontend, and end-to-end smoke tests.
- Technical documentation sufficient to install, operate, demonstrate, and evaluate the project.

### 4.2 Out of scope for the accepted release

- Packet payload capture or deep packet inspection.
- HTTP paths, headers, status codes, TLS contents, or DNS-derived domain attribution.
- UDP, SCTP, ICMP, or non-IP Unix socket traffic.
- Multi-cluster aggregation.
- Historical distributed tracing and request latency.
- Automatic network-policy generation or enforcement.
- Hosted SaaS, user billing, or multi-tenancy.
- High-availability backend replicas.
- Windows nodes.
- IPv6 in the initial release.
- Prometheus or Loki as mandatory dependencies; they remain optional future enrichments.

These are possible extensions, but the implementation agent must not add them to the core phases.

## 5. Architectural decisions

### 5.1 Collection: eBPF node agent

Deploy one Go agent per Linux node as a Kubernetes DaemonSet. Compile the BPF programs with `bpf2go` from `github.com/cilium/ebpf` and use CO-RE-compatible kernel types where practical.

Start from `tracepoint/sock/inet_sock_set_state`, because the prototype has validated it on the target machine. Emit only active-open connections by filtering transitions from `TCP_SYN_SENT` to `TCP_ESTABLISHED`. This prevents an accepted server socket from becoming a false reverse edge. Support `AF_INET` with a versioned event structure. IPv6 is a deferred extension.

Each raw event contains:

- monotonic kernel timestamp;
- address family;
- source and destination addresses;
- source and destination ports;
- transport protocol;
- process ID when available;
- node identity.

The BPF program must not inspect or copy payload bytes.

The Go agent will:

1. load and attach the BPF program;
2. consume events from a ring buffer on the required Linux 6.8+ development and validation environments;
3. discard configured infrastructure ports and invalid events;
4. resolve both endpoints from informer-maintained caches;
5. normalize endpoints to stable graph node identifiers;
6. aggregate identical edges for a configurable flush interval;
7. send batches with retry and bounded local buffering;
8. expose health and Prometheus-format internal metrics on a container port.

The agent must use exponential backoff with jitter. A batch is removed from memory only after a successful `2xx` response. The queue must be bounded; on overflow, drop the oldest batch and increment a visible dropped-batch metric. The backend must deduplicate retried batches.

### 5.2 Kubernetes metadata and graph identity

Use shared informers for:

- Pods;
- ReplicaSets;
- Deployments;
- StatefulSets;
- DaemonSets;
- Jobs;
- Services;
- EndpointSlices;
- Namespaces.

Follow owner references from Pod to the stable top-level workload. In particular, collapse `Pod -> ReplicaSet -> Deployment` to the Deployment.

Canonical node IDs are opaque, deterministic strings:

```text
k8s:<cluster_id>:<namespace>:<kind>:<name>
external:EXTERNAL
```

Allowed Kubernetes node kinds are `Service`, `Deployment`, `StatefulSet`, `DaemonSet`, `Job`, and `Pod`. A pod with no recognized owner remains a `Pod` node.

Endpoint normalization rules:

- Source: resolve the source pod IP, then collapse it to the owning workload.
- Destination: if the destination pod IP belongs to one or more EndpointSlices, select the Service whose declared port maps to the observed target port; otherwise collapse the destination pod to its owning workload.
- ClusterIP: map directly to its Service when present in the Service cache.
- Node/host traffic: classify as `host` metadata and exclude it from the default application graph.
- Unknown routable IP: classify as the single summarized `EXTERNAL` node without persisting the remote IP as graph identity.
- Unknown private/cluster IP: classify as unresolved and expose an unresolved counter; do not silently call it external.

If Service selection is ambiguous, retain the destination workload and attach candidate Service names as metadata. Never choose a Service arbitrarily.

The graph API returns display labels separately from canonical IDs. Renaming a workload creates a new identity; pod replacement under the same workload does not.

### 5.3 Event and ingestion contract

Expose versioned endpoints under `/api/v1`.

The agent sends a batch shaped as follows:

```json
{
  "schema_version": 1,
  "cluster_id": "kind-topology",
  "agent_id": "topology-agent/kind-worker",
  "batch_id": "01J...ULID",
  "observed_at": "2026-08-10T12:00:00Z",
  "interval_seconds": 10,
  "edges": [
    {
      "source": {
        "id": "k8s:kind-topology:demo:Deployment:client",
        "kind": "Deployment",
        "namespace": "demo",
        "name": "client"
      },
      "target": {
        "id": "k8s:kind-topology:demo:Service:backend",
        "kind": "Service",
        "namespace": "demo",
        "name": "backend"
      },
      "protocol": "TCP",
      "destination_port": 8080,
      "connection_count": 30,
      "first_seen": "2026-08-10T11:59:51Z",
      "last_seen": "2026-08-10T12:00:00Z"
    }
  ]
}
```

Contract rules:

- `batch_id` is unique per agent batch and is the idempotency key.
- Timestamps use RFC 3339 UTC.
- `connection_count` is a positive integer.
- `bytes_sent` and `bytes_received` are optional non-negative integers and remain absent until byte accounting passes the Phase 4 feasibility gate.
- One edge key is `(cluster_id, source_id, target_id, protocol, destination_port)`.
- Reject malformed batches with `422`; reject unsupported schema versions with `400`.
- Return `202` for a new batch and `200` for an already-ingested batch.
- Apply explicit request-body and edge-count limits.
- Do not accept arbitrary node kinds or protocols.

Required API routes:

```text
GET  /health/live
GET  /health/ready
POST /api/v1/ingest/batches
GET  /api/v1/graph
GET  /api/v1/diff
GET  /api/v1/namespaces
GET  /api/v1/nodes/{node_id}
GET  /api/v1/edges/{edge_id}
GET  /metrics
```

`GET /api/v1/graph` accepts:

- either `window` (`1m`, `5m`, `15m`, `1h`, `6h`, or `24h`) or explicit RFC 3339 `from` and `to` values;
- repeated `namespace` values;
- optional `kind`;
- optional `query` substring;
- `include_external`, default `true`;
- `include_unresolved`, default `false`.

`GET /api/v1/diff` requires `baseline_from`, `baseline_to`, `current_from`, and `current_to`, plus the same topology filters. For each logical edge it returns:

- `NEW` when present only in the current period;
- `REMOVED` when present only in the baseline period;
- `CHANGED` when present in both and the absolute percentage change in connection count or byte volume meets a configurable threshold;
- unchanged edges only when `include_unchanged=true`.

Diff results include baseline and current values, absolute delta, percentage delta when defined, and the exact classification reason. A missing period is treated as zero, not as missing data. Reject overlapping inverted ranges and ranges beyond the configured maximum query span.

Graph and diff responses include `generated_at`, effective filters, nodes, edges, and summary counters. Publish an OpenAPI document from FastAPI and treat it as the frontend/backend contract.

### 5.4 Storage and retention

Use PostgreSQL as the final FYP database. Deploy it inside Kubernetes for the self-contained demonstration, backed by a PersistentVolumeClaim. Keep database access behind repository interfaces so the POC can use an in-memory adapter before PostgreSQL is introduced.

Minimum schema:

```text
ingest_batches(
  batch_id PRIMARY KEY,
  cluster_id,
  agent_id,
  observed_at,
  received_at
)

nodes(
  id PRIMARY KEY,
  cluster_id,
  kind,
  namespace NULL,
  name,
  attributes_json,
  first_seen,
  last_seen
)

edge_buckets(
  bucket_start,
  cluster_id,
  source_id,
  target_id,
  protocol,
  destination_port,
  connection_count,
  bytes_sent NULL,
  bytes_received NULL,
  first_seen,
  last_seen,
  PRIMARY KEY(
    bucket_start, cluster_id, source_id, target_id,
    protocol, destination_port
  )
)
```

Aggregate into one-minute buckets inside a transaction. Record `batch_id` in the same transaction as edge updates. This makes agent retries idempotent.

Default retention is 24 hours and is configurable. A periodic backend task removes expired edge buckets and nodes no longer referenced inside the retention window. Run migrations explicitly at backend startup and fail readiness if migrations or storage checks fail.

Use PostgreSQL constraints and `INSERT ... ON CONFLICT` within a transaction to merge one-minute buckets and record the batch id atomically. Index time range, namespace-relevant node lookups, source ID, and target ID. Store database credentials in a Kubernetes Secret, never in committed values files. The Helm chart must support an internal PostgreSQL subchart for the demo and an external `DATABASE_URL` for an existing PostgreSQL instance.

Byte fields remain nullable until the Phase 4 feasibility spike proves reliable accounting. Connection count is always available and is the fallback traffic-intensity measure.

### 5.5 Backend

Keep FastAPI and Pydantic. Separate the application into API, domain, and persistence layers; do not retain the prototype's single-file layout.

Required backend behavior:

- validate and ingest batches transactionally;
- aggregate requested time buckets into graph edges;
- compute deterministic topology diffs for two explicit periods;
- derive graph nodes only from edges in the selected window;
- return deterministic ordering for nodes and edges;
- cap graph responses and return a clear truncation indicator;
- implement structured JSON logging with request IDs;
- expose counts for ingested batches, rejected batches, graph query duration, stored edge buckets, and retention deletions;
- restrict CORS to configured origins;
- support graceful shutdown;
- never expose raw stack traces in API responses.

Use dependency injection for storage and clock access so tests do not depend on wall-clock timing.

### 5.6 Frontend

Use React, TypeScript, Vite, React Flow, and Dagre. Generate TypeScript API types from the backend OpenAPI schema or validate responses with a shared generated client. Do not duplicate unvalidated payload types by hand.

The page layout contains:

- header with connection status, live/history/compare mode, selected time range, refresh/pause, and last update;
- left filter panel for namespace, kind, external visibility, and search;
- main zoomable graph canvas;
- right details panel opened by selecting a node or edge;
- comparison controls for baseline and current periods;
- compact legend and counts;
- actionable loading, empty, truncated, and error states.

Visual rules:

- node shape and icon communicate kind; color may reinforce kind but cannot be the only cue;
- namespaces are visually distinguishable without creating unreadable nested graphs;
- edge width uses a capped logarithmic scale of connection count;
- in comparison mode, edge styling and a text badge distinguish `NEW`, `REMOVED`, and `CHANGED` without relying only on color;
- arrows communicate direction;
- edge labels show `TCP:<port>`, connection count, and byte volume when available and zoom permits;
- graph layout is stable across refreshes for unchanged nodes;
- keyboard focus, labels, and contrast meet WCAG AA expectations;
- the graph remains usable at 1280x720, the expected demo resolution.

Poll `/api/v1/graph` every five seconds in the accepted release. Cancel stale requests and retain the last successful graph during transient errors. Server-Sent Events may be added only after all accepted phases pass.

### 5.7 Deployment and configuration

Package the final platform as a Helm chart with this structure:

```text
charts/
  topology-visualizer/
    Chart.yaml
    values.yaml
    values.schema.json
    templates/
      agent-daemonset.yaml
      agent-rbac.yaml
      backend-deployment.yaml
      backend-service.yaml
      frontend-deployment.yaml
      frontend-service.yaml
      configmaps.yaml
      secrets.yaml
      networkpolicies.yaml
      NOTES.txt
    ci/kind-values.yaml
kind/
  cluster.yaml
```

The chart must install the agent, backend, frontend, PostgreSQL dependency, ServiceAccount, least-privilege RBAC, configuration, and required persistent storage. Production-style values may point to an external PostgreSQL database. `helm lint` and `helm template` are mandatory validation steps.

Configuration is supplied by environment variables and ConfigMaps. At minimum:

```text
CLUSTER_ID
BACKEND_INGEST_URL
AGENT_FLUSH_INTERVAL_SECONDS
AGENT_MAX_PENDING_BATCHES
INFRASTRUCTURE_PORTS
DATABASE_URL
RETENTION_HOURS
GRAPH_MAX_NODES
GRAPH_MAX_EDGES
CORS_ALLOWED_ORIGINS
TOPOLOGY_DIFF_CHANGE_THRESHOLD_PERCENT
```

The agent ServiceAccount receives only `get`, `list`, and `watch` for the resource types it actually watches. The backend and frontend receive no Kubernetes API permissions.

Keep the agent privileged for the FYP because loading and attaching eBPF programs requires host capabilities and filesystem access on the target setup. Document this clearly. After the full system works, attempt to replace `privileged: true` with the smallest capability set supported by the target kernel; do not block the core demonstration on this optional hardening.

### 5.8 Repository layout

Create the implementation as a new Git repository at the target path. Use this initial structure:

```text
kubernetes-topology-visualizer/
  README.md
  Makefile
  .gitignore
  .github/workflows/ci.yml
  docs/
    architecture.md
    demo-script.md
    limitations.md
    FYP_Project_Scope_Source_of_Truth.md
    adr/
      ADR-001-runtime-topology-visualizer.md
  contracts/
    openapi.json
    examples/
  agent/
    cmd/agent/main.go
    internal/collector/
    internal/resolver/
    internal/aggregate/
    internal/delivery/
    internal/metrics/
    bpf/
    Dockerfile
  backend/
    app/api/
    app/domain/
    app/persistence/
    app/settings.py
    app/main.py
    migrations/
    tests/
    Dockerfile
    pyproject.toml
  frontend/
    src/api/
    src/components/
    src/features/graph/
    src/features/filters/
    src/features/details/
    src/styles/
    tests/
    Dockerfile
    package.json
  charts/
    topology-visualizer/
  kind/
  demo/
  scripts/
```

Copy reusable logic from the reference prototype deliberately, but do not copy generated binaries, dependency caches, or the prototype's deployment assumptions.

## 6. Quality attributes

### Reliability

- A backend restart must not erase previously ingested topology inside retention.
- A transient backend outage must not crash the agent.
- Retried batches must not inflate connection counts.
- Informer reconnects and pod churn must update resolution without restarting the agent.

### Performance targets

For the FYP test environment:

- process at least 1,000 raw TCP establishment events per second per node without blocking the BPF consumer;
- keep agent memory below 256 MiB under normal demo load;
- return a graph of 500 nodes and 2,000 edges in under 500 ms at backend p95 on the local machine;
- render and interact with that graph without freezing the UI for more than 100 ms during polling updates;
- expose lost kernel samples, unresolved endpoints, queue depth, and dropped batches.

Performance claims in the report must be backed by a repeatable script and captured results.

### Security and privacy

- Never capture payloads.
- Use read-only Kubernetes RBAC.
- Validate request size and schema before storage.
- Do not expose ingestion outside the cluster in the default manifests.
- Default frontend exposure for `kind` is port-forwarding.
- Do not persist or return individual external IPs in topology data; disable raw event logging by default and redact external IPs from screenshots and recordings.
- Pin container base image versions or digests before the final release.
- Run backend and frontend as non-root with read-only root filesystems where compatible.

### Observability

Every component provides structured logs and health checks. Agent and backend metrics must make silent data loss diagnosable. At minimum include:

- raw events received;
- kernel samples lost;
- events filtered;
- endpoints unresolved;
- batches sent/retried/dropped;
- batches accepted/deduplicated/rejected;
- graph query count and latency;
- current stored bucket count.

## 7. Phased implementation

Each phase must leave the repository in a runnable and tested state. The implementation agent must complete acceptance criteria before starting the next phase.

### Phase 0: Repository foundation and contracts

Deliverables:

- initialize the target Git repository and directory structure;
- copy this ADR and the source-of-truth document into `docs/`;
- add pinned toolchain versions, formatting, linting, and test commands to `Makefile`;
- define the versioned ingestion, graph, and diff models and publish an initial OpenAPI contract;
- add representative valid and invalid fixtures;
- define canonical service/workload IDs, the single `EXTERNAL` ID, and edge-key rules in prose and tests;
- create the Helm chart skeleton and a three-node `kind` configuration;
- add CI jobs for Go, Python, frontend, Helm lint/template, and container builds.

Acceptance criteria:

- `make lint` and `make test` succeed on the service skeletons;
- `helm lint charts/topology-visualizer` succeeds;
- `helm template topology charts/topology-visualizer -f charts/topology-visualizer/ci/kind-values.yaml` renders valid Kubernetes YAML;
- contract examples validate against the API schema;
- all supported tool versions and local prerequisites are documented.

### Phase 1: Feasibility — eBPF capture and Kubernetes resolution

This phase reproduces the proven POC pipeline before product features are added.

Deliverables:

- implement the versioned IPv4 TCP eBPF event type;
- capture active-open TCP establishment transitions with source/destination IPs and ports plus timestamp;
- implement the Go ring-buffer reader and lost-event metrics;
- implement informer caches and pod-to-Service/logical-workload resolution;
- implement ten-second aggregation and print normalized edge batches to structured logs;
- package the agent and deploy it as a privileged DaemonSet in `kind`;
- add unit tests for event parsing, direction filtering, identity, Service resolution, aggregation, and the `EXTERNAL` node.

Acceptance criteria:

- real unmodified demo workloads produce captured IPv4 TCP events;
- `Frontend -> Backend` and `Backend -> Redis` become the same logical service-level edges in agent output;
- multiple replicas collapse to one logical node and edge;
- external traffic becomes `source -> EXTERNAL`, never one node per IP;
- accepted server sockets do not create false reverse edges;
- no payload bytes are present in BPF maps, Go structs, logs, or output;
- the agent runs on every node in the three-node `kind` cluster.

### Phase 2: End-to-end live product

This phase proves the complete `eBPF -> Go -> FastAPI -> React` path. An in-memory repository adapter is acceptable only in this phase.

Deliverables:

- implement FastAPI layering, validation, structured logging, health routes, and the in-memory repository adapter;
- implement idempotent batch ingestion, recent graph queries, namespaces, and detail routes;
- implement agent batch IDs, bounded delivery queue, retry with jitter, and graceful shutdown;
- implement the React/TypeScript typed client and React Flow graph;
- implement recent time presets, namespace filter, search, the `EXTERNAL` node, service/edge detail, polling, and error/empty states;
- add automated unit, API, frontend, and end-to-end smoke tests.

Acceptance criteria:

- traffic observed by eBPF appears in the browser within 20 seconds;
- posting the same `batch_id` twice changes counts only once;
- a temporary backend outage causes agent retries without agent termination;
- the graph shows service-level direction, TCP destination port, connection count, first seen, and last seen;
- filters and search produce deterministic visible results;
- transient API failure leaves the last successful graph visible;
- an automated smoke test checks the named expected demo edges.

### Phase 3: Production data model and historical topology

Deliverables:

- deploy PostgreSQL through the Helm chart with PVC-backed persistence and Secret-based credentials;
- implement schema migrations, indexes, transactional idempotent ingestion, one-minute buckets, retention, and readiness checks;
- replace the deployed in-memory adapter with PostgreSQL while retaining it for fast unit tests;
- implement preset and custom `from`/`to` graph queries;
- implement deterministic two-period topology diff with `NEW`, `REMOVED`, and `CHANGED` classifications;
- add the frontend history picker and comparison mode;
- add persistence, restart, time-boundary, retention, and diff tests.

Acceptance criteria:

- backend and agent pod restarts do not erase committed topology or inflate counts;
- users can inspect the last 5 minutes, 15 minutes, hour, and a custom range;
- a controlled deployment change appears as the expected `NEW` or `REMOVED` edge;
- a controlled connection-count change crossing the configured threshold appears as `CHANGED` with its calculation visible;
- database failure makes readiness fail and returns actionable errors without exposing credentials;
- graph and diff queries produce deterministic results at exact bucket boundaries.

### Phase 4: Product completeness and traffic-volume feasibility

Deliverables:

- complete node and edge detail panels with incoming/outgoing dependencies and timestamps;
- implement stable layout, accessible controls, legend, truncation state, and traffic-intensity styling;
- perform a bounded eBPF traffic-volume spike to evaluate reliable per-edge bytes sent/received on the target kernels;
- if reliable, add nullable byte counters through collection, ingestion, PostgreSQL, graph/diff APIs, UI, and tests;
- if unreliable, retain connection count as the traffic measure and document the experiment, failure mode, and deferred design;
- optionally add deterministic top-talkers and high-dependency summaries only after all required behavior passes.

Acceptance criteria:

- the UI is usable at 1280x720 and all controls are keyboard reachable;
- comparison status is understandable without color alone;
- edge thickness uses byte volume when available and connection count otherwise, with the metric named explicitly;
- the byte-accounting decision is backed by a reproducible experiment under `docs/evaluation/`, whether accepted or deferred;
- detail views show namespace, incoming/outgoing dependencies, ports, counts, optional bytes, first seen, and last seen;
- frontend unit/component tests pass in CI.

### Phase 5: Helm packaging, multi-node validation, and FYP handoff

Deliverables:

- complete the Helm chart for agent, backend, frontend, PostgreSQL, ServiceAccount, RBAC, ConfigMaps, Secret, PVCs, probes, resource limits, security contexts, and internal Services;
- add Make targets to create the `kind` cluster, build/load images, install/upgrade the chart, wait for readiness, generate traffic, verify topology/diff, and uninstall safely;
- create deterministic demo workloads across at least two namespaces, including a topology change scenario;
- validate the same chart on a multi-node kubeadm cluster and record any environment-specific values;
- run and document correctness, load, resource, restart, retry, pod-churn, history, and comparison experiments;
- record environment details and raw results under `docs/evaluation/`;
- add architecture and sequence diagrams, limitations, operator instructions, troubleshooting, and the presentation script;
- pin dependencies and container images, scan them, triage results, and tag the submission release.

Acceptance criteria:

- a clean supported machine can run `make demo-up` without hand-editing manifests;
- `make demo-traffic` and `make demo-change` produce the expected current topology and diff;
- deleting and recreating backend or database pods preserves committed history through their PVCs;
- the agent reports successfully from every node in both the `kind` and kubeadm validation clusters;
- `make demo-down` removes only resources created by the project;
- all ADR requirements map to a test, demonstration step, or documented limitation;
- measured results support or explicitly reject each performance target;
- CI passes from a clean checkout and the complete demo succeeds using only committed instructions;
- failure modes for missing BTF, denied BPF permissions, backend outage, PostgreSQL failure, and empty graph have actionable messages;
- final screenshots and recordings expose no secrets or individual external IP addresses.

## 8. Required test matrix

| Layer | Required tests |
|---|---|
| eBPF | event layout, IPv4 parsing, active-open filter, unsupported family/protocol rejection, optional byte-accounting spike |
| Agent domain | owner resolution, Service port matching, canonical IDs, infrastructure filtering, aggregation |
| Agent delivery | success, timeout, retry, duplicate response, queue limit, shutdown flush |
| Backend API | schema validation, idempotency, preset/custom ranges, diff classification, filters, response limits, error mapping |
| PostgreSQL | migration, transaction rollback, bucket upsert, retention, credentials, restart persistence |
| Frontend | API mapping, history/compare modes, filters, stable layout inputs, selection, loading/error/empty/truncated states |
| Kubernetes | Helm lint/template, RBAC access, probes, Secrets, PVCs, rollout, agent scheduling on all nodes |
| End-to-end | known traffic produces expected directed graph and controlled topology changes produce expected diffs; restart/retry does not lose or double count |

Tests that require privileged eBPF execution may be separated from ordinary CI, but must have a documented local command and must run before the final release.

## 9. Definition of done

The FYP implementation is done only when all of the following are true:

- A clean `kind` deployment works from committed Helm and Make commands.
- The Helm deployment is validated on a multi-node kubeadm cluster.
- The agent observes real TCP connections without application changes or sidecars.
- Pod churn does not fragment workload identity.
- Service destinations are resolved using EndpointSlices and ports.
- PostgreSQL persists graph history across backend and database pod restarts.
- Agent retries do not double-count connections.
- The UI supports preset/custom history, topology comparison, filters, and detail views.
- Controlled topology changes are classified deterministically as `NEW`, `REMOVED`, or `CHANGED`.
- Byte-level traffic volume is either delivered end to end or its required feasibility attempt and evidence are documented, with connection count retained as the fallback measure.
- Automated tests validate the expected demo topology.
- Metrics and logs expose collection or delivery failure.
- Security and privacy constraints are documented and enforced.
- Known limitations are stated honestly in the UI documentation and FYP report.

## 10. Consequences and trade-offs

### Positive

- Runtime observation shows dependencies that manifests alone cannot prove.
- No sidecar or application instrumentation is required.
- Stable workload identities keep the graph readable during pod replacement.
- Durable bucketed storage enables time-window queries and repeatable demonstrations.
- The chosen stack builds directly on a working local proof of concept.
- Versioned contracts and phased acceptance criteria let another agent implement incrementally.

### Negative

- The agent needs elevated node privileges and Linux-specific kernel features.
- TCP establishment counts are not request counts and must not be presented as such.
- Encrypted application traffic remains opaque by design.
- Destination Service attribution can be ambiguous when multiple Services select the same pod and port.
- PostgreSQL increases installation resource usage and operational complexity.
- Runtime-only topology cannot show a dependency that has not communicated inside the selected time window.

### Mitigations

- Make privilege requirements explicit and attempt capability reduction after correctness.
- Label the metric as `connections`, never `requests`.
- Preserve ambiguity and unresolved states instead of fabricating certainty.
- Package PostgreSQL through Helm for a self-contained demo and support an external database for other environments.
- Provide deterministic demo traffic and a selectable observation window.

## 11. Alternatives considered

### Infer topology only from Kubernetes manifests

Rejected because selectors and configuration show possible relationships, not observed communication. It also misses direct pod traffic and external destinations.

### Require a service mesh

Rejected for the FYP core because it adds sidecars or ambient infrastructure and changes the environment being observed. A future comparison with mesh telemetry would be academically useful.

### Use packet capture or deep packet inspection

Rejected because it expands privileges, data volume, protocol complexity, and privacy risk. L3/L4 metadata is sufficient for the accepted objective.

### Replace FastAPI with a Go backend

Rejected for the initial implementation. The prototype already validates FastAPI, and separating the collector from the API demonstrates a language-neutral contract. A single Go codebase would simplify deployment but would add rewrite cost without improving the FYP result.

### Use SQLite for the final product

Rejected because persistent historical topology and period comparison are core final-FYP requirements, and the authoritative technology stack specifies PostgreSQL. An in-memory adapter is permitted only during the end-to-end POC; PostgreSQL is mandatory from Phase 3 onward.

### Stream every event directly to the browser

Rejected because it couples collection rate to UI capacity and complicates retries. Server-side aggregation and polling provide predictable load and a stable historical view.

## 12. Implementation instructions for the receiving AI agent

1. Read this ADR completely before editing or generating code.
2. Read the authoritative source-of-truth document, then inspect the reference prototype to reuse validated behavior and identify generated eBPF build requirements.
3. Create the target repository; do not modify or delete the reference prototype.
4. Implement phases in order and stop advancing when a phase's acceptance criteria fail.
5. Prefer the smallest implementation that satisfies this ADR; do not add out-of-scope features.
6. Record any necessary architectural deviation as a new ADR in `docs/adr/` before implementing it.
7. Maintain a requirements-to-tests checklist as work progresses.
8. Use exact, pinned dependencies for the final release, but verify current compatible versions during implementation.
9. Never report a phase complete without running its relevant tests and recording the commands used.
10. Preserve user-owned files and changes under `/home/it-laptop`; restrict implementation writes to the target repository unless explicitly authorized.

## 13. Deferred follow-up decisions

Create separate ADRs only if these are taken into scope later:

- high-availability PostgreSQL and multi-replica backend deployment;
- multi-cluster identity and federation;
- IPv6 capture;
- UDP/DNS visibility;
- Server-Sent Events or WebSocket updates;
- automatic NetworkPolicy recommendations;
- authentication and role-based UI access;
- storage beyond the configured retention period and long-term analytics;
- machine learning, prediction, and advanced anomaly detection;
- optional Prometheus and Loki detail-panel integrations;
- capability-based agent hardening without privileged mode.
