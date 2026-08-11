"""API surface. Route list and semantics are fixed by ADR-001 §5.3 and contracts/ids.md.

Phase 0 defines the complete contract; handlers return 501 until Phase 2 (ADR-004 §4). The
signatures here are what generate contracts/openapi.json, so they are already normative.
"""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Literal

from fastapi import APIRouter, HTTPException, Query, Response, status

from app.domain.models import (
    DiffResponse,
    EdgeDetail,
    ErrorResponse,
    GraphResponse,
    HealthResponse,
    IngestBatch,
    IngestResult,
    NamespaceList,
    NodeDetail,
    NodeKind,
)
from app.settings import settings

WindowPreset = Literal["1m", "5m", "15m", "1h", "6h", "24h"]

health_router = APIRouter(tags=["health"])
api_router = APIRouter(prefix="/api/v1", tags=["topology"])

_NOT_IMPLEMENTED = "Implemented in Phase 2 (graph/ingest) and Phase 3 (diff/history)."


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
async def health_ready(response: Response) -> HealthResponse:
    """Readiness. From Phase 3 this fails if migrations or storage checks fail (ADR-005 D-5.6)."""
    response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
    return HealthResponse(status="unavailable", checks={"storage": "not configured (Phase 0)"})


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
async def ingest_batch(batch: IngestBatch) -> IngestResult:
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
    raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=_NOT_IMPLEMENTED)


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
) -> GraphResponse:
    """Topology for one window. Nodes are derived only from edges inside that window.

    Supply exactly one of `window` or the `from`/`to` pair.
    """
    raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=_NOT_IMPLEMENTED)


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
) -> DiffResponse:
    """Deterministic comparison of two periods.

    A period with no observation contributes zero, not null. Percentage delta is undefined when
    the baseline is zero, and the reason field says so.
    """
    raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=_NOT_IMPLEMENTED)


@api_router.get("/namespaces", response_model=NamespaceList)
async def list_namespaces(
    window: Annotated[WindowPreset | None, Query()] = None,
    from_: Annotated[datetime | None, Query(alias="from")] = None,
    to: Annotated[datetime | None, Query()] = None,
) -> NamespaceList:
    """Namespaces observed in the window, for populating the filter panel."""
    raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=_NOT_IMPLEMENTED)


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
) -> NodeDetail:
    """Node detail. `node_id` contains ':' separators and must be URL-encoded by the client."""
    raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=_NOT_IMPLEMENTED)


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
) -> EdgeDetail:
    """Edge detail. `edge_id` is `source|target|protocol|port` and must be URL-encoded."""
    raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=_NOT_IMPLEMENTED)
