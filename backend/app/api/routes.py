"""API surface. Route list and semantics are fixed by ADR-001 §5.3 and contracts/ids.md.

Phase 0 defines the complete contract; handlers return 501 until Phase 2 (ADR-004 §4). The
signatures here are what generate contracts/openapi.json, so they are already normative.
"""

from __future__ import annotations

import logging
import time
from datetime import datetime, timedelta
from typing import Annotated, Literal

from fastapi import APIRouter, HTTPException, Query, Request, Response, status
from fastapi.responses import PlainTextResponse

from app.api.dependencies import ClockDep, RepositoryDep
from app.domain import diff, graph
from app.domain import window as win
from app.domain.models import (
    DiffResponse,
    EdgeDetail,
    EffectiveFilters,
    ErrorResponse,
    GraphResponse,
    HealthResponse,
    IngestBatch,
    IngestResult,
    NamespaceList,
    NodeDetail,
    NodeKind,
    build_edge_id,
)
from app.domain.window import ResolvedWindow
from app.persistence.protocol import EdgeFilters, IngestOutcome
from app.settings import settings

WindowPreset = Literal["1m", "5m", "15m", "1h", "6h", "24h"]

health_router = APIRouter(tags=["health"])
api_router = APIRouter(prefix="/api/v1", tags=["topology"])


def _resolve(*, window, start, end, clock) -> ResolvedWindow:
    """Resolve a window, mapping a bad request to 422 rather than a 500."""
    try:
        return win.resolve(
            now=clock(),
            window=window,
            start=start,
            end=end,
            max_span=timedelta(hours=settings.max_query_span_hours),
        )
    except win.WindowError as exc:
        raise HTTPException(
            status_code=status.HTTP_422_UNPROCESSABLE_CONTENT, detail=str(exc)
        ) from exc


def _to_edge_filters(effective: EffectiveFilters) -> EdgeFilters:
    return EdgeFilters(
        namespaces=tuple(effective.namespaces),
        kind=effective.kind,
        query=effective.query,
        include_external=effective.include_external,
        include_unresolved=effective.include_unresolved,
    )


# ── Health (outside the version prefix, ADR-003 D-3.8) ──────────────────────────────────────


@health_router.get("/health/live", response_model=HealthResponse)
async def health_live() -> HealthResponse:
    """Process liveness only. Never touches storage."""
    return HealthResponse(status="ok", checks={"process": "ok"})


@health_router.get(
    "/health/ready",
    response_model=HealthResponse,
    responses={503: {"model": HealthResponse}},
)
async def health_ready(response: Response, repository: RepositoryDep) -> HealthResponse:
    """Readiness, gated on storage.

    Kubernetes takes this pod out of service when it fails, which is what stops the frontend
    querying a backend that cannot answer. From Phase 3 it also covers migrations (ADR-005 D-5.6).
    """
    health = await repository.health()
    if not health.available:
        response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
        return HealthResponse(status="unavailable", checks={"storage": health.detail})
    return HealthResponse(status="ok", checks={"storage": health.detail})


@health_router.get("/metrics", response_class=PlainTextResponse, include_in_schema=False)
async def metrics(request: Request, repository: RepositoryDep) -> PlainTextResponse:
    """Prometheus text format.

    Excluded from the OpenAPI document deliberately: it is text/plain, not JSON, and the
    generated TypeScript client has no use for it (ADR-003 D-3.8).
    """
    stored = 0
    counter = getattr(repository, "stored_bucket_count", None)
    if counter is not None:
        stored = await counter()
    return PlainTextResponse(request.app.state.metrics.render(stored_buckets=stored))


# ── Ingestion ───────────────────────────────────────────────────────────────────────────────


@api_router.post(
    "/ingest/batches",
    response_model=IngestResult,
    status_code=status.HTTP_202_ACCEPTED,
    responses={
        200: {"model": IngestResult, "description": "batch_id already ingested; no state change"},
        400: {"model": ErrorResponse, "description": "Unsupported schema_version"},
        413: {"model": ErrorResponse, "description": "Body or edge count over limit"},
        422: {"model": ErrorResponse, "description": "Malformed batch"},
        503: {"model": ErrorResponse, "description": "Storage unavailable; retry"},
    },
)
async def ingest_batch(
    batch: IngestBatch,
    request: Request,
    response: Response,
    repository: RepositoryDep,
) -> IngestResult:
    """Idempotent ingestion. Re-posting a `batch_id` returns 200 and changes nothing.

    Version checking happens here rather than in the model so that an unsupported version yields
    400 rather than Pydantic's 422 (contracts/ids.md §8).
    """
    if batch.schema_version != settings.supported_schema_version:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=(
                f"unsupported schema_version {batch.schema_version}; "
                f"this release accepts {settings.supported_schema_version}"
            ),
        )
    if len(batch.edges) > settings.max_batch_edges:
        raise HTTPException(
            status_code=status.HTTP_413_CONTENT_TOO_LARGE,
            detail=f"batch contains {len(batch.edges)} edges; limit is {settings.max_batch_edges}",
        )

    # Idempotency is decided inside the repository, in the same transaction as the edge merge.
    # A read-then-write pre-check here would be a race under two agents retrying at once
    # (ADR-004 D-4.3).
    outcome = await repository.ingest_batch(batch)

    # 200 means "already stored": success for the agent, which must drop the batch rather than
    # retry it forever (contracts/ids.md §8).
    response.status_code = (
        status.HTTP_200_OK
        if outcome is IngestOutcome.ALREADY_INGESTED
        else status.HTTP_202_ACCEPTED
    )

    request.app.state.metrics.record_ingest(deduplicated=outcome is IngestOutcome.ALREADY_INGESTED)

    logging.getLogger("api.ingest").info(
        "batch ingested",
        extra={
            "batch_id": batch.batch_id,
            "agent_id": batch.agent_id,
            "outcome": outcome.value,
            "edges": len(batch.edges),
        },
    )

    return IngestResult(
        batch_id=batch.batch_id,
        status=outcome.value,
        edges_accepted=len(batch.edges) if outcome is IngestOutcome.INGESTED else 0,
    )


# ── Graph ───────────────────────────────────────────────────────────────────────────────────


@api_router.get(
    "/graph",
    response_model=GraphResponse,
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def get_graph(
    window: Annotated[
        WindowPreset | None, Query(description="Preset window. Mutually exclusive with from/to.")
    ] = None,
    from_: Annotated[
        datetime | None, Query(alias="from", description="RFC 3339 UTC, inclusive.")
    ] = None,
    to: Annotated[datetime | None, Query(description="RFC 3339 UTC, exclusive.")] = None,
    namespace: Annotated[list[str] | None, Query(description="Repeatable.")] = None,
    kind: Annotated[NodeKind | None, Query()] = None,
    query: Annotated[
        str | None, Query(description="Case-insensitive substring match on label.")
    ] = None,
    include_external: Annotated[bool, Query()] = True,
    include_unresolved: Annotated[bool, Query()] = False,
    request: Request = None,  # noqa: RUF013
    repository: RepositoryDep = None,  # noqa: RUF013
    clock: ClockDep = None,  # noqa: RUF013
) -> GraphResponse:
    """Topology for one window. Nodes are derived only from edges inside that window.

    Supply exactly one of `window` or the `from`/`to` pair.
    """
    started = time.perf_counter()
    resolved = _resolve(window=window, start=from_, end=to, clock=clock)
    effective = EffectiveFilters(
        namespaces=sorted(namespace or []),
        kind=kind,
        query=query,
        include_external=include_external,
        include_unresolved=include_unresolved,
    )

    edges = await repository.query_edges(resolved, _to_edge_filters(effective))
    nodes = await repository.nodes_for({e.source_id for e in edges} | {e.target_id for e in edges})

    assembled = graph.assemble(
        edges=edges,
        nodes=nodes,
        window=resolved,
        filters=effective,
        generated_at=clock(),
        max_nodes=settings.graph_max_nodes,
        max_edges=settings.graph_max_edges,
    )
    request.app.state.metrics.record_graph_query(time.perf_counter() - started)
    return assembled


@api_router.get(
    "/diff",
    response_model=DiffResponse,
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def get_diff(
    baseline_from: Annotated[datetime, Query()],
    baseline_to: Annotated[datetime, Query()],
    current_from: Annotated[datetime, Query()],
    current_to: Annotated[datetime, Query()],
    namespace: Annotated[list[str] | None, Query()] = None,
    kind: Annotated[NodeKind | None, Query()] = None,
    query: Annotated[str | None, Query()] = None,
    include_external: Annotated[bool, Query()] = True,
    include_unresolved: Annotated[bool, Query()] = False,
    include_unchanged: Annotated[bool, Query()] = False,
    request: Request = None,  # noqa: RUF013
    repository: RepositoryDep = None,  # noqa: RUF013
    clock: ClockDep = None,  # noqa: RUF013
) -> DiffResponse:
    """Deterministic comparison of two periods.

    A period with no observation contributes zero, not null. Percentage delta is undefined when
    the baseline is zero, and the reason field says so.
    """
    baseline_window = _resolve(window=None, start=baseline_from, end=baseline_to, clock=clock)
    current_window = _resolve(window=None, start=current_from, end=current_to, clock=clock)

    try:
        win.validate_diff_periods(baseline_window, current_window)
    except win.WindowError as exc:
        raise HTTPException(
            status_code=status.HTTP_422_UNPROCESSABLE_CONTENT, detail=str(exc)
        ) from exc

    effective = EffectiveFilters(
        namespaces=sorted(namespace or []),
        kind=kind,
        query=query,
        include_external=include_external,
        include_unresolved=include_unresolved,
    )
    edge_filters = _to_edge_filters(effective)

    result = diff.compare(
        baseline_edges=await repository.query_edges(baseline_window, edge_filters),
        current_edges=await repository.query_edges(current_window, edge_filters),
        baseline_window=baseline_window,
        current_window=current_window,
        threshold_percent=settings.topology_diff_change_threshold_percent,
        filters=effective,
        generated_at=clock(),
        include_unchanged=include_unchanged,
        max_edges=settings.graph_max_edges,
    )
    request.app.state.metrics.record_diff_query()
    return result


@api_router.get("/namespaces", response_model=NamespaceList)
async def list_namespaces(
    window: Annotated[WindowPreset | None, Query()] = None,
    from_: Annotated[datetime | None, Query(alias="from")] = None,
    to: Annotated[datetime | None, Query()] = None,
    repository: RepositoryDep = None,  # noqa: RUF013
    clock: ClockDep = None,  # noqa: RUF013
) -> NamespaceList:
    """Namespaces observed in the window, for populating the filter panel."""
    resolved = _resolve(window=window, start=from_, end=to, clock=clock)
    return NamespaceList(
        window=resolved.as_model(),
        namespaces=await repository.list_namespaces(resolved),
    )


@api_router.get(
    "/nodes/{node_id}",
    response_model=NodeDetail,
    responses={404: {"model": ErrorResponse}},
)
async def get_node(
    node_id: str,
    window: Annotated[WindowPreset | None, Query()] = None,
    from_: Annotated[datetime | None, Query(alias="from")] = None,
    to: Annotated[datetime | None, Query()] = None,
    repository: RepositoryDep = None,  # noqa: RUF013
    clock: ClockDep = None,  # noqa: RUF013
) -> NodeDetail:
    """Node detail. `node_id` contains ':' separators and must be URL-encoded by the client."""
    resolved = _resolve(window=window, start=from_, end=to, clock=clock)
    edges = await repository.query_edges(resolved, EdgeFilters())
    nodes = await repository.nodes_for(
        {e.source_id for e in edges} | {e.target_id for e in edges} | {node_id}
    )

    detail = graph.node_detail(node_id=node_id, edges=edges, nodes=nodes, window=resolved)
    if detail is None:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"no node {node_id!r} observed in this window",
        )
    return detail


@api_router.get(
    "/edges/{edge_id}",
    response_model=EdgeDetail,
    responses={404: {"model": ErrorResponse}},
)
async def get_edge(
    edge_id: str,
    window: Annotated[WindowPreset | None, Query()] = None,
    from_: Annotated[datetime | None, Query(alias="from")] = None,
    to: Annotated[datetime | None, Query()] = None,
    repository: RepositoryDep = None,  # noqa: RUF013
    clock: ClockDep = None,  # noqa: RUF013
) -> EdgeDetail:
    """Edge detail. `edge_id` is `source|target|protocol|port` and must be URL-encoded."""
    resolved = _resolve(window=window, start=from_, end=to, clock=clock)
    edges = await repository.query_edges(resolved, EdgeFilters())
    nodes = await repository.nodes_for({e.source_id for e in edges} | {e.target_id for e in edges})

    for aggregate in edges:
        candidate = build_edge_id(
            aggregate.source_id,
            aggregate.target_id,
            aggregate.protocol,
            aggregate.destination_port,
        )
        if candidate != edge_id:
            continue
        source, target = nodes.get(aggregate.source_id), nodes.get(aggregate.target_id)
        if source is None or target is None:
            break
        return EdgeDetail(
            edge=graph._to_edge(aggregate),
            window=resolved.as_model(),
            source=graph._to_node(source),
            target=graph._to_node(target),
        )

    raise HTTPException(
        status_code=status.HTTP_404_NOT_FOUND,
        detail=f"no edge {edge_id!r} observed in this window",
    )
