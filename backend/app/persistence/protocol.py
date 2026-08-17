"""The storage contract.

Both the in-memory adapter (Phase 2, and permanently for fast unit tests) and the PostgreSQL
adapter (Phase 3 onward, the only deployed one) implement this. A shared contract-test suite runs
against both, which is the main defence against "passes in unit tests, fails in the demo"
(ADR-005 D-5.1).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from enum import StrEnum
from typing import Protocol, runtime_checkable

from app.domain.models import IngestBatch
from app.domain.window import ResolvedWindow


class IngestOutcome(StrEnum):
    """Distinguishes a stored batch from a replayed one.

    The API maps these to 202 and 200 respectively. The split is what makes agent retries both
    safe and observable (contracts/ids.md §8).
    """

    INGESTED = "ingested"
    ALREADY_INGESTED = "already_ingested"


@dataclass(frozen=True, slots=True)
class StoredNode:
    """A node as persisted. `label` is stored, never derived by parsing the id."""

    id: str
    cluster_id: str
    kind: str
    namespace: str | None
    name: str
    label: str
    first_seen: datetime
    last_seen: datetime
    attributes: dict[str, str] = field(default_factory=dict)


@dataclass(frozen=True, slots=True)
class EdgeAggregate:
    """One logical edge, already summed across buckets within the queried window."""

    source_id: str
    target_id: str
    protocol: str
    destination_port: int
    connection_count: int
    first_seen: datetime
    last_seen: datetime
    bytes_sent: int | None = None
    bytes_received: int | None = None

    @property
    def key(self) -> tuple[str, str, str, int]:
        """The edge key minus cluster_id, which is implicit in a single-cluster deployment."""
        return (self.source_id, self.target_id, self.protocol, self.destination_port)


@dataclass(frozen=True, slots=True)
class EdgeFilters:
    """Filters pushed down to storage rather than applied after loading everything."""

    namespaces: tuple[str, ...] = ()
    kind: str | None = None
    query: str | None = None
    include_external: bool = True
    include_unresolved: bool = False


@dataclass(frozen=True, slots=True)
class PurgeStats:
    edge_buckets_deleted: int
    nodes_deleted: int


@dataclass(frozen=True, slots=True)
class StorageHealth:
    available: bool
    detail: str
    migrations_applied: bool = True


@runtime_checkable
class TopologyRepository(Protocol):
    """Storage operations. Implementations must be safe for concurrent use."""

    async def ingest_batch(self, batch: IngestBatch) -> IngestOutcome:
        """Record a batch atomically.

        The `batch_id` check and the edge merge must happen in ONE transaction, with the batch
        insert gating everything else. A duplicate must return ALREADY_INGESTED having moved no
        counter (ADR-005 D-5.3). A read-then-write pre-check is a race under two agents retrying
        at once and is not acceptable.
        """
        ...

    async def query_edges(
        self, window: ResolvedWindow, filters: EdgeFilters
    ) -> list[EdgeAggregate]:
        """Edges whose buckets fall in [window.start, window.end), summed and sorted.

        Ordering is part of the contract: `(source_id, target_id, protocol, destination_port)`.
        """
        ...

    async def nodes_for(self, node_ids: set[str]) -> dict[str, StoredNode]:
        """Look up node metadata. Graph nodes are derived only from edges in the window, so the
        caller decides which ids it needs (ADR-004 D-4.4)."""
        ...

    async def list_namespaces(self, window: ResolvedWindow) -> list[str]:
        """Namespaces observed in the window, sorted."""
        ...

    async def purge_expired(self, before: datetime) -> PurgeStats:
        """Delete buckets older than `before`, then nodes no longer referenced."""
        ...

    async def health(self) -> StorageHealth:
        """Report availability. Drives /health/ready (ADR-005 D-5.6)."""
        ...
