"""Topology comparison.

A pure function of two edge collections and a threshold. No I/O, no clock — every rule below is
testable in isolation, which matters because this is the densest boundary logic in the project
(ADR-004 D-4.5, ADR-003 D-3.7).
"""

from __future__ import annotations

from datetime import datetime

from app.domain.models import DiffEdge, DiffResponse, DiffSummary, EffectiveFilters, build_edge_id
from app.domain.window import ResolvedWindow
from app.persistence.protocol import EdgeAggregate

EdgeKey = tuple[str, str, str, int]


def _bytes_total(edge: EdgeAggregate | None) -> int | None:
    """None means "not measured". Zero would mean "measured, and it was zero"."""
    if edge is None or (edge.bytes_sent is None and edge.bytes_received is None):
        return None
    return (edge.bytes_sent or 0) + (edge.bytes_received or 0)


def _percent_delta(baseline: int, current: int) -> float | None:
    """Percentage change, or None when it is undefined.

    Undefined against a zero baseline: any increase from nothing is infinite, and emitting
    Infinity or NaN would both break JSON serialisation and render as garbage. The caller states
    the reason in words instead (contracts/ids.md §10).
    """
    if baseline == 0:
        return None
    return round(((current - baseline) / baseline) * 100, 2)


def compare(
    *,
    baseline_edges: list[EdgeAggregate],
    current_edges: list[EdgeAggregate],
    baseline_window: ResolvedWindow,
    current_window: ResolvedWindow,
    threshold_percent: float,
    filters: EffectiveFilters,
    generated_at: datetime,
    include_unchanged: bool,
    max_edges: int,
) -> DiffResponse:
    """Classify every edge across two periods.

    A period with no observation contributes ZERO, not null. That distinction drives the whole
    classification: an edge absent from the baseline is NEW because it was measured as zero
    there, not because the data is missing.
    """
    baseline = {e.key: e for e in baseline_edges}
    current = {e.key: e for e in current_edges}

    results: list[DiffEdge] = []

    # Sorted union: the output order is part of the contract, and iterating a set would make it
    # accidental.
    for key in sorted(baseline.keys() | current.keys()):
        before = baseline.get(key)
        after = current.get(key)

        before_count = before.connection_count if before else 0
        after_count = after.connection_count if after else 0
        delta = after_count - before_count

        before_bytes = _bytes_total(before)
        after_bytes = _bytes_total(after)

        connection_percent = _percent_delta(before_count, after_count)
        bytes_percent = (
            _percent_delta(before_bytes, after_bytes)
            if before_bytes is not None and after_bytes is not None
            else None
        )

        classification, reason = _classify(
            present_before=before is not None,
            present_after=after is not None,
            before_count=before_count,
            after_count=after_count,
            connection_percent=connection_percent,
            bytes_percent=bytes_percent,
            threshold=threshold_percent,
        )

        if classification == "UNCHANGED" and not include_unchanged:
            continue

        source_id, target_id, protocol, port = key
        results.append(
            DiffEdge(
                id=build_edge_id(source_id, target_id, protocol, port),
                source_id=source_id,
                target_id=target_id,
                protocol=protocol,  # type: ignore[arg-type]
                destination_port=port,
                classification=classification,  # type: ignore[arg-type]
                baseline_connection_count=before_count,
                current_connection_count=after_count,
                connection_delta=delta,
                connection_percent_delta=connection_percent,
                baseline_bytes_total=before_bytes,
                current_bytes_total=after_bytes,
                bytes_percent_delta=bytes_percent,
                reason=reason,
            )
        )

    truncated = len(results) > max_edges
    if truncated:
        results = results[:max_edges]

    return DiffResponse(
        generated_at=generated_at,
        baseline=baseline_window.as_model(),
        current=current_window.as_model(),
        threshold_percent=threshold_percent,
        filters=filters,
        include_unchanged=include_unchanged,
        edges=results,
        summary=DiffSummary(
            new_count=sum(1 for e in results if e.classification == "NEW"),
            removed_count=sum(1 for e in results if e.classification == "REMOVED"),
            changed_count=sum(1 for e in results if e.classification == "CHANGED"),
            unchanged_count=sum(1 for e in results if e.classification == "UNCHANGED"),
            truncated=truncated,
        ),
    )


def _classify(
    *,
    present_before: bool,
    present_after: bool,
    before_count: int,
    after_count: int,
    connection_percent: float | None,
    bytes_percent: float | None,
    threshold: float,
) -> tuple[str, str]:
    """Classify one edge and explain the classification.

    The reason is not decoration: Phase 3 requires a threshold-crossing change to appear "with its
    calculation visible", so the comparison performed and the threshold applied are stated in the
    response rather than left for the reader to infer.

    Order matters. NEW and REMOVED are decided by PRESENCE, before any magnitude comparison —
    an edge that appeared is new even if its count happens to match something.
    """
    if present_after and not present_before:
        return (
            "NEW",
            f"absent from the baseline period and observed {after_count} time(s) in the current "
            "period; a period with no observation counts as zero, not missing data",
        )

    if present_before and not present_after:
        return (
            "REMOVED",
            f"observed {before_count} time(s) in the baseline period and absent from the current "
            "period",
        )

    # Present in both: magnitude decides. Byte volume is preferred when BOTH periods measured it,
    # because it is the better traffic proxy — but connection count is what always exists.
    if bytes_percent is not None and abs(bytes_percent) >= threshold:
        return (
            "CHANGED",
            f"byte volume changed by {bytes_percent:+.2f}%, at or beyond the {threshold:g}% "
            "threshold",
        )

    if connection_percent is not None and abs(connection_percent) >= threshold:
        return (
            "CHANGED",
            f"connection count changed from {before_count} to {after_count} "
            f"({connection_percent:+.2f}%), at or beyond the {threshold:g}% threshold",
        )

    if connection_percent is None:
        # Present in both yet a zero baseline: only reachable if a bucket stored a zero count.
        return (
            "UNCHANGED",
            "baseline count is zero, so a percentage change is undefined; reported as unchanged "
            "rather than as an infinite increase",
        )

    return (
        "UNCHANGED",
        f"connection count changed from {before_count} to {after_count} "
        f"({connection_percent:+.2f}%), within the {threshold:g}% threshold",
    )
