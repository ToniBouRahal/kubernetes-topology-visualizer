"""Settings parsing — the env-var path specifically.

These exist because a real deployment crash-looped on a bug that every local test missed: the
suite used default values and never exercised parsing from the environment, which is the only
path a container ever takes.
"""

from __future__ import annotations

import pytest

from app.main import _retention_interval_seconds
from app.settings import Settings


def test_cors_origins_accept_a_comma_separated_string(monkeypatch: pytest.MonkeyPatch) -> None:
    """The ConfigMap emits a comma-separated list, not a JSON array.

    pydantic-settings would otherwise run json.loads on the raw value before any validator sees
    it, raising during settings construction so the process never starts. Requiring operators to
    write JSON in a ConfigMap would be the worse trade.
    """
    monkeypatch.setenv("CORS_ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:8080")
    assert Settings().cors_allowed_origins == [
        "http://localhost:5173",
        "http://localhost:8080",
    ]


def test_cors_origins_tolerate_whitespace_and_trailing_commas(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("CORS_ALLOWED_ORIGINS", " http://a , http://b ,")
    assert Settings().cors_allowed_origins == ["http://a", "http://b"]


def test_cors_origins_single_value(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CORS_ALLOWED_ORIGINS", "http://only-one")
    assert Settings().cors_allowed_origins == ["http://only-one"]


def test_cors_origins_default_when_unset(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("CORS_ALLOWED_ORIGINS", raising=False)
    assert Settings().cors_allowed_origins == ["http://localhost:5173"]


@pytest.mark.parametrize(
    ("variable", "value", "attribute", "expected"),
    [
        ("CLUSTER_ID", "prod-cluster", "cluster_id", "prod-cluster"),
        ("RETENTION_HOURS", "48", "retention_hours", 48),
        ("GRAPH_MAX_NODES", "250", "graph_max_nodes", 250),
        ("GRAPH_MAX_EDGES", "900", "graph_max_edges", 900),
        (
            "TOPOLOGY_DIFF_CHANGE_THRESHOLD_PERCENT",
            "35.5",
            "topology_diff_change_threshold_percent",
            35.5,
        ),
    ],
)
def test_chart_templated_variables_parse(
    monkeypatch: pytest.MonkeyPatch,
    variable: str,
    value: str,
    attribute: str,
    expected: object,
) -> None:
    """Every variable the Helm ConfigMap sets must survive the env-var path.

    ADR-001 §5.7 fixes these names; the chart templates them; this asserts the backend actually
    reads them rather than silently falling back to a default.
    """
    monkeypatch.setenv(variable, value)
    assert getattr(Settings(), attribute) == expected


class TestRetentionInterval:
    """How often the purge runs, given how long data is kept.

    This exists because the bare fraction it replaced was correct at the value it was written for
    and quietly wrong at every larger one — and wrong in the direction that leaves expired data
    readable while every dashboard reports normal.
    """

    def test_sweeps_hourly_at_the_original_day_long_retention(self) -> None:
        # The behaviour ADR-005 D-5.5 describes, unchanged.
        assert _retention_interval_seconds(24) == 3600

    def test_still_sweeps_hourly_at_a_two_month_retention(self) -> None:
        """The defect the cap fixes.

        A twenty-fourth of 1440 hours is 60 hours. Because the loop sleeps BEFORE its first pass,
        that left a restarted backend serving rows up to two and a half days past the cutoff, with
        no error anywhere — queries simply reached further back than retention claimed.
        """
        assert _retention_interval_seconds(1440) == 3600

    def test_scales_down_with_a_short_retention(self) -> None:
        # A twenty-fourth of an hour. The floor does not bind here and is not supposed to.
        assert _retention_interval_seconds(1) == 150

    def test_the_floor_holds_if_retention_is_ever_shorter_than_an_hour(self) -> None:
        """Defensive, and currently unreachable through the chart.

        values.schema.json sets `retentionHours` minimum 1, and a twenty-fourth of one hour is
        already 150 s, so no valid configuration reaches the floor today. It stays because the
        cost is one comparison and the failure it prevents — a sweep every few seconds against a
        growing table — is a live database issue rather than a wrong number.
        """
        assert _retention_interval_seconds(0) == 60

    @pytest.mark.parametrize("hours", [1, 6, 24, 168, 720, 1440, 8760])
    def test_stays_between_a_minute_and_an_hour_at_every_plausible_retention(
        self, hours: int
    ) -> None:
        assert 60 <= _retention_interval_seconds(hours) <= 3600
