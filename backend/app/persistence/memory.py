"""In-memory storage adapter.

Not a stub. It implements the same semantics as PostgreSQL — idempotency, one-minute bucketing,
half-open windows, deterministic ordering — so the contract-test suite is meaningful when run
against it (ADR-005 D-5.1).

It is the deployed adapter only in Phase 2. From Phase 3 it is retained permanently for fast unit
tests, which is what keeps the slow database-backed tests a minority.
"""

from __future__ import annotations

import asyncio
from datetime import datetime

from app.domain.models import IngestBatch, NodeRef
from app.domain.window import ResolvedWindow, bucket_start
from app.persistence.protocol import (
    EdgeAggregate,
    EdgeFilters,
    IngestOutcome,
    PurgeStats,
    StorageHealth,
    StoredNode,
)

# (bucket_start, source_id, target_id, protocol, destination_port) — the edge key with the
# bucket prepended, mirroring the edge_buckets primary key (contracts/ids.md §4).
BucketKey = tuple[datetime, str, str, str, int]


class _Bucket:
    __slots__ = ("connection_count", "bytes_sent", "bytes_received", "first_seen", "last_seen")

    def __init__(self, connection_count: int, first_seen: datetime, last_seen: datetime) -> None:
        self.connection_count = connection_count
        self.bytes_sent: int | None = None
        self.bytes_received: int | None = None
        self.first_seen = first_seen
        self.last_seen = last_seen


class InMemoryRepository:
    """Implements TopologyRepository. Safe for concurrent use via a single lock."""

    def __init__(self, cluster_id: str) -> None:
        self._cluster_id = cluster_id
        self._lock = asyncio.Lock()
        self._batches: set[str] = set()
        self._buckets: dict[BucketKey, _Bucket] = {}
        self._nodes: dict[str, StoredNode] = {}

    # ── Ingestion ───────────────────────────────────────────────────────────────────────────

    async def ingest_batch(self, batch: IngestBatch) -> IngestOutcome:
        async with self._lock:
            # The batch-id gate comes first and short-circuits everything. Under the lock this
            # is atomic, mirroring the single transaction PostgreSQL uses.
            if batch.batch_id in self._batches:
                return IngestOutcome.ALREADY_INGESTED
            self._batches.add(batch.batch_id)

            for edge in batch.edges:
                self._upsert_node(edge.source, edge.first_seen, edge.last_seen)
                self._upsert_node(edge.target, edge.first_seen, edge.last_seen)

                key: BucketKey = (
                    bucket_start(edge.last_seen),
                    edge.source.id,
                    edge.target.id,
                    edge.protocol,
                    edge.destination_port,
                )

                existing = self._buckets.get(key)
                if existing is None:
                    bucket = _Bucket(edge.connection_count, edge.first_seen, edge.last_seen)
                    bucket.bytes_sent = edge.bytes_sent
                    bucket.bytes_received = edge.bytes_received
                    self._buckets[key] = bucket
                    continue

                existing.connection_count += edge.connection_count
                existing.first_seen = min(existing.first_seen, edge.first_seen)
                existing.last_seen = max(existing.last_seen, edge.last_seen)
                # Absent stays absent: only a measured value promotes the column away from None
                # (contracts/ids.md §10).
                if edge.bytes_sent is not None:
                    existing.bytes_sent = (existing.bytes_sent or 0) + edge.bytes_sent
                if edge.bytes_received is not None:
                    existing.bytes_received = (existing.bytes_received or 0) + edge.bytes_received

            return IngestOutcome.INGESTED

    def _upsert_node(self, ref: NodeRef, first_seen: datetime, last_seen: datetime) -> None:
        existing = self._nodes.get(ref.id)
        if existing is None:
            self._nodes[ref.id] = StoredNode(
                id=ref.id,
                cluster_id=self._cluster_id,
                kind=ref.kind,
                namespace=ref.namespace,
                name=ref.name,
                # Stored, never derived by parsing the id (contracts/ids.md §2).
                label=ref.name,
                first_seen=first_seen,
                last_seen=last_seen,
            )
            return

        self._nodes[ref.id] = StoredNode(
            id=existing.id,
            cluster_id=existing.cluster_id,
            kind=existing.kind,
            namespace=existing.namespace,
            name=existing.name,
            label=existing.label,
            first_seen=min(existing.first_seen, first_seen),
            last_seen=max(existing.last_seen, last_seen),
            attributes=existing.attributes,
        )

    # ── Queries ─────────────────────────────────────────────────────────────────────────────

    async def query_edges(
        self, window: ResolvedWindow, filters: EdgeFilters
    ) -> list[EdgeAggregate]:
        async with self._lock:
            summed: dict[tuple[str, str, str, int], _Bucket] = {}

            for (bucket, source_id, target_id, protocol, port), value in self._buckets.items():
                # Half-open: a bucket exactly at `end` is excluded.
                if not (window.start <= bucket < window.end):
                    continue
                if not self._passes(source_id, target_id, filters):
                    continue

                edge_key = (source_id, target_id, protocol, port)
                acc = summed.get(edge_key)
                if acc is None:
                    acc = _Bucket(0, value.first_seen, value.last_seen)
                    summed[edge_key] = acc
                acc.connection_count += value.connection_count
                acc.first_seen = min(acc.first_seen, value.first_seen)
                acc.last_seen = max(acc.last_seen, value.last_seen)
                if value.bytes_sent is not None:
                    acc.bytes_sent = (acc.bytes_sent or 0) + value.bytes_sent
                if value.bytes_received is not None:
                    acc.bytes_received = (acc.bytes_received or 0) + value.bytes_received

            aggregates = [
                EdgeAggregate(
                    source_id=source_id,
                    target_id=target_id,
                    protocol=protocol,
                    destination_port=port,
                    connection_count=acc.connection_count,
                    first_seen=acc.first_seen,
                    last_seen=acc.last_seen,
                    bytes_sent=acc.bytes_sent,
                    bytes_received=acc.bytes_received,
                )
                for (source_id, target_id, protocol, port), acc in summed.items()
            ]

            # Explicit sort. Dict order happens to be insertion order in CPython, but relying on
            # that would make the ordering guarantee an accident (contracts/ids.md §9).
            aggregates.sort(key=lambda e: e.key)
            return aggregates

    def _passes(self, source_id: str, target_id: str, filters: EdgeFilters) -> bool:
        source = self._nodes.get(source_id)
        target = self._nodes.get(target_id)
        if source is None or target is None:
            return False

        if not filters.include_external and any(
            n.id == "external:EXTERNAL" for n in (source, target)
        ):
            return False

        # An edge survives a filter if EITHER endpoint matches: filtering by namespace should
        # still show what that namespace talks to, not just traffic entirely inside it.
        if filters.namespaces:
            wanted = set(filters.namespaces)
            if not ({source.namespace, target.namespace} & wanted):
                return False

        if filters.kind and filters.kind not in {source.kind, target.kind}:
            return False

        if filters.query:
            needle = filters.query.casefold()
            if needle not in source.label.casefold() and needle not in target.label.casefold():
                return False

        return True

    async def nodes_for(self, node_ids: set[str]) -> dict[str, StoredNode]:
        async with self._lock:
            return {nid: self._nodes[nid] for nid in node_ids if nid in self._nodes}

    async def list_namespaces(self, window: ResolvedWindow) -> list[str]:
        edges = await self.query_edges(window, EdgeFilters())
        async with self._lock:
            namespaces = {
                node.namespace
                for edge in edges
                for nid in (edge.source_id, edge.target_id)
                if (node := self._nodes.get(nid)) is not None and node.namespace
            }
        return sorted(namespaces)

    # ── Maintenance ─────────────────────────────────────────────────────────────────────────

    async def purge_expired(self, before: datetime) -> PurgeStats:
        async with self._lock:
            expired = [key for key in self._buckets if key[0] < before]
            for key in expired:
                del self._buckets[key]

            referenced = {key[1] for key in self._buckets} | {key[2] for key in self._buckets}
            orphaned = [nid for nid in self._nodes if nid not in referenced]
            for nid in orphaned:
                del self._nodes[nid]

            return PurgeStats(edge_buckets_deleted=len(expired), nodes_deleted=len(orphaned))

    async def health(self) -> StorageHealth:
        return StorageHealth(
            available=True,
            detail="in-memory adapter (Phase 2 only; not durable across restarts)",
        )

    # ── Test/metric helpers ─────────────────────────────────────────────────────────────────

    async def stored_bucket_count(self) -> int:
        async with self._lock:
            return len(self._buckets)

    async def ingested_batch_count(self) -> int:
        async with self._lock:
            return len(self._batches)
