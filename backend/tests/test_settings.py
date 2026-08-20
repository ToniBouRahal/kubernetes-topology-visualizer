"""Settings parsing — the env-var path specifically.

These exist because a real deployment crash-looped on a bug that every local test missed: the
suite used default values and never exercised parsing from the environment, which is the only
path a container ever takes.
"""

from __future__ import annotations

import pytest

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
