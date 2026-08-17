"""Dependency wiring.

Storage and the clock are injected rather than reached for, so tests can substitute both
(ADR-004 D-4.2). A handler that calls `datetime.now()` directly makes bucket-boundary tests
impossible, which is prototype defect B12.
"""

from __future__ import annotations

from collections.abc import Callable
from datetime import UTC, datetime
from typing import Annotated

from fastapi import Depends, Request

from app.persistence.protocol import TopologyRepository

Clock = Callable[[], datetime]


def utc_now() -> datetime:
    return datetime.now(UTC)


def get_repository(request: Request) -> TopologyRepository:
    """The repository selected at startup.

    Held on app.state rather than a module global so a test can build an app with its own
    adapter without disturbing any other test.
    """
    repository = getattr(request.app.state, "repository", None)
    if repository is None:  # pragma: no cover - configuration error, not a runtime path
        raise RuntimeError(
            "no repository configured; the app factory must set app.state.repository"
        )
    return repository


def get_clock(request: Request) -> Clock:
    return getattr(request.app.state, "clock", utc_now)


def get_request_id(request: Request) -> str:
    """Correlates a response with its log lines (ADR-004 D-4.6)."""
    return getattr(request.state, "request_id", "")


RepositoryDep = Annotated[TopologyRepository, Depends(get_repository)]
ClockDep = Annotated[Clock, Depends(get_clock)]
RequestIdDep = Annotated[str, Depends(get_request_id)]
