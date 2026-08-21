"""Diff classification — T-4.6.

Pure-function tests over the classifier. Phase 3 gates on diff being deterministic and on a
threshold-crossing change appearing "with its calculation visible", so these assert the REASON
text as well as the label.
"""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

from app.domain.diff import compare
from app.domain.models import EffectiveFilters
from app.domain.window import ResolvedWindow
from app.persistence.protocol import EdgeAggregate

BASELINE = ResolvedWindow(
    start=datetime(2026, 8, 10, 10, tzinfo=UTC), end=datetime(2026, 8, 10, 11, tzinfo=UTC)
)
CURRENT = ResolvedWindow(
    start=datetime(2026, 8, 10, 11, tzinfo=UTC), end=datetime(2026, 8, 10, 12, tzinfo=UTC)
)
NOW = datetime(2026, 8, 10, 12, tzinfo=UTC)


def edge(source: str, target: str, count: int, port: int = 8080, **kwargs) -> EdgeAggregate:
    return EdgeAggregate(
        source_id=f"k8s:c:demo:Deployment:{source}",
        target_id=f"k8s:c:demo:Service:{target}",
        protocol="TCP",
        destination_port=port,
        connection_count=count,
        first_seen=datetime(2026, 8, 10, 10, 30, tzinfo=UTC),
        last_seen=datetime(2026, 8, 10, 11, 30, tzinfo=UTC),
        **kwargs,
    )


def run(baseline, current, *, threshold=20.0, include_unchanged=False, max_edges=2000):
    return compare(
        baseline_edges=baseline,
        current_edges=current,
        baseline_window=BASELINE,
        current_window=CURRENT,
        threshold_percent=threshold,
        filters=EffectiveFilters(),
        generated_at=NOW,
        include_unchanged=include_unchanged,
        max_edges=max_edges,
    )


# ── Classification ──────────────────────────────────────────────────────────────────────────


def test_an_edge_only_in_the_current_period_is_new():
    result = run([], [edge("a", "b", 10)])

    assert [e.classification for e in result.edges] == ["NEW"]
    assert result.edges[0].baseline_connection_count == 0, (
        "a period with no observation counts as ZERO, not as missing data"
    )
    assert result.summary.new_count == 1


def test_an_edge_only_in_the_baseline_is_removed():
    result = run([edge("a", "b", 10)], [])

    assert [e.classification for e in result.edges] == ["REMOVED"]
    assert result.edges[0].current_connection_count == 0
    assert result.summary.removed_count == 1


def test_presence_decides_before_magnitude():
    """An edge that appeared is NEW even when its count matches an unrelated edge's."""
    result = run([edge("a", "b", 10)], [edge("a", "b", 10), edge("c", "d", 10)])
    by_source = {e.source_id.split(":")[-1]: e.classification for e in result.edges}
    assert by_source["c"] == "NEW"


@pytest.mark.parametrize(
    ("before", "after", "expected"),
    [
        (100, 130, "CHANGED"),  # +30%, over
        (100, 70, "CHANGED"),  # -30%, over — a DROP is a change too
        (100, 120, "CHANGED"),  # exactly +20%, at the threshold
        (100, 80, "CHANGED"),  # exactly -20%
        (100, 119, "UNCHANGED"),
        (100, 81, "UNCHANGED"),
        (100, 100, "UNCHANGED"),
    ],
)
def test_threshold_boundary_is_inclusive(before, after, expected):
    """The comparison is >= on the absolute percentage, and the boundary is tested explicitly
    because an off-by-one here silently reclassifies every marginal edge."""
    result = run([edge("a", "b", before)], [edge("a", "b", after)], include_unchanged=True)
    assert result.edges[0].classification == expected


def test_a_decrease_is_reported_with_a_negative_delta():
    result = run([edge("a", "b", 100)], [edge("a", "b", 50)])
    assert result.edges[0].connection_delta == -50
    assert result.edges[0].connection_percent_delta == -50.0


# ── Undefined percentages ───────────────────────────────────────────────────────────────────


def test_a_new_edge_has_no_percentage_and_says_why():
    """Zero baseline makes a percentage undefined. Infinity or NaN would break JSON and render
    as garbage (contracts/ids.md §10)."""
    result = run([], [edge("a", "b", 10)])
    assert result.edges[0].connection_percent_delta is None
    assert "zero" in result.edges[0].reason.lower()


def test_percentages_are_finite_and_serialisable():
    result = run([edge("a", "b", 1)], [edge("a", "b", 1_000_000)])
    value = result.edges[0].connection_percent_delta
    assert value is not None
    assert value == value and abs(value) != float("inf")  # not NaN, not Infinity


# ── Reasons carry the calculation ───────────────────────────────────────────────────────────


def test_a_changed_edge_states_its_calculation():
    """Phase 3 requires the calculation to be visible, not inferred."""
    result = run([edge("a", "b", 100)], [edge("a", "b", 150)])
    reason = result.edges[0].reason
    assert "100" in reason and "150" in reason
    assert "+50.00%" in reason
    assert "20%" in reason, "the applied threshold must appear in the explanation"


def test_every_edge_carries_a_reason():
    result = run(
        [edge("a", "b", 10), edge("c", "d", 5)], [edge("a", "b", 99)], include_unchanged=True
    )
    assert all(e.reason for e in result.edges)


# ── Bytes ───────────────────────────────────────────────────────────────────────────────────


def test_byte_volume_is_preferred_when_both_periods_measured_it():
    result = run(
        [edge("a", "b", 100, bytes_sent=1000, bytes_received=0)],
        # Connections barely moved, but bytes tripled.
        [edge("a", "b", 101, bytes_sent=3000, bytes_received=0)],
    )
    assert result.edges[0].classification == "CHANGED"
    assert "byte volume" in result.edges[0].reason


def test_unmeasured_bytes_fall_back_to_connection_count():
    result = run([edge("a", "b", 100)], [edge("a", "b", 200)])
    assert result.edges[0].classification == "CHANGED"
    assert "connection count" in result.edges[0].reason
    assert result.edges[0].baseline_bytes_total is None


def test_bytes_measured_in_only_one_period_are_not_compared():
    """Comparing a measured period against an unmeasured one would invent a change."""
    result = run(
        [edge("a", "b", 100)],
        [edge("a", "b", 100, bytes_sent=5000)],
        include_unchanged=True,
    )
    assert result.edges[0].bytes_percent_delta is None
    assert result.edges[0].classification == "UNCHANGED"


# ── Determinism and shape ───────────────────────────────────────────────────────────────────


def test_output_is_ordered_by_the_edge_key():
    baseline = [edge("z", "a", 10, port=9000), edge("a", "z", 10, port=8080)]
    result = run(baseline, [], include_unchanged=True)
    keys = [(e.source_id, e.target_id, e.protocol, e.destination_port) for e in result.edges]
    assert keys == sorted(keys)


def test_repeated_comparisons_are_identical():
    baseline = [edge("a", "b", 100), edge("c", "d", 50)]
    current = [edge("a", "b", 200)]
    first = run(baseline, current)
    for _ in range(5):
        again = run(baseline, current)
        assert [e.model_dump() for e in again.edges] == [e.model_dump() for e in first.edges]


def test_unchanged_edges_are_suppressed_by_default():
    result = run([edge("a", "b", 100)], [edge("a", "b", 101)])
    assert result.edges == []
    assert result.summary.unchanged_count == 0

    included = run([edge("a", "b", 100)], [edge("a", "b", 101)], include_unchanged=True)
    assert len(included.edges) == 1
    assert included.summary.unchanged_count == 1


def test_result_is_truncated_with_an_indicator():
    baseline = [edge(f"s{i}", "t", 10, port=1000 + i) for i in range(50)]
    result = run(baseline, [], max_edges=10)
    assert len(result.edges) == 10
    assert result.summary.truncated is True


def test_windows_and_threshold_are_echoed():
    result = run([], [edge("a", "b", 1)], threshold=35.0)
    assert result.threshold_percent == 35.0
    assert result.baseline.start == BASELINE.start
    assert result.current.end == CURRENT.end
