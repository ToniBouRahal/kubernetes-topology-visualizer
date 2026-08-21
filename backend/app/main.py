"""Application factory.

Layering is fixed by ADR-004 D-4.1: api / domain / persistence, with no import from domain back
into api or persistence.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
from contextlib import asynccontextmanager
from datetime import timedelta

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from app.api import errors
from app.api.dependencies import utc_now
from app.api.logging import RequestContextMiddleware, configure_logging
from app.api.metrics import Metrics
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


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Select storage, migrate, and run retention for the life of the process.

    Migrations run BEFORE the app serves traffic, and readiness stays false until they succeed:
    an API answering against a half-built schema produces errors that look like data problems
    (ADR-005 D-5.6).
    """
    log = logging.getLogger("app.lifespan")
    owned: object | None = None

    # An explicitly injected repository is never replaced — that is a test's choice.
    if not getattr(app.state, "injected_repository", False):
        if settings.database_url:
            from app.persistence.postgres import PostgresRepository, sanitise_dsn

            log.info("connecting to storage", extra={"dsn": sanitise_dsn(settings.database_url)})
            postgres = await PostgresRepository.connect(
                settings.database_url, cluster_id=settings.cluster_id
            )
            applied = await postgres.migrate()
            log.info("migrations complete", extra={"applied": applied})
            app.state.repository = postgres
            owned = postgres
        else:
            # Phase 2 behaviour, retained for tests and local runs. Not durable, and
            # /health/ready says so rather than implying otherwise.
            log.warning("DATABASE_URL is unset; using the in-memory adapter (not durable)")
            app.state.repository = InMemoryRepository(cluster_id=settings.cluster_id)

    retention = asyncio.create_task(_retention_loop(app))
    try:
        yield
    finally:
        retention.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await retention
        if owned is not None:
            await owned.close()


async def _retention_loop(app: FastAPI) -> None:
    """Delete expired buckets periodically (ADR-005 D-5.5)."""
    log = logging.getLogger("app.retention")
    interval = max(60, settings.retention_hours * 3600 // 24)

    while True:
        await asyncio.sleep(interval)
        try:
            cutoff = app.state.clock() - timedelta(hours=settings.retention_hours)
            stats = await app.state.repository.purge_expired(cutoff)
            if stats.edge_buckets_deleted or stats.nodes_deleted:
                app.state.metrics.record_retention(stats.edge_buckets_deleted)
                log.info(
                    "retention pass complete",
                    extra={
                        "buckets_deleted": stats.edge_buckets_deleted,
                        "nodes_deleted": stats.nodes_deleted,
                        "cutoff": cutoff.isoformat(),
                    },
                )
        except asyncio.CancelledError:
            raise
        except Exception:
            # Retention failing must never take the API down; the next pass retries.
            log.exception("retention pass failed")


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
        lifespan=lifespan,
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

    # A usable adapter from the moment the app object exists, so a TestClient constructed
    # without entering the lifespan still works. When DATABASE_URL is set, lifespan REPLACES
    # this with PostgreSQL before any traffic is served.
    app.state.repository = repository or InMemoryRepository(cluster_id=settings.cluster_id)
    app.state.injected_repository = repository is not None
    app.state.clock = utc_now
    app.state.metrics = Metrics()

    # Registered after the routers exist so handler lookup covers every route.
    errors.register(app)

    app.include_router(health_router)
    app.include_router(api_router)
    return app


app = create_app()
