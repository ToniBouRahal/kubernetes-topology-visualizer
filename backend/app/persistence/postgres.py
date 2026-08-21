"""PostgreSQL storage adapter.

Implements the same TopologyRepository contract as the in-memory adapter, and the shared contract
suite runs against both. Where they can drift — ordering, boundary inclusivity, absent-vs-zero
bytes — that suite is the thing that catches it (ADR-005 D-5.1).
"""

from __future__ import annotations

import logging
import re
from datetime import datetime
from pathlib import Path

import asyncpg

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

log = logging.getLogger("persistence.postgres")

MIGRATIONS_DIR = Path(__file__).resolve().parents[2] / "migrations"

# Retention deletes in bounded batches so a large purge cannot hold a long transaction against
# ingestion (ADR-005 D-5.5).
PURGE_BATCH = 5_000


def sanitise_dsn(dsn: str) -> str:
    """Strip the password from a DSN before it can reach a log or an error.

    asyncpg and libpq embed the full DSN in connection errors, so any handler that formats an
    exception would otherwise publish the database password to whoever triggered the failure
    (ADR-005 D-5.7, test T-5.11).
    """
    return re.sub(r"(?<=://)([^:/@]+):([^@]*)@", r"\1:***@", dsn)


class PostgresRepository:
    """Durable storage. Construct via `connect`."""

    def __init__(self, pool: asyncpg.Pool, cluster_id: str, dsn: str) -> None:
        self._pool = pool
        self._cluster_id = cluster_id
        self._safe_dsn = sanitise_dsn(dsn)
        self._migrations_applied = False

    # ── Lifecycle ───────────────────────────────────────────────────────────────────────────

    @classmethod
    async def connect(
        cls, dsn: str, cluster_id: str, *, min_size: int = 2, max_size: int = 10
    ) -> PostgresRepository:
        try:
            pool = await asyncpg.create_pool(
                dsn, min_size=min_size, max_size=max_size, command_timeout=30
            )
        except Exception as exc:  # noqa: BLE001 - re-raised with the DSN removed
            raise ConnectionError(
                f"could not connect to PostgreSQL at {sanitise_dsn(dsn)}: {type(exc).__name__}"
            ) from None
        if pool is None:  # pragma: no cover - asyncpg returns None only on misuse
            raise ConnectionError("asyncpg returned no pool")
        return cls(pool, cluster_id, dsn)

    async def close(self) -> None:
        await self._pool.close()

    async def migrate(self) -> list[str]:
        """Apply pending migrations in filename order, inside a transaction each.

        Readiness depends on this having succeeded: starting an API against a half-built schema
        produces errors that look like data problems (ADR-005 D-5.6).
        """
        # An absent directory previously made glob() return nothing, so migrate() logged
        # "applied: []" and the backend started against an EMPTY database — reporting success
        # for the exact failure this method exists to prevent.
        if not MIGRATIONS_DIR.is_dir():
            raise RuntimeError(
                f"migrations directory not found at {MIGRATIONS_DIR}; "
                "the image is missing its schema"
            )

        available = sorted(MIGRATIONS_DIR.glob("*.sql"))
        if not available:
            raise RuntimeError(f"no .sql migrations found in {MIGRATIONS_DIR}")

        applied: list[str] = []
        async with self._pool.acquire() as conn:
            await conn.execute(
                """
                CREATE TABLE IF NOT EXISTS schema_migrations (
                    version     TEXT PRIMARY KEY,
                    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
                )
                """
            )
            done = {r["version"] for r in await conn.fetch("SELECT version FROM schema_migrations")}

            for path in available:
                if path.name in done:
                    continue
                async with conn.transaction():
                    await conn.execute(path.read_text(encoding="utf-8"))
                    await conn.execute(
                        "INSERT INTO schema_migrations (version) VALUES ($1)", path.name
                    )
                applied.append(path.name)
                log.info("migration applied", extra={"version": path.name})

        self._migrations_applied = True
        return applied

    # ── Ingestion ───────────────────────────────────────────────────────────────────────────

    async def ingest_batch(self, batch: IngestBatch) -> IngestOutcome:
        """Record a batch atomically.

        THE transaction. Three properties, each of which a test proves:

        1. The batch-id insert comes FIRST and gates everything. A duplicate returns before any
           counter moves, which is what makes agent retries safe.
        2. It is ONE transaction. A crash between the batch record and the edge merge would
           otherwise leave a retry either double-counting or being rejected as already-ingested
           with nothing stored — both are silent corruption.
        3. Nodes are inserted before edges (foreign keys) and in sorted order, so two agents
           writing overlapping node sets cannot deadlock by taking locks in opposite orders.
        """
        async with self._pool.acquire() as conn, conn.transaction():
            inserted = await conn.fetchval(
                """
                INSERT INTO ingest_batches (batch_id, cluster_id, agent_id, observed_at)
                VALUES ($1, $2, $3, $4)
                ON CONFLICT (batch_id) DO NOTHING
                RETURNING batch_id
                """,
                batch.batch_id,
                batch.cluster_id,
                batch.agent_id,
                batch.observed_at,
            )

            if inserted is None:
                # Already stored. Touch nothing else.
                return IngestOutcome.ALREADY_INGESTED

            nodes: dict[str, tuple[NodeRef, datetime, datetime]] = {}
            for edge in batch.edges:
                for ref in (edge.source, edge.target):
                    existing = nodes.get(ref.id)
                    if existing is None:
                        nodes[ref.id] = (ref, edge.first_seen, edge.last_seen)
                    else:
                        _, first, last = existing
                        nodes[ref.id] = (
                            ref,
                            min(first, edge.first_seen),
                            max(last, edge.last_seen),
                        )

            # Sorted: deterministic lock order between concurrent agents.
            for node_id in sorted(nodes):
                ref, first_seen, last_seen = nodes[node_id]
                await conn.execute(
                    """
                    INSERT INTO nodes (id, cluster_id, kind, namespace, name, label,
                                       first_seen, last_seen)
                    VALUES ($1, $2, $3, $4, $5, $5, $6, $7)
                    ON CONFLICT (id) DO UPDATE SET
                        first_seen = LEAST(nodes.first_seen, EXCLUDED.first_seen),
                        last_seen  = GREATEST(nodes.last_seen, EXCLUDED.last_seen)
                    """,
                    ref.id,
                    batch.cluster_id,
                    ref.kind,
                    ref.namespace,
                    ref.name,
                    first_seen,
                    last_seen,
                )

            for edge in batch.edges:
                await conn.execute(
                    """
                    INSERT INTO edge_buckets (
                        bucket_start, cluster_id, source_id, target_id, protocol,
                        destination_port, connection_count, bytes_sent, bytes_received,
                        first_seen, last_seen
                    )
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
                    ON CONFLICT (bucket_start, cluster_id, source_id, target_id, protocol,
                                 destination_port)
                    DO UPDATE SET
                        connection_count = edge_buckets.connection_count
                                           + EXCLUDED.connection_count,
                        -- A NULL stays NULL until something real is measured, so "not measured"
                        -- never silently becomes "measured as zero".
                        bytes_sent = CASE
                            WHEN edge_buckets.bytes_sent IS NULL AND EXCLUDED.bytes_sent IS NULL
                                THEN NULL
                            ELSE COALESCE(edge_buckets.bytes_sent, 0)
                                 + COALESCE(EXCLUDED.bytes_sent, 0)
                        END,
                        bytes_received = CASE
                            WHEN edge_buckets.bytes_received IS NULL
                                 AND EXCLUDED.bytes_received IS NULL
                                THEN NULL
                            ELSE COALESCE(edge_buckets.bytes_received, 0)
                                 + COALESCE(EXCLUDED.bytes_received, 0)
                        END,
                        first_seen = LEAST(edge_buckets.first_seen, EXCLUDED.first_seen),
                        last_seen  = GREATEST(edge_buckets.last_seen, EXCLUDED.last_seen)
                    """,
                    bucket_start(edge.last_seen),
                    batch.cluster_id,
                    edge.source.id,
                    edge.target.id,
                    edge.protocol,
                    edge.destination_port,
                    edge.connection_count,
                    edge.bytes_sent,
                    edge.bytes_received,
                    edge.first_seen,
                    edge.last_seen,
                )

            return IngestOutcome.INGESTED

    # ── Queries ─────────────────────────────────────────────────────────────────────────────

    async def query_edges(
        self, window: ResolvedWindow, filters: EdgeFilters
    ) -> list[EdgeAggregate]:
        """Edges whose buckets fall in [start, end), summed and ordered.

        The window is half-open in SQL exactly as it is in the in-memory adapter: >= start and
        < end. A bucket landing precisely on `end` belongs to the next window, and Phase 3 gates
        on that being true at exact boundaries (ADR-005 D-5.4).
        """
        conditions = [
            "b.cluster_id = $1",
            "b.bucket_start >= $2",
            "b.bucket_start < $3",
        ]
        params: list[object] = [self._cluster_id, window.start, window.end]

        if not filters.include_external:
            conditions.append("s.id <> 'external:EXTERNAL' AND t.id <> 'external:EXTERNAL'")

        if filters.namespaces:
            params.append(list(filters.namespaces))
            # EITHER endpoint matching keeps edges LEAVING a selected namespace, so a dependency
            # on another namespace stays visible rather than being filtered away.
            conditions.append(
                f"(s.namespace = ANY(${len(params)}) OR t.namespace = ANY(${len(params)}))"
            )

        if filters.kind:
            params.append(filters.kind)
            conditions.append(f"(s.kind = ${len(params)} OR t.kind = ${len(params)})")

        if filters.query:
            params.append(f"%{filters.query.lower()}%")
            conditions.append(
                f"(lower(s.label) LIKE ${len(params)} OR lower(t.label) LIKE ${len(params)})"
            )

        sql = f"""
            SELECT
                b.source_id,
                b.target_id,
                b.protocol,
                b.destination_port,
                SUM(b.connection_count)::BIGINT      AS connection_count,
                MIN(b.first_seen)                    AS first_seen,
                MAX(b.last_seen)                     AS last_seen,
                -- SUM over all-NULL returns NULL, which is exactly the semantics wanted:
                -- unmeasured stays unmeasured.
                SUM(b.bytes_sent)::BIGINT            AS bytes_sent,
                SUM(b.bytes_received)::BIGINT        AS bytes_received
            FROM edge_buckets b
            JOIN nodes s ON s.id = b.source_id
            JOIN nodes t ON t.id = b.target_id
            WHERE {" AND ".join(conditions)}
            GROUP BY b.source_id, b.target_id, b.protocol, b.destination_port
            ORDER BY b.source_id, b.target_id, b.protocol, b.destination_port
        """

        async with self._pool.acquire() as conn:
            rows = await conn.fetch(sql, *params)

        return [
            EdgeAggregate(
                source_id=r["source_id"],
                target_id=r["target_id"],
                protocol=r["protocol"],
                destination_port=r["destination_port"],
                connection_count=r["connection_count"],
                first_seen=r["first_seen"],
                last_seen=r["last_seen"],
                bytes_sent=r["bytes_sent"],
                bytes_received=r["bytes_received"],
            )
            for r in rows
        ]

    async def nodes_for(self, node_ids: set[str]) -> dict[str, StoredNode]:
        if not node_ids:
            return {}
        async with self._pool.acquire() as conn:
            rows = await conn.fetch(
                """
                SELECT id, cluster_id, kind, namespace, name, label,
                       attributes_json, first_seen, last_seen
                FROM nodes
                WHERE id = ANY($1)
                """,
                list(node_ids),
            )
        return {
            r["id"]: StoredNode(
                id=r["id"],
                cluster_id=r["cluster_id"],
                kind=r["kind"],
                namespace=r["namespace"],
                name=r["name"],
                label=r["label"],
                first_seen=r["first_seen"],
                last_seen=r["last_seen"],
            )
            for r in rows
        }

    async def list_namespaces(self, window: ResolvedWindow) -> list[str]:
        async with self._pool.acquire() as conn:
            rows = await conn.fetch(
                """
                SELECT DISTINCT n.namespace
                FROM edge_buckets b
                JOIN nodes n ON n.id IN (b.source_id, b.target_id)
                WHERE b.cluster_id = $1
                  AND b.bucket_start >= $2
                  AND b.bucket_start < $3
                  AND n.namespace IS NOT NULL
                ORDER BY n.namespace
                """,
                self._cluster_id,
                window.start,
                window.end,
            )
        return [r["namespace"] for r in rows]

    # ── Maintenance ─────────────────────────────────────────────────────────────────────────

    async def purge_expired(self, before: datetime) -> PurgeStats:
        """Delete expired buckets, then nodes nothing references any more.

        Bounded batches: an unbounded DELETE over a day of buckets holds locks long enough to
        stall ingestion, which would show up as agent retries rather than as a slow purge.
        """
        buckets_deleted = 0
        async with self._pool.acquire() as conn:
            while True:
                # ctid is the cheapest stable row handle for a bounded delete, and avoids
                # repeating the six-column primary key in a self-join.
                status = await conn.execute(
                    """
                    DELETE FROM edge_buckets
                    WHERE ctid IN (
                        SELECT ctid FROM edge_buckets
                        WHERE bucket_start < $1
                        LIMIT $2
                    )
                    """,
                    before,
                    PURGE_BATCH,
                )
                # asyncpg returns the command tag, e.g. "DELETE 5000".
                removed = int(status.split()[-1]) if status else 0
                buckets_deleted += removed
                if removed < PURGE_BATCH:
                    break

            nodes_deleted = await conn.fetchval(
                """
                WITH orphaned AS (
                    DELETE FROM nodes n
                    WHERE NOT EXISTS (
                        SELECT 1 FROM edge_buckets b
                        WHERE b.source_id = n.id OR b.target_id = n.id
                    )
                    RETURNING 1
                )
                SELECT count(*) FROM orphaned
                """
            )

            await conn.execute("DELETE FROM ingest_batches WHERE received_at < $1", before)

        return PurgeStats(edge_buckets_deleted=buckets_deleted, nodes_deleted=nodes_deleted or 0)

    async def health(self) -> StorageHealth:
        if not self._migrations_applied:
            return StorageHealth(
                available=False,
                detail="migrations have not been applied",
                migrations_applied=False,
            )
        try:
            async with self._pool.acquire() as conn:
                await conn.fetchval("SELECT 1")
        except Exception as exc:  # noqa: BLE001 - the message must not carry the DSN
            return StorageHealth(
                available=False,
                # Type name only. Never str(exc): asyncpg embeds the DSN, password included.
                detail=f"PostgreSQL unreachable ({type(exc).__name__})",
                migrations_applied=True,
            )
        return StorageHealth(available=True, detail=f"PostgreSQL at {self._safe_dsn}")

    # ── Helpers used by metrics and tests ───────────────────────────────────────────────────

    async def stored_bucket_count(self) -> int:
        async with self._pool.acquire() as conn:
            return await conn.fetchval("SELECT count(*) FROM edge_buckets") or 0

    async def ingested_batch_count(self) -> int:
        async with self._pool.acquire() as conn:
            return await conn.fetchval("SELECT count(*) FROM ingest_batches") or 0
