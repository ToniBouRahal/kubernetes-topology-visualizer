"""Time-window resolution.

Every window in this system is **half-open and lower-inclusive**: `[start, end)`. That single
convention applies to graph queries, diff periods, and retention alike (ADR-005 D-5.4). Phase 3
requires determinism at exact bucket boundaries, which is only achievable if one rule is written
down and tested rather than reinvented per call site.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime, timedelta

from app.domain.models import TimeWindow

# Presets are fixed by ADR-001 §5.3. `1m` is one bucket, not a sliding minute — see the
# resolution-floor limitation in ADR-005 §8.
WINDOW_PRESETS: dict[str, timedelta] = {
    "1m": timedelta(minutes=1),
    "5m": timedelta(minutes=5),
    "15m": timedelta(minutes=15),
    "1h": timedelta(hours=1),
    "6h": timedelta(hours=6),
    "24h": timedelta(hours=24),
}

BUCKET = timedelta(minutes=1)


class WindowError(ValueError):
    """A window the caller asked for cannot be served. Maps to 422."""


@dataclass(frozen=True, slots=True)
class ResolvedWindow:
    start: datetime
    end: datetime

    def as_model(self) -> TimeWindow:
        return TimeWindow(start=self.start, end=self.end)

    def contains(self, moment: datetime) -> bool:
        """Half-open: the start instant is inside, the end instant is not."""
        return self.start <= moment < self.end


def bucket_start(moment: datetime) -> datetime:
    """Truncate to the containing one-minute bucket, in UTC.

    Computed here in the domain layer rather than in SQL so the in-memory and PostgreSQL
    repositories produce identical buckets (ADR-005 D-5.4).
    """
    moment = _as_utc(moment)
    return moment.replace(second=0, microsecond=0)


def resolve(
    *,
    now: datetime,
    window: str | None = None,
    start: datetime | None = None,
    end: datetime | None = None,
    max_span: timedelta,
) -> ResolvedWindow:
    """Resolve a preset or an explicit range into absolute bounds.

    `now` is injected rather than read from the clock so window arithmetic is testable without
    depending on wall-clock timing (ADR-004 D-4.2).
    """
    has_preset = window is not None
    has_range = start is not None or end is not None

    if has_preset and has_range:
        raise WindowError(
            "supply either 'window' or the 'from'/'to' pair, not both — "
            "they would otherwise disagree silently"
        )

    if has_preset:
        span = WINDOW_PRESETS.get(window or "")
        if span is None:
            raise WindowError(
                f"unknown window preset {window!r}; expected one of "
                f"{', '.join(sorted(WINDOW_PRESETS))}"
            )
        resolved_end = _as_utc(now)
        return _validated(resolved_end - span, resolved_end, max_span)

    if start is None or end is None:
        raise WindowError("an explicit range needs both 'from' and 'to'")

    return _validated(_as_utc(start), _as_utc(end), max_span)


def _validated(start: datetime, end: datetime, max_span: timedelta) -> ResolvedWindow:
    if start >= end:
        raise WindowError(
            f"'from' ({start.isoformat()}) must be strictly before 'to' ({end.isoformat()})"
        )

    span = end - start
    if span > max_span:
        raise WindowError(
            f"requested span {span} exceeds the configured maximum {max_span}; "
            "narrow the range or raise MAX_QUERY_SPAN_HOURS"
        )

    return ResolvedWindow(start=start, end=end)


def validate_diff_periods(baseline: ResolvedWindow, current: ResolvedWindow) -> None:
    """Reject comparison periods that cannot produce a meaningful diff.

    Overlapping periods would double-count the shared interval on both sides of the comparison,
    making a `CHANGED` classification meaningless (ADR-003 D-3.6).
    """
    if baseline.start < current.end and current.start < baseline.end:
        raise WindowError(
            "baseline and current periods overlap; the shared interval would be counted "
            "on both sides of the comparison"
        )


def _as_utc(moment: datetime) -> datetime:
    """Normalise to UTC, rejecting naive datetimes.

    A naive datetime is ambiguous, and silently assuming UTC would produce windows that are
    wrong by the caller's offset with no error anywhere (contracts/ids.md §7).
    """
    if moment.tzinfo is None:
        raise WindowError(
            f"{moment.isoformat()} is timezone-naive; "
            "all timestamps must be RFC 3339 with an offset"
        )
    return moment.astimezone(UTC)
