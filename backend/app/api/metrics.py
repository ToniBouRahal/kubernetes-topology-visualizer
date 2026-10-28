"""Prometheus-format metrics.

Hand-rolled rather than pulling in a client library: the required set is small and fixed
(ADR-001 §6), and this keeps the dependency surface of a privileged-adjacent service minimal.

The point of these counters is that silent data loss becomes diagnosable. A rejected batch and an
absence of traffic look identical in a graph; they must not look identical here.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field

ALERT_OUTCOMES = ("sent", "failed", "dropped")


@dataclass
class Metrics:
    """Process-lifetime counters. Reset on restart, which is expected for a counter."""

    batches_accepted: int = 0
    batches_deduplicated: int = 0
    batches_rejected: int = 0
    graph_queries: int = 0
    diff_queries: int = 0
    retention_deletions: int = 0
    # ADR-016 D-16.5: attempts (successful + failed) whose target is a canary, counted at ingest.
    canary_connections: int = 0
    # ADR-016 D-16.3: what the evaluator found on its last pass, per type. Seeded by the app with
    # every type at zero, so an alert expression never meets a missing series.
    active_anomalies: dict[str, int] = field(default_factory=dict)
    # ADR-018 D-18.7: alert outcomes per (channel, outcome). Seeded with every configured
    # channel's outcomes at zero, for the same reason.
    alerts: dict[tuple[str, str], int] = field(default_factory=dict)

    # Query durations, kept as a running sum and count so an average is derivable without
    # carrying a histogram implementation.
    graph_query_seconds_total: float = 0.0

    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    def record_ingest(self, *, deduplicated: bool) -> None:
        with self._lock:
            if deduplicated:
                self.batches_deduplicated += 1
            else:
                self.batches_accepted += 1

    def record_rejection(self) -> None:
        with self._lock:
            self.batches_rejected += 1

    def record_graph_query(self, seconds: float) -> None:
        with self._lock:
            self.graph_queries += 1
            self.graph_query_seconds_total += seconds

    def record_diff_query(self) -> None:
        with self._lock:
            self.diff_queries += 1

    def record_canary_contact(self, attempts: int) -> None:
        with self._lock:
            self.canary_connections += attempts

    def set_active_anomalies(self, counts: dict[str, int]) -> None:
        with self._lock:
            self.active_anomalies = dict(counts)

    def seed_alert_channels(self, channels: list[str]) -> None:
        with self._lock:
            for channel in channels:
                for outcome in ALERT_OUTCOMES:
                    self.alerts.setdefault((channel, outcome), 0)

    def record_alert(self, channel: str, outcome: str) -> None:
        with self._lock:
            self.alerts[(channel, outcome)] = self.alerts.get((channel, outcome), 0) + 1

    def record_retention(self, deleted: int) -> None:
        with self._lock:
            self.retention_deletions += deleted

    def render(self, *, stored_buckets: int) -> str:
        """Render the Prometheus text exposition format."""
        with self._lock:
            samples = [
                (
                    "topology_backend_batches_accepted_total",
                    "Batches stored",
                    "counter",
                    self.batches_accepted,
                ),
                (
                    "topology_backend_batches_deduplicated_total",
                    "Batches recognised as already ingested (agent retries)",
                    "counter",
                    self.batches_deduplicated,
                ),
                (
                    "topology_backend_batches_rejected_total",
                    "Batches rejected as malformed or unsupported",
                    "counter",
                    self.batches_rejected,
                ),
                (
                    "topology_backend_graph_queries_total",
                    "Graph queries served",
                    "counter",
                    self.graph_queries,
                ),
                (
                    "topology_backend_diff_queries_total",
                    "Diff queries served",
                    "counter",
                    self.diff_queries,
                ),
                (
                    "topology_backend_retention_deletions_total",
                    "Edge buckets removed by retention",
                    "counter",
                    self.retention_deletions,
                ),
                (
                    "topology_backend_graph_query_seconds_total",
                    "Cumulative graph query time",
                    "counter",
                    round(self.graph_query_seconds_total, 6),
                ),
                (
                    "topology_backend_canary_connections_total",
                    "Connection attempts to a canary workload, counted at ingest",
                    "counter",
                    self.canary_connections,
                ),
                (
                    "topology_backend_stored_edge_buckets",
                    "Edge buckets currently stored",
                    "gauge",
                    stored_buckets,
                ),
            ]
            anomalies = sorted(self.active_anomalies.items())
            alerts = sorted(self.alerts.items())

        lines: list[str] = []
        for name, help_text, metric_type, value in samples:
            lines.append(f"# HELP {name} {help_text}")
            lines.append(f"# TYPE {name} {metric_type}")
            lines.append(f"{name} {value}")
        if anomalies:
            name = "topology_backend_active_anomalies"
            lines.append(f"# HELP {name} Anomalies found by the last evaluation, by rule")
            lines.append(f"# TYPE {name} gauge")
            # Type names are a fixed Literal set, never user input, so no label escaping needed.
            lines.extend(f'{name}{{type="{kind.lower()}"}} {count}' for kind, count in anomalies)
        if alerts:
            name = "topology_backend_alerts_total"
            lines.append(f"# HELP {name} Alert messages by channel and outcome")
            lines.append(f"# TYPE {name} counter")
            # Channel and outcome names are fixed in code, never user input.
            lines.extend(
                f'{name}{{channel="{channel}",outcome="{outcome}"}} {count}'
                for (channel, outcome), count in alerts
            )
        return "\n".join(lines) + "\n"
