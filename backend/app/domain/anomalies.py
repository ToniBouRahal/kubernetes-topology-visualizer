"""Rule-based anomalies and canary matching (ADR-016).

Pure functions over stored aggregates, like graph.py and diff.py: no I/O, no clock. Every rule is
a threshold comparison that its reason string states in full, because a finding nobody can explain
is not evidence (ADR-016 §4).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime

from app.domain.models import (
    EXTERNAL_NODE_ID,
    Anomaly,
    AnomalyResponse,
    AnomalySeverity,
    AnomalySummary,
    AnomalyType,
    EffectiveFilters,
    GraphNode,
    build_edge_id,
)
from app.domain.window import ResolvedWindow
from app.persistence.protocol import EdgeAggregate

ANOMALY_TYPES: tuple[AnomalyType, ...] = (
    "CANARY_CONTACT",
    "NEW_EXTERNAL_EGRESS",
    "NEW_EDGE",
    "FAILURE_SPIKE",
    "SLOW_SETUP",
)

SEVERITY: dict[AnomalyType, AnomalySeverity] = {
    "CANARY_CONTACT": "critical",
    "NEW_EXTERNAL_EGRESS": "critical",
    "NEW_EDGE": "warning",
    "FAILURE_SPIKE": "critical",
    "SLOW_SETUP": "warning",
}

# A slowdown smaller than this is scheduler noise however large the ratio: 30 µs → 100 µs is
# "3.3× slower" and means nothing.
SLOW_SETUP_FLOOR_US = 1000.0


# ── Canaries ────────────────────────────────────────────────────────────────────────────────


@dataclass(frozen=True, slots=True)
class CanarySet:
    """Selectors from CANARY_WORKLOADS: `namespace/name` or `namespace/Kind/name` (D-16.4).

    Matched against a node's fields. The id is never parsed (contracts/ids.md §2).
    """

    selectors: frozenset[tuple[str, str | None, str]] = frozenset()

    @classmethod
    def parse(cls, entries: list[str]) -> CanarySet:
        parsed: set[tuple[str, str | None, str]] = set()
        for entry in entries:
            parts = entry.split("/")
            if len(parts) == 2 and all(parts):
                parsed.add((parts[0], None, parts[1]))
            elif len(parts) == 3 and all(parts):
                parsed.add((parts[0], parts[1], parts[2]))
            else:
                raise ValueError(f"canary {entry!r}: want namespace/name or namespace/Kind/name")
        return cls(frozenset(parsed))

    def matches(self, namespace: str | None, kind: str, name: str) -> bool:
        if namespace is None or not self.selectors:
            return False
        return (namespace, None, name) in self.selectors or (
            namespace,
            kind,
            name,
        ) in self.selectors


NO_CANARIES = CanarySet()


# ── Rules ───────────────────────────────────────────────────────────────────────────────────


@dataclass(frozen=True, slots=True)
class Thresholds:
    min_attempts: int = 10
    failure_ratio: float = 0.2
    setup_factor: float = 3.0


def _attempts(edge: EdgeAggregate) -> int:
    return edge.connection_count + (edge.failed_connection_count or 0)


def _failure_ratio(edge: EdgeAggregate | None) -> float | None:
    """None when failures were not measured (contracts/ids.md §10: absent is not zero)."""
    if edge is None:
        return 0.0
    if edge.failed_connection_count is None:
        return None
    attempts = _attempts(edge)
    return edge.failed_connection_count / attempts if attempts else 0.0


def _mean_setup(edge: EdgeAggregate | None) -> float | None:
    if edge is None or edge.connect_latency_count == 0:
        return None
    return edge.connect_latency_sum_us / edge.connect_latency_count


def _pct(ratio: float) -> str:
    return f"{ratio * 100:.0f}%"


def _ms(us: float) -> str:
    return f"{us / 1000:.1f} ms"


def detect(
    *,
    current_edges: list[EdgeAggregate],
    baseline_edges: list[EdgeAggregate],
    canary_targets: set[str],
    window: ResolvedWindow,
    baseline_window: ResolvedWindow,
    thresholds: Thresholds,
) -> tuple[list[Anomaly], bool]:
    """Apply every rule to every edge in the window. Returns (anomalies, baseline_observed).

    Output is sorted by edge key, then by the order of ANOMALY_TYPES, so identical inputs give
    identical output (contracts/ids.md §5).
    """
    baseline = {e.key: e for e in baseline_edges if _attempts(e) > 0}
    baseline_observed = bool(baseline)
    hours = (baseline_window.end - baseline_window.start).total_seconds() / 3600
    period = f"the previous {hours:g} h"

    found: list[Anomaly] = []
    for edge in sorted(current_edges, key=lambda e: e.key):
        if _attempts(edge) == 0:
            continue
        before = baseline.get(edge.key)
        fired: list[tuple[AnomalyType, str]] = []

        if edge.target_id in canary_targets:
            fired.append(
                (
                    "CANARY_CONTACT",
                    f"{_attempts(edge)} connection attempt(s) to a canary workload; "
                    "nothing legitimate connects to one",
                )
            )

        if before is None and baseline_observed:
            if edge.target_id == EXTERNAL_NODE_ID:
                fired.append(
                    (
                        "NEW_EXTERNAL_EGRESS",
                        f"connections leave the cluster on port {edge.destination_port}; "
                        f"none on this port from this source in {period}",
                    )
                )
            else:
                fired.append(("NEW_EDGE", f"not observed in {period}"))

        now_ratio = _failure_ratio(edge)
        was_ratio = _failure_ratio(before)
        if (
            now_ratio is not None
            and was_ratio is not None
            and _attempts(edge) >= thresholds.min_attempts
            and now_ratio >= thresholds.failure_ratio
            and now_ratio - was_ratio >= thresholds.failure_ratio
        ):
            fired.append(
                (
                    "FAILURE_SPIKE",
                    f"{_pct(now_ratio)} of {_attempts(edge)} attempts failed, against "
                    f"{_pct(was_ratio)} in {period} (threshold: ≥ {_pct(thresholds.failure_ratio)}"
                    f" and ≥ {_pct(thresholds.failure_ratio)} points higher)",
                )
            )

        now_setup = _mean_setup(edge)
        was_setup = _mean_setup(before)
        if (
            now_setup is not None
            and was_setup is not None
            and before is not None
            and edge.connect_latency_count >= thresholds.min_attempts
            and before.connect_latency_count >= thresholds.min_attempts
            and now_setup >= thresholds.setup_factor * was_setup
            and now_setup - was_setup >= SLOW_SETUP_FLOOR_US
        ):
            fired.append(
                (
                    "SLOW_SETUP",
                    f"mean TCP setup {_ms(now_setup)} against {_ms(was_setup)} in {period} "
                    f"(threshold: ≥ {thresholds.setup_factor:g}× and ≥ "
                    f"{_ms(SLOW_SETUP_FLOOR_US)} slower)",
                )
            )

        edge_id = build_edge_id(
            edge.source_id, edge.target_id, edge.protocol, edge.destination_port
        )
        for anomaly_type, reason in sorted(fired, key=lambda f: ANOMALY_TYPES.index(f[0])):
            found.append(
                Anomaly(
                    id=f"{edge_id}|{anomaly_type}",
                    edge_id=edge_id,
                    type=anomaly_type,
                    severity=SEVERITY[anomaly_type],
                    source_id=edge.source_id,
                    target_id=edge.target_id,
                    protocol=edge.protocol,  # type: ignore[arg-type]
                    destination_port=edge.destination_port,
                    current_connection_count=edge.connection_count,
                    current_failed_connection_count=edge.failed_connection_count,
                    baseline_connection_count=before.connection_count if before else 0,
                    baseline_failed_connection_count=(
                        before.failed_connection_count if before else 0
                    ),
                    current_mean_setup_us=_round(now_setup),
                    baseline_mean_setup_us=_round(was_setup),
                    reason=reason,
                )
            )

    return found, baseline_observed


def _round(value: float | None) -> float | None:
    return None if value is None else round(value, 1)


def respond(
    *,
    anomalies: list[Anomaly],
    baseline_observed: bool,
    nodes: list[GraphNode],
    window: ResolvedWindow,
    baseline_window: ResolvedWindow,
    filters: EffectiveFilters,
    generated_at: datetime,
    max_anomalies: int,
) -> AnomalyResponse:
    """Cap after sorting, so truncation is reproducible (ADR-004 D-4.4)."""
    kept = anomalies[:max_anomalies]
    referenced = {a.source_id for a in kept} | {a.target_id for a in kept}
    return AnomalyResponse(
        generated_at=generated_at,
        window=window.as_model(),
        baseline=baseline_window.as_model(),
        filters=filters,
        anomalies=kept,
        nodes=sorted((n for n in nodes if n.id in referenced), key=lambda n: n.id),
        summary=AnomalySummary(
            critical_count=sum(1 for a in kept if a.severity == "critical"),
            warning_count=sum(1 for a in kept if a.severity == "warning"),
            baseline_observed=baseline_observed,
            truncated=len(anomalies) > len(kept),
        ),
    )
