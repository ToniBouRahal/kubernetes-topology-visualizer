"""Startup connection retry (PostgresRepository.connect).

On a fresh install the backend starts alongside its database, and the database's Service name
does not resolve until that pod is Ready. The first attempt failed with socket.gaierror and the pod
crash-looped four times before settling. These pin the retry that absorbs that race, and that it
does not hide a failure that will never clear.
"""

from __future__ import annotations

import asyncio
import socket

import asyncpg
import pytest

from app.persistence import postgres
from app.persistence.postgres import PostgresRepository

DSN = "postgresql://topology:s3cret@db.example:5432/topology"


class FakePool:
    pass


def fake_create_pool(
    monkeypatch: pytest.MonkeyPatch,
    outcomes: list[BaseException | None],
    *,
    real_sleep: bool = False,
):
    calls: list[int] = []

    async def create_pool(*_args: object, **_kwargs: object) -> FakePool:
        calls.append(1)
        outcome = outcomes.pop(0) if outcomes else None
        if outcome is not None:
            raise outcome
        return FakePool()

    async def no_sleep(_seconds: float) -> None:
        return None

    monkeypatch.setattr(postgres.asyncpg, "create_pool", create_pool)
    if not real_sleep:
        monkeypatch.setattr(postgres.asyncio, "sleep", no_sleep)
    return calls


async def test_retries_until_the_database_name_resolves(monkeypatch: pytest.MonkeyPatch) -> None:
    calls = fake_create_pool(
        monkeypatch,
        [socket.gaierror(-2, "Name or service not known"), ConnectionRefusedError(), None],
    )
    repo = await PostgresRepository.connect(DSN, "c1", retry_for=30.0)
    assert isinstance(repo._pool, FakePool)
    assert len(calls) == 3


async def test_retries_while_postgres_reports_it_is_starting(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls = fake_create_pool(
        monkeypatch, [asyncpg.CannotConnectNowError("the database system is starting up"), None]
    )
    await PostgresRepository.connect(DSN, "c1", retry_for=30.0)
    assert len(calls) == 2


async def test_does_not_retry_a_failure_that_will_not_clear(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls = fake_create_pool(monkeypatch, [asyncpg.InvalidPasswordError("bad password")])
    with pytest.raises(ConnectionError, match="InvalidPasswordError"):
        await PostgresRepository.connect(DSN, "c1", retry_for=30.0)
    assert len(calls) == 1


async def test_gives_up_without_leaking_the_password(monkeypatch: pytest.MonkeyPatch) -> None:
    # retry_for=0 is the default: one attempt, exactly the previous behaviour.
    calls = fake_create_pool(monkeypatch, [socket.gaierror(-2, "Name or service not known")] * 5)
    with pytest.raises(ConnectionError) as info:
        await PostgresRepository.connect(DSN, "c1")
    assert len(calls) == 1
    assert "s3cret" not in str(info.value)
    assert "topology:***@db.example" in str(info.value)
    assert "gaierror" in str(info.value)


async def test_stops_retrying_at_the_deadline(monkeypatch: pytest.MonkeyPatch) -> None:
    # Real time with a short budget, so the deadline is what ends the loop.
    calls = fake_create_pool(monkeypatch, [ConnectionRefusedError()] * 1000, real_sleep=True)
    with pytest.raises(ConnectionError, match="ConnectionRefusedError"):
        await PostgresRepository.connect(DSN, "c1", retry_for=0.6)
    assert 2 <= len(calls) <= 3  # 0.5s, then the 0.1s remainder


async def test_a_crumb_of_time_left_is_the_deadline(monkeypatch: pytest.MonkeyPatch) -> None:
    """A clock that has not moved must not turn the last instant into a spin of retries.

    Deterministic version of what CI hit once: with uvloop the loop clock advanced only between
    iterations, the remainder sat at 7e-15 s, and 27 attempts ran back to back. Here the clock is
    frozen a microsecond short of the deadline, so a `remaining <= 0` check would retry forever; the
    minimum wait ends it at the first failure.
    """
    calls = fake_create_pool(monkeypatch, [ConnectionRefusedError()] * 1000)
    loop = asyncio.get_running_loop()
    start = loop.time()
    ticks = iter([start])  # the deadline is set from the first read

    def frozen() -> float:
        # A microsecond: far above float spacing at loop-clock magnitudes (~1e-12 at 1e4 s, which
        # would round the crumb away to exactly 0), far below the minimum wait.
        return next(ticks, start + 0.5 - 1e-6)

    monkeypatch.setattr(loop, "time", frozen)
    with pytest.raises(ConnectionError, match="ConnectionRefusedError"):
        await PostgresRepository.connect(DSN, "c1", retry_for=0.5)
    assert len(calls) == 1
