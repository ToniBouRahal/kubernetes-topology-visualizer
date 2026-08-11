"""Contract models — the single source of truth for the wire format.

Normative reference: contracts/ids.md and ADR-003. These models generate contracts/openapi.json,
which in turn generates the frontend TypeScript client. Changing anything here is a contract
change: follow the procedure in contracts/ids.md §11.
"""

from __future__ import annotations

import re
from typing import Annotated, Literal

from pydantic import AwareDatetime, BaseModel, ConfigDict, Field, model_validator

# ── Identity ────────────────────────────────────────────────────────────────────────────────
# contracts/ids.md §1. Exactly six kinds. ReplicaSet is deliberately absent: pods owned by a
# ReplicaSet collapse to that ReplicaSet's own owner, the Deployment.
NodeKind = Literal["Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "Pod"]

Protocol = Literal["TCP"]

DiffClassification = Literal["NEW", "REMOVED", "CHANGED", "UNCHANGED"]

EXTERNAL_NODE_ID = "external:EXTERNAL"

# Crockford base32, 26 characters. Excludes I, L, O, U by design.
ULID_PATTERN = re.compile(r"^[0-9A-HJKMNP-TV-Z]{26}$")

_ID_SEGMENT_FORBIDDEN = ":"


def build_node_id(cluster_id: str, namespace: str, kind: str, name: str) -> str:
    """Construct a canonical node ID. The one place this grammar is written in Python.

    Segments may not contain ':' — a name containing the separator would make the ID ambiguous.
    IDs are opaque to consumers (contracts/ids.md §2), but they must still be unambiguous to
    produce.
    """
    for label, segment in (
        ("cluster_id", cluster_id),
        ("namespace", namespace),
        ("kind", kind),
        ("name", name),
    ):
        if not segment:
            raise ValueError(f"node id segment {label!r} must not be empty")
        if _ID_SEGMENT_FORBIDDEN in segment:
            raise ValueError(
                f"node id segment {label!r} must not contain {_ID_SEGMENT_FORBIDDEN!r}"
            )
    return f"k8s:{cluster_id}:{namespace}:{kind}:{name}"


def build_edge_id(source_id: str, target_id: str, protocol: str, destination_port: int) -> str:
    """Stable, URL-safe identifier for an edge within a cluster.

    Mirrors the edge key of contracts/ids.md §4 minus cluster_id, which is implicit in a
    single-cluster deployment. Used only for addressing GET /api/v1/edges/{edge_id}.
    """
    return f"{source_id}|{target_id}|{protocol}|{destination_port}"


# ── Ingestion ───────────────────────────────────────────────────────────────────────────────


class NodeRef(BaseModel):
    """A node as reported by the agent."""

    model_config = ConfigDict(extra="forbid")

    id: str = Field(min_length=1, examples=["k8s:kind-topology:demo:Deployment:client"])
    kind: NodeKind
    namespace: str | None = Field(default=None, description="Null only for the external node.")
    name: str = Field(min_length=1)


class EdgeObservation(BaseModel):
    """One aggregated client→server relationship observed during the agent's flush interval."""

    model_config = ConfigDict(extra="forbid")

    source: NodeRef
    target: NodeRef
    protocol: Protocol
    destination_port: Annotated[int, Field(ge=1, le=65535)]
    connection_count: Annotated[int, Field(ge=1)]

    # Absent until the Phase 4 byte-accounting gate passes. Absent is NOT zero
    # (contracts/ids.md §10).
    bytes_sent: Annotated[int, Field(ge=0)] | None = None
    bytes_received: Annotated[int, Field(ge=0)] | None = None

    first_seen: AwareDatetime
    last_seen: AwareDatetime

    @model_validator(mode="after")
    def _check_interval(self) -> EdgeObservation:
        if self.last_seen < self.first_seen:
            raise ValueError("last_seen must not precede first_seen")
        return self


class IngestBatch(BaseModel):
    """A retry-safe unit of ingestion. `batch_id` is the idempotency key."""

    model_config = ConfigDict(extra="forbid")

    # Plain int, not Literal[1]: an unsupported version must yield 400, not 422
    # (contracts/ids.md §8). The route enforces the supported value.
    schema_version: int
    cluster_id: str = Field(min_length=1)
    agent_id: str = Field(min_length=1)
    batch_id: str = Field(min_length=26, max_length=26, examples=["01J8ZQ9X7K4M2N6P8R3T5V7W9Y"])
    observed_at: AwareDatetime
    interval_seconds: Annotated[int, Field(ge=1, le=3600)]
    edges: list[EdgeObservation]

    @model_validator(mode="after")
    def _check_batch_id(self) -> IngestBatch:
        if not ULID_PATTERN.match(self.batch_id):
            raise ValueError("batch_id must be a 26-character Crockford base32 ULID")
        return self


class IngestResult(BaseModel):
    """202 for a newly stored batch, 200 for one already ingested."""

    batch_id: str
    status: Literal["ingested", "already_ingested"]
    edges_accepted: int


# ── Graph ───────────────────────────────────────────────────────────────────────────────────


class GraphNode(BaseModel):
    """Identity plus the display fields that make ID parsing unnecessary (contracts/ids.md §2)."""

    id: str
    kind: NodeKind | Literal["External"]
    namespace: str | None
    name: str
    label: str
    first_seen: AwareDatetime
    last_seen: AwareDatetime
    attributes: dict[str, str] = Field(default_factory=dict)


class GraphEdge(BaseModel):
    id: str
    source_id: str
    target_id: str
    protocol: Protocol
    destination_port: int
    connection_count: int
    bytes_sent: int | None = None
    bytes_received: int | None = None
    first_seen: AwareDatetime
    last_seen: AwareDatetime


class TimeWindow(BaseModel):
    """Half-open, lower-inclusive: [start, end). Fixed by ADR-005 D-5.4."""

    start: AwareDatetime
    end: AwareDatetime


class EffectiveFilters(BaseModel):
    """Filters actually applied, including defaulted values, so the client never has to guess."""

    namespaces: list[str] = Field(default_factory=list)
    kind: NodeKind | None = None
    query: str | None = None
    include_external: bool = True
    include_unresolved: bool = False


class GraphSummary(BaseModel):
    node_count: int
    edge_count: int
    total_connections: int
    truncated: bool = False
    truncation_reason: str | None = None


class GraphResponse(BaseModel):
    generated_at: AwareDatetime
    window: TimeWindow
    filters: EffectiveFilters
    nodes: list[GraphNode]
    edges: list[GraphEdge]
    summary: GraphSummary


# ── Diff ────────────────────────────────────────────────────────────────────────────────────


class DiffEdge(BaseModel):
    """One classified edge. A period with no observation contributes zero, not null."""

    id: str
    source_id: str
    target_id: str
    protocol: Protocol
    destination_port: int
    classification: DiffClassification

    baseline_connection_count: int
    current_connection_count: int
    connection_delta: int

    # Undefined when the baseline is zero — the reason says so rather than emitting
    # Infinity or NaN (contracts/ids.md §10).
    connection_percent_delta: float | None = None

    baseline_bytes_total: int | None = None
    current_bytes_total: int | None = None
    bytes_percent_delta: float | None = None

    reason: str = Field(
        description="Human-readable justification for the classification, including the "
        "comparison performed and the threshold applied."
    )


class DiffSummary(BaseModel):
    new_count: int
    removed_count: int
    changed_count: int
    unchanged_count: int
    truncated: bool = False


class DiffResponse(BaseModel):
    generated_at: AwareDatetime
    baseline: TimeWindow
    current: TimeWindow
    threshold_percent: float
    filters: EffectiveFilters
    include_unchanged: bool
    edges: list[DiffEdge]
    summary: DiffSummary


# ── Detail views ────────────────────────────────────────────────────────────────────────────


class NodeDependency(BaseModel):
    node_id: str
    label: str
    protocol: Protocol
    destination_port: int
    connection_count: int
    bytes_total: int | None = None
    first_seen: AwareDatetime
    last_seen: AwareDatetime


class NodeDetail(BaseModel):
    node: GraphNode
    window: TimeWindow
    incoming: list[NodeDependency]
    outgoing: list[NodeDependency]


class EdgeDetail(BaseModel):
    edge: GraphEdge
    window: TimeWindow
    source: GraphNode
    target: GraphNode


# ── Errors and health ───────────────────────────────────────────────────────────────────────


class ErrorResponse(BaseModel):
    """Stable error envelope. Never carries a stack trace, DSN, or credential (ADR-004 D-4.6)."""

    error: str
    detail: str
    request_id: str | None = None


class HealthResponse(BaseModel):
    status: Literal["ok", "degraded", "unavailable"]
    checks: dict[str, str] = Field(default_factory=dict)


class NamespaceList(BaseModel):
    window: TimeWindow
    namespaces: list[str]
