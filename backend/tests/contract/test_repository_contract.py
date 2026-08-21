"""The shared repository contract — T-5.12.

Every test here runs against BOTH adapters. That is the whole point: the in-memory adapter is
only a trustworthy test double if it behaves identically to the one actually deployed, and the
places the two could quietly diverge — ordering, boundary inclusivity, absent-vs-zero bytes,
idempotency under concurrency — are exactly what this file pins down (ADR-005 D-5.1).

PostgreSQL tests are skipped when TEST_DATABASE_URL is unset, so the suite still runs on a
machine without a database. They are NOT optional before a release: skipping silently is the
failure mode ADR-008 warns about, so the skip reason names what is being lost.
"""

from __future__ import annotations

import asyncio
import json
import os
from collections.abc import AsyncIterator
from datetime import UTC, datetime
from pathlib import Path

import pytest
import pytest_asyncio

from app.domain.models import IngestBatch
from app.domain.window import ResolvedWindow
from app.persistence.memory import InMemoryRepository
from app.persistence.protocol import EdgeFilters, IngestOutcome

CLUSTER = "kind-topology"
FIXTURES = Path(__file__).resolve().parents[3] / "contracts" / "examples"
TEST_DSN = os.environ.get("TEST_DATABASE_URL")

ULID_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


def ulid(n: int) -> str:
    base = list("01J8ZQ9X7K4M2N6P8R3T5V7W9Y")
    base[-1] = ULID_ALPHABET[n % 32]
    base[-2] = ULID_ALPHABET[(n // 32) % 32]
    return "".join(base)


def batch(
    n: int,
    *,
    edges: list[dict] | None = None,
    observed: str = "2026-08-10T12:00:00Z",
) -> IngestBatch:
    doc = json.loads((FIXTURES / "batch.valid.json").read_text(encoding="utf-8"))
    doc["batch_id"] = ulid(n)
    doc["observed_at"] = observed
    if edges is not None:
        doc["edges"] = edges
    return IngestBatch.model_validate(doc)


def window(start: str, end: str) -> ResolvedWindow:
    return ResolvedWindow(
        start=datetime.fromisoformat(start.replace("Z", "+00:00")),
        end=datetime.fromisoformat(end.replace("Z", "+00:00")),
    )


WIDE = window("2026-08-10T00:00:00Z", "2026-08-11T00:00:00Z")


# ── Adapter fixtures ────────────────────────────────────────────────────────────────────────


async def _make_postgres():
    """Connect, migrate, and truncate. Truncation gives each test a clean database without the
    cost of recreating the schema."""
    from app.persistence.postgres import PostgresRepository

    repo = await PostgresRepository.connect(TEST_DSN, CLUSTER)
    await repo.migrate()
    async with repo._pool.acquire() as conn:  # noqa: SLF001 - test isolation
        await conn.execute("TRUNCATE edge_buckets, nodes, ingest_batches CASCADE")
    return repo


@pytest_asyncio.fixture(params=["memory", "postgres"])
async def repo(request: pytest.FixtureRequest) -> AsyncIterator[object]:
    """Every test in this file runs twice — once per adapter.

    Each adapter is built inline rather than delegated to another async fixture: pytest cannot
    resolve one async fixture from inside another.
    """
    if request.param == "memory":
        yield InMemoryRepository(cluster_id=CLUSTER)
        return

    if not TEST_DSN:
        pytest.skip(
            "TEST_DATABASE_URL is unset — the PostgreSQL half of the contract suite did NOT run. "
            "Start one with:\n"
            "  docker run --rm -d -e POSTGRES_PASSWORD=test -p 5433:5432 postgres:17-alpine"
        )

    postgres = await _make_postgres()
    try:
        yield postgres
    finally:
        await postgres.close()


@pytest_asyncio.fixture
async def postgres_repo() -> AsyncIterator[object]:
    """PostgreSQL only, for tests that have no in-memory equivalent."""
    if not TEST_DSN:
        pytest.skip("TEST_DATABASE_URL is unset")
    postgres = await _make_postgres()
    try:
        yield postgres
    finally:
        await postgres.close()


# ── Idempotency: the guarantee agent retries depend on ──────────────────────────────────────


async def test_first_ingest_is_recorded(repo) -> None:
    assert await repo.ingest_batch(batch(1)) is IngestOutcome.INGESTED


async def test_replaying_a_batch_moves_no_counter(repo) -> None:
    await repo.ingest_batch(batch(2))
    before = [e.connection_count for e in await repo.query_edges(WIDE, EdgeFilters())]

    assert await repo.ingest_batch(batch(2)) is IngestOutcome.ALREADY_INGESTED

    after = [e.connection_count for e in await repo.query_edges(WIDE, EdgeFilters())]
    assert after == before, (
        "replaying one batch_id changed the stored counts; agent retries would inflate topology"
    )


async def test_a_distinct_batch_id_does_accumulate(repo) -> None:
    await repo.ingest_batch(batch(3))
    first = {e.key: e.connection_count for e in await repo.query_edges(WIDE, EdgeFilters())}

    await repo.ingest_batch(batch(4))
    second = {e.key: e.connection_count for e in await repo.query_edges(WIDE, EdgeFilters())}

    for key, count in first.items():
        assert second[key] == count * 2, "the same observations under a new batch_id must add"


async def test_concurrent_replays_are_counted_once(repo) -> None:
    """T-5.3. Two agents retrying the same batch simultaneously is the realistic race."""
    outcomes = await asyncio.gather(*(repo.ingest_batch(batch(5)) for _ in range(5)))

    assert outcomes.count(IngestOutcome.INGESTED) == 1, (
        "exactly one concurrent attempt may store the batch"
    )
    assert outcomes.count(IngestOutcome.ALREADY_INGESTED) == 4

    edges = await repo.query_edges(WIDE, EdgeFilters())
    assert [e.connection_count for e in edges] == [4, 143, 30], (
        "concurrent replays inflated the counts"
    )


# ── Window boundaries: half-open, lower-inclusive ───────────────────────────────────────────


async def test_window_start_is_inclusive_and_end_is_exclusive(repo) -> None:
    """T-5.7. The rule that makes graph and diff deterministic at exact bucket boundaries."""
    await repo.ingest_batch(batch(6))

    # The fixture's edges land in the 11:59 and 12:00 buckets.
    inclusive = await repo.query_edges(
        window("2026-08-10T11:59:00Z", "2026-08-10T12:01:00Z"), EdgeFilters()
    )
    assert inclusive, "a window containing the buckets must return them"

    # A window ENDING exactly at the first bucket must exclude it.
    excluded = await repo.query_edges(
        window("2026-08-10T11:00:00Z", "2026-08-10T11:59:00Z"), EdgeFilters()
    )
    assert excluded == [], "a bucket exactly at `end` belongs to the next window, not this one"

    # A window STARTING exactly at that bucket must include it.
    included = await repo.query_edges(
        window("2026-08-10T11:59:00Z", "2026-08-10T12:00:00Z"), EdgeFilters()
    )
    assert included, "a bucket exactly at `start` is inside the window"


async def test_a_window_before_any_data_is_empty(repo) -> None:
    await repo.ingest_batch(batch(7))
    assert (
        await repo.query_edges(
            window("2026-08-09T00:00:00Z", "2026-08-09T01:00:00Z"), EdgeFilters()
        )
        == []
    )


# ── Ordering ────────────────────────────────────────────────────────────────────────────────


async def test_edges_are_ordered_by_the_edge_key(repo) -> None:
    await repo.ingest_batch(batch(8))
    edges = await repo.query_edges(WIDE, EdgeFilters())
    assert [e.key for e in edges] == sorted(e.key for e in edges)


async def test_ordering_is_stable_across_repeated_queries(repo) -> None:
    await repo.ingest_batch(batch(9))
    first = [e.key for e in await repo.query_edges(WIDE, EdgeFilters())]
    for _ in range(5):
        assert [e.key for e in await repo.query_edges(WIDE, EdgeFilters())] == first


# ── Bytes: absent is not zero ───────────────────────────────────────────────────────────────


async def test_unmeasured_bytes_stay_null(repo) -> None:
    """T-5.6. The fixture sends no byte fields, so the columns must remain NULL."""
    await repo.ingest_batch(batch(10))
    for edge in await repo.query_edges(WIDE, EdgeFilters()):
        assert edge.bytes_sent is None, "absent bytes became a number; unmeasured is not zero"
        assert edge.bytes_received is None


async def test_measured_bytes_are_summed(repo) -> None:
    doc = json.loads((FIXTURES / "batch.valid-with-bytes.json").read_text(encoding="utf-8"))
    await repo.ingest_batch(batch(11, edges=doc["edges"]))
    await repo.ingest_batch(batch(12, edges=doc["edges"]))

    measured = [e for e in await repo.query_edges(WIDE, EdgeFilters()) if e.bytes_sent is not None]
    assert measured, "an edge carrying byte counts should report them"
    assert measured[0].bytes_sent == 4096 * 2


# ── Filters ─────────────────────────────────────────────────────────────────────────────────


async def test_namespace_filter_keeps_edges_leaving_the_namespace(repo) -> None:
    await repo.ingest_batch(batch(13))
    edges = await repo.query_edges(WIDE, EdgeFilters(namespaces=("demo",)))

    assert edges, "filtering to demo returned nothing"
    # backend (demo) -> redis (data) must survive: it is a dependency OF demo.
    assert any("data" in e.target_id for e in edges), (
        "a namespace filter dropped an edge leaving that namespace, hiding its dependencies"
    )


async def test_excluding_external_removes_its_edges(repo) -> None:
    await repo.ingest_batch(batch(14))
    edges = await repo.query_edges(WIDE, EdgeFilters(include_external=False))
    assert all("external:EXTERNAL" not in (e.source_id, e.target_id) for e in edges)


async def test_query_filter_is_case_insensitive(repo) -> None:
    await repo.ingest_batch(batch(15))
    lower = await repo.query_edges(WIDE, EdgeFilters(query="redis"))
    upper = await repo.query_edges(WIDE, EdgeFilters(query="REDIS"))
    assert lower and [e.key for e in lower] == [e.key for e in upper]


# ── Nodes and namespaces ────────────────────────────────────────────────────────────────────


async def test_node_lookup_returns_stored_labels(repo) -> None:
    await repo.ingest_batch(batch(16))
    edges = await repo.query_edges(WIDE, EdgeFilters())
    ids = {e.source_id for e in edges} | {e.target_id for e in edges}

    nodes = await repo.nodes_for(ids)
    assert set(nodes) == ids
    for node in nodes.values():
        assert node.label, "every node must carry a stored label, never one derived from its id"


async def test_namespaces_are_sorted_and_exclude_external(repo) -> None:
    await repo.ingest_batch(batch(17))
    assert await repo.list_namespaces(WIDE) == ["data", "demo"]


# ── Retention ───────────────────────────────────────────────────────────────────────────────


async def test_purge_removes_expired_buckets_and_orphaned_nodes(repo) -> None:
    """T-5.8."""
    await repo.ingest_batch(batch(18))
    assert await repo.query_edges(WIDE, EdgeFilters())

    stats = await repo.purge_expired(before=datetime(2026, 8, 11, tzinfo=UTC))

    assert stats.edge_buckets_deleted > 0
    assert await repo.query_edges(WIDE, EdgeFilters()) == []
    assert await repo.nodes_for({"k8s:kind-topology:demo:Deployment:client"}) == {}, (
        "a node no edge references any more should not survive retention"
    )


async def test_purge_keeps_data_inside_the_retention_window(repo) -> None:
    await repo.ingest_batch(batch(19))
    stats = await repo.purge_expired(before=datetime(2026, 8, 9, tzinfo=UTC))

    assert stats.edge_buckets_deleted == 0
    assert await repo.query_edges(WIDE, EdgeFilters()), "retention deleted data still in window"


# ── Health ──────────────────────────────────────────────────────────────────────────────────


async def test_health_reports_available(repo) -> None:
    health = await repo.health()
    assert health.available is True
    assert health.detail


async def test_health_detail_never_leaks_a_password(repo) -> None:
    """T-5.11. Whatever the detail says, it must not be a credential."""
    health = await repo.health()
    for secret in ("password", "secret", "@localhost:5433", "postgres:test"):
        assert secret not in health.detail.lower() or "***" in health.detail


# ── Durability, PostgreSQL only ─────────────────────────────────────────────────────────────


@pytest.mark.skipif(not TEST_DSN, reason="needs TEST_DATABASE_URL")
async def test_data_survives_a_new_connection_pool(postgres_repo) -> None:
    """T-5.9 in miniature: reconnecting is what a backend restart looks like to the database."""
    from app.persistence.postgres import PostgresRepository

    await postgres_repo.ingest_batch(batch(20))
    before = [e.key for e in await postgres_repo.query_edges(WIDE, EdgeFilters())]

    reconnected = await PostgresRepository.connect(TEST_DSN, CLUSTER)
    await reconnected.migrate()
    try:
        after = [e.key for e in await reconnected.query_edges(WIDE, EdgeFilters())]
        assert after == before, "committed topology did not survive a reconnect"
    finally:
        await reconnected.close()


@pytest.mark.skipif(not TEST_DSN, reason="needs TEST_DATABASE_URL")
async def test_the_database_rejects_a_disallowed_kind(postgres_repo) -> None:
    """The CHECK constraint is a second, independent layer behind the API's validation."""
    import asyncpg

    async with postgres_repo._pool.acquire() as conn:  # noqa: SLF001
        with pytest.raises(asyncpg.CheckViolationError):
            await conn.execute(
                """
                INSERT INTO nodes (id, cluster_id, kind, namespace, name, label,
                                   first_seen, last_seen)
                VALUES ('k8s:c:demo:ReplicaSet:x', 'c', 'ReplicaSet', 'demo', 'x', 'x',
                        now(), now())
                """
            )
