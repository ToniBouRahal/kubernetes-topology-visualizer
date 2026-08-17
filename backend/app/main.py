"""Application factory.

Layering is fixed by ADR-004 D-4.1: api / domain / persistence, with no import from domain back
into api or persistence.
"""

from __future__ import annotations

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from app.api.dependencies import utc_now
from app.api.logging import RequestContextMiddleware, configure_logging
from app.api.routes import api_router, health_router
from app.persistence.memory import InMemoryRepository
from app.persistence.protocol import TopologyRepository
from app.settings import settings

DESCRIPTION = """
Runtime service topology for Kubernetes, collected with eBPF.

This document is the contract between the backend, the Go node agent, and the React frontend.
It is generated from the FastAPI application and committed as `contracts/openapi.json`; CI fails
if the committed copy drifts. Never hand-edit it.

Identity, edge keys, status codes, and determinism rules are specified in `contracts/ids.md`.
""".strip()


def create_app(repository: TopologyRepository | None = None) -> FastAPI:
    """Build the application.

    `repository` is injectable so a test can supply its own adapter without touching global
    state. Phase 2 defaults to the in-memory adapter; Phase 3 (P3-B10) selects PostgreSQL from
    DATABASE_URL and keeps this one for fast unit tests (ADR-005 D-5.1).
    """
    configure_logging()

    app = FastAPI(
        title="Kubernetes Runtime Topology Visualizer",
        description=DESCRIPTION,
        version="0.1.0",
        openapi_url="/api/v1/openapi.json",
    )

    # Restricted to configured origins — never "*" (ADR-004 D-4.6, fixing prototype defect B2).
    app.add_middleware(
        CORSMiddleware,
        allow_origins=settings.cors_allowed_origins,
        allow_credentials=False,
        allow_methods=["GET", "POST"],
        allow_headers=["Content-Type"],
    )

    # Outermost so every request gets an id before anything else can log.
    app.add_middleware(RequestContextMiddleware)

    app.state.repository = repository or InMemoryRepository(cluster_id=settings.cluster_id)
    app.state.clock = utc_now

    app.include_router(health_router)
    app.include_router(api_router)
    return app


app = create_app()
