# FYP Project Scope — Source of Truth

## Project Title
**Dynamic Service Topology Visualization in Kubernetes Environments**

## 1. Project Goal

The project is a **Kubernetes runtime topology visualization platform**.

Its purpose is to automatically observe the actual communication occurring between workloads inside a Kubernetes cluster and transform those observations into an **interactive, service-level topology graph**.

The key idea is that the topology represents what the application is **actually doing at runtime**, rather than simply reconstructing what Kubernetes manifests say should exist.

Example:

```text
                         ┌──────────────┐
                         │   EXTERNAL   │
                         └──────▲───────┘
                                │
                              HTTPS
                                │
┌──────────┐   HTTP    ┌────────┴───────┐
│ Frontend │ ────────► │     Backend    │
└──────────┘            └───────┬───────┘
                                │
                   ┌────────────┼────────────┐
                   │            │            │
                   ▼            ▼            ▼
               ┌───────┐   ┌────────┐   ┌──────────┐
               │ Redis │   │Postgres│   │    MQ    │
               └───────┘   └────────┘   └──────────┘
```

The platform should also preserve historical information so that users can understand **how this topology changes over time**.

---

## 2. Core Problems Being Solved

### 2.1 Runtime Visibility

Kubernetes manifests describe the desired configuration of the cluster, but they do not necessarily tell an operator exactly which application components are communicating at a particular moment.

The system discovers those relationships from actual runtime activity.

### 2.2 Hidden Dependencies

A service may communicate with:

- Databases
- Redis or other caches
- Message queues
- Other microservices
- External APIs

without that dependency being immediately obvious from a static architecture diagram.

The platform should discover these relationships automatically.

### 2.3 Topology Evolution

The topology is not assumed to remain static.

Example:

```text
10:00

Frontend → Backend → PostgreSQL

14:00

Frontend → Backend → PostgreSQL
                    ↘ Redis

17:00

Frontend → Backend → PostgreSQL
                    ↘ Redis
                    ↘ Payment Service
```

The system should preserve enough historical information to identify these changes.

### 2.4 Operational Understanding

The resulting graph should make the architecture easier to inspect during:

- Deployments
- Troubleshooting
- Dependency analysis
- Runtime architecture review
- General system operation

---

## 3. Scope of Runtime Discovery

The system monitors **network communication occurring at runtime**.

For the initial FYP, the primary communication type is:

**TCP connections over IPv4.**

A captured observation conceptually contains:

```text
source IP
destination IP
source port
destination port
protocol
timestamp
```

Example:

```text
10.244.1.7 → 10.244.2.11:6379 TCP
```

The raw network information is then enriched using Kubernetes metadata.

---

## 4. Kubernetes Identity Resolution

Raw IP addresses are not useful enough for the final user.

The platform therefore correlates runtime observations with Kubernetes resources.

Example:

```text
10.244.1.7
       ↓
Pod: backend-789bd76d-x2jf
       ↓
Service / Workload identity
       ↓
backend
```

and:

```text
10.244.2.11
       ↓
Pod: redis-55fbb7d9
       ↓
redis
```

The resulting topology becomes:

```text
backend → redis
```

rather than:

```text
10.244.1.7 → 10.244.2.11
```

Relevant Kubernetes information may include:

- Pods
- Services
- EndpointSlices
- Namespaces
- Pod IP addresses
- Labels
- Workload ownership information

---

## 5. Service-Level Topology

The final visualization should primarily be **service/application level**, not a large pod-level network graph.

If several backend pod replicas communicate with Redis, the UI should normally show:

```text
Backend ─────► Redis
```

rather than displaying separate edges for every replica.

This keeps the graph focused on **architecture understanding**.

Pod-level information may still be kept internally or exposed in a detailed view later, but it is not the primary visualization.

---

## 6. External Communication

External destinations should be represented, but the FYP does not need to become an Internet traffic analysis platform.

Unknown or non-cluster destinations can initially be aggregated into a single node:

```text
Backend ─────► EXTERNAL
```

The graph may show:

- Destination port
- Protocol
- Connection count
- Traffic volume when byte tracking is implemented

External communication should remain summarized rather than exposing many individual remote IPs.

---

## 7. Runtime Engine

The first major system component is the **runtime engine**.

### Technology
- **Go**
- **eBPF**

### Kubernetes Deployment Model
The runtime engine runs on every Kubernetes node using a **DaemonSet**.

### Responsibilities

The runtime engine follows this pipeline:

**Capture → Resolve → Aggregate → Send**

It should:

1. Load and attach eBPF programs.
2. Capture TCP runtime connection events.
3. Read source and destination IP/port information.
4. Correlate observed IPs with Kubernetes resources.
5. Convert pod-level communication into service-level edges.
6. Aggregate repeated communication events.
7. Send summarized topology information to the backend.

---

## 8. eBPF Scope

eBPF is the mechanism used to obtain runtime network information without modifying the applications themselves.

The initial eBPF scope should remain intentionally narrow:

```text
TCP connection observation
        ↓
Source / Destination
        ↓
Kubernetes correlation
        ↓
Service dependency
```

### Core Requirements

- Capture real TCP communication.
- IPv4 is sufficient for the initial version.
- Observe connection establishment.
- Obtain source IP.
- Obtain destination IP.
- Obtain source port.
- Obtain destination port.
- Obtain timestamp.

### Non-Goals for eBPF

The project is **not** intended to become:

- A packet capture tool
- A deep packet inspection tool
- An HTTP payload analyzer
- A full network security platform

The purpose of eBPF is to feed reliable runtime topology information into the visualization platform.

---

## 9. Traffic Aggregation

Individual network events should not directly become graph edges.

Repeated events such as:

```text
backend → redis
backend → redis
backend → redis
```

should be aggregated into a meaningful edge:

```text
Backend → Redis
TCP : 6379
Connections: 143
```

Aggregation should happen over configurable time windows, initially around **10 seconds**.

Possible aggregated fields:

- Source service
- Destination service
- Destination port
- Protocol
- Connection count
- Bytes transferred, when implemented
- First seen timestamp
- Last seen timestamp

---

## 10. Central Backend

The second major component is the **central backend platform**.

### Technology
- **Python**
- **FastAPI**

### Responsibilities

The backend should:

1. Receive topology batches from node agents.
2. Validate incoming records.
3. Merge information coming from multiple nodes.
4. Store topology history.
5. Query topology over selected time ranges.
6. Compute topology differences.
7. Provide data to the frontend.

### Initial API Scope

```text
POST /ingest
GET  /graph
GET  /health
```

Expected later endpoints:

```text
GET /graph?from=...&to=...
GET /diff?from=...&to=...
```

The API contract may evolve during implementation.

---

## 11. Historical Topology Storage

Historical data is a core part of the final FYP.

### Recommended Technology
**PostgreSQL**

The stored data should conceptually include:

```text
Timestamp
Source
Destination
Port
Protocol
Connection count
Traffic volume
```

Example:

```text
14:00 backend → redis       6379
14:00 backend → postgres    5432

14:10 backend → redis       6379
14:10 backend → postgres    5432
14:10 backend → payment     443
```

This allows the platform to determine that `Backend → Payment` is a newly observed dependency.

---

## 12. Time-Aware Topology

The final system should support more than a live graph.

Users should be able to inspect topology over selected time windows.

Examples:

- Last 5 minutes
- Last 15 minutes
- Last hour
- Custom time range

The system should support before/after comparison, for example around a deployment.

---

## 13. Frontend

The third major component is the **web visualization interface**.

### Technology
- React
- TypeScript preferred
- A graph visualization library such as Cytoscape.js or React Flow

### Main Purpose

The UI should convert backend topology data into a clear and interactive representation of the application architecture.

The UI should prioritize **clarity and architecture understanding** over exposing large amounts of low-level network data.

---

## 14. UI Functional Scope

### 14.1 Live / Recent Topology
Display current or recently observed communication relationships.

### 14.2 Service Nodes
Each primary graph node represents a Kubernetes service or logical workload.

### 14.3 Communication Edges
Edges should show service communication.

Possible information:

- Destination port
- Protocol
- Connection count
- Traffic volume

### 14.4 Traffic Visualization
Traffic intensity may be represented through edge thickness, numerical labels, or tooltips.

### 14.5 Namespace Filtering
Users should be able to filter topology by namespace.

### 14.6 Search
Users should be able to search for a specific service.

### 14.7 External Traffic
External communication should appear as a summarized `EXTERNAL` node.

### 14.8 Historical Time Selection
Users should be able to select previous time windows.

### 14.9 Topology Comparison
Users should be able to compare two topology periods.

The comparison may classify edges as:

```text
NEW
REMOVED
CHANGED
```

### 14.10 Service Details Panel
Selecting a service may display:

- Namespace
- Incoming dependencies
- Outgoing dependencies
- Ports
- Connection count
- Traffic volume
- First seen
- Last seen

---

## 15. Topology Change Detection

The product should identify architectural changes over time.

### New Dependency
An edge exists now but was absent in the baseline window.

### Removed Dependency
An edge was present previously but no longer appears.

### Traffic Change
Communication volume changes significantly between selected periods.

These insights should remain deterministic and explainable.

Machine learning is not required for the core FYP.

---

## 16. Traffic Volume

Traffic volume is a desired feature.

The system should eventually support visualizing the amount of traffic associated with communication edges.

Possible measurements:

- Bytes sent
- Bytes received
- Total bytes
- Connection count

### Implementation Priority

For the initial POC, **connection count is enough**.

For the final FYP, **byte-level traffic volume should be attempted and included if technically feasible**.

---

## 17. Deployment Model

The final product should itself be deployable into Kubernetes.

### Final Kubernetes Components

- Agent DaemonSet
- Backend Deployment
- Backend Service
- Frontend Deployment
- Frontend Service
- Database
- ServiceAccount
- RBAC
- ConfigMaps / Secrets as required
- Helm chart

---

## 18. Kubernetes Permissions

Because the runtime engine uses eBPF and node-level visibility, the DaemonSet is expected to require elevated permissions.

For the initial project environment, using a privileged DaemonSet is acceptable.

Possible requirements include:

- `privileged: true`
- `hostPID: true`
- access to `/sys/fs/bpf`
- access to `/sys/fs/cgroup`
- additional mounts/capabilities depending on the final eBPF implementation

Kubernetes RBAC should allow read access to resources needed for identity resolution:

- Pods
- Services
- EndpointSlices
- Namespaces

Permissions should be restricted to the minimum required by the final implementation.

---

## 19. Development Environment

### Initial Development / POC

Use:

- Linux host
- Kernel 6.8+
- Docker
- kind
- kubectl
- bpftool
- Go
- Python
- Node.js

### Final Validation

The final system should also be validated on a multi-node Kubernetes environment, preferably kubeadm, to demonstrate that the design is not limited to kind.

---

## 20. POC Scope

The first major milestone is a feasibility POC.

The POC needs to prove the complete pipeline:

```text
REAL TCP connection
        ↓
eBPF captures it
        ↓
Go reads event
        ↓
IP mapped to Kubernetes resource
        ↓
Service-level edge generated
        ↓
Edge sent to FastAPI
        ↓
Backend exposes topology
        ↓
React displays graph
```

### POC Success Example

Actual workload traffic:

```text
Frontend → Backend
Backend → Redis
```

should result in the same dependencies appearing automatically in the frontend.

### POC Does NOT Need

- PostgreSQL
- Historical comparison
- Traffic byte accounting
- ML
- Multi-cluster support
- Production authentication

The goal is to prove technical feasibility of the architecture.

---

## 21. Final FYP Scope

After the POC succeeds, evolve it into:

```text
Multi-node collection
        +
Service-level aggregation
        +
Persistent storage
        +
Historical topology
        +
Traffic volume
        +
Topology comparison
        +
Polished interactive UI
        +
Kubernetes packaging
```

---

## 22. Optional Observability Integrations

Prometheus and Loki are **not required for topology discovery**.

The topology system should remain standalone.

However, optional integrations may enrich the service detail view.

### Prometheus
Potential information:

- CPU usage
- Memory usage
- Restart count
- Service health metrics

### Loki
Potential integration:

- Open logs for selected service
- Open logs around selected topology timestamp

These integrations are secondary and must not become dependencies for the core topology functionality.

---

## 23. Pattern Recognition and Insights

Basic pattern detection is within scope as an enhancement once historical data exists.

Suitable features include:

### New Dependency Detection
Detect when a service starts communicating with a service it did not communicate with during the baseline period.

### Traffic Spike Detection
Detect when current communication volume is significantly higher than the previous baseline.

### Top Talkers
Identify services producing the most communication.

### High-Dependency Services
Identify services that communicate with or are depended upon by many other services.

These should initially use simple statistical or graph-based logic rather than machine learning.

---

## 24. Machine Learning / Prediction

Machine learning is **not a mandatory requirement** of the FYP.

It may be explored only after the core platform is complete.

Possible future examples:

- Communication anomaly detection
- Dependency pattern prediction
- Traffic forecasting
- Detection of unusual architectural behavior

These are stretch goals and should not risk delivery of the main product.

---

## 25. Explicit Non-Goals

The project is **not** intended to build:

- A Kubernetes management platform like Rancher
- A service mesh
- A CNI
- A replacement for Prometheus
- A replacement for Grafana
- A complete APM platform
- Distributed tracing
- Log management
- Deep packet inspection
- Packet payload capture
- Full Layer 7 parsing
- Automatic remediation
- A SIEM
- Network policy enforcement
- Full Internet traffic analysis

These boundaries should be maintained to keep the FYP achievable.

---

## 26. Target Users

Primary users:

- DevOps engineers
- Site Reliability Engineers
- System engineers
- Cloud engineers
- Kubernetes administrators
- Software/system architects

---

## 27. Main Use Cases

### Runtime Architecture Discovery
Understand how the deployed application actually communicates.

### Dependency Discovery
Reveal service dependencies that may not be documented.

### Deployment Validation
Compare architecture before and after a deployment.

### Troubleshooting
Understand which services depend on a failing component.

### Architecture Documentation
Use observed runtime topology as a more current representation of application architecture.

### External Communication Awareness
Identify which services communicate outside the cluster.

### Traffic Understanding
Identify high-volume communication relationships.

---

## 28. Technology Stack

### Runtime Engine
- Go
- eBPF
- `cilium/ebpf`

### Backend
- Python
- FastAPI

### Database
- PostgreSQL

### Frontend
- React
- TypeScript
- Cytoscape.js or React Flow

### Deployment
- Kubernetes
- Helm

### Development Environment
- kind
- Docker

### Final Testing
- Multi-node Kubernetes / kubeadm

---

## 29. High-Level Data Flow

```text
Application Pods
      │
      │ Runtime TCP traffic
      ▼
Linux Kernel
      │
      │ eBPF events
      ▼
Go Node Agent
      │
      │ Resolve IP → Kubernetes identity
      │ Aggregate connections
      ▼
FastAPI Backend
      │
      ├────────────► PostgreSQL
      │               Historical topology
      │
      ▼
Topology API
      │
      ▼
React Frontend
      │
      ▼
Interactive Service Graph
```

---

## 30. Core Project Pipeline

The central implementation pipeline that should remain stable throughout development is:

> **eBPF → Go Agent → Kubernetes Resolution → Aggregation → FastAPI → PostgreSQL → React Topology → Historical Comparison**

Any proposed feature should be evaluated based on whether it strengthens this pipeline or distracts from the main project objectives.

---

## 31. Final Product Definition

**Dynamic Service Topology Visualization in Kubernetes Environments** is a Kubernetes-native observability platform that automatically discovers actual runtime communication between application services and represents those relationships through an interactive service-level topology graph.

A node-level runtime engine observes network communication without requiring application instrumentation or a service mesh. A central backend correlates communication data with Kubernetes resources, aggregates and stores topology information, and maintains its evolution over time.

A web interface enables users to inspect current service dependencies, communication intensity, historical topology, and architectural changes, providing DevOps engineers, SREs, and system architects with a clearer understanding of how distributed applications actually behave at runtime.

---

## 32. Development Priority

### Priority 1 — Feasibility
- Real eBPF capture
- Go event reader
- Kubernetes IP resolution
- Real edge generation

### Priority 2 — End-to-End Product
- Backend ingestion
- Graph API
- Frontend topology visualization

### Priority 3 — Production Data Model
- Aggregation
- PostgreSQL
- Historical topology

### Priority 4 — Product Features
- Time selection
- Topology diff
- Traffic visualization
- External node
- Filters
- Service details

### Priority 5 — Enhancements
- Pattern detection
- Prometheus integration
- Loki integration
- Advanced traffic accounting

### Priority 6 — Stretch Goals
- ML/prediction
- Multi-cluster support
- Advanced external destination resolution
- Layer 7 information

---

## 33. Definition of Done

The FYP can be considered successfully implemented when:

1. A real Kubernetes application produces network communication.
2. The runtime engine observes that communication without modifying the application.
3. The system maps observed connections to Kubernetes service identities.
4. Multiple node agents can report to the backend.
5. The backend persists topology data.
6. The frontend displays an interactive service-level topology.
7. Users can select historical time ranges.
8. Users can identify changes between topology periods.
9. External communication is represented in summarized form.
10. The complete platform is deployable into Kubernetes.
