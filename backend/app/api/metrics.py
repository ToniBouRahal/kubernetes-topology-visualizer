"""Prometheus-format metrics.

Hand-rolled rather than pulling in a client library: the required set is small and fixed
(ADR-001 §6), and this keeps the dependency surface of a privileged-adjacent service minimal.

The point of these counters is that silent data loss becomes diagnosable. A rejected batch and an
absence of traffic look identical in a graph; they must not look identical here.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field


@dataclass
class Metrics:
    """Process-lifetime counters. Reset on restart, which is expected for a counter."""

    batches_accepted: int = 0
    batches_deduplicated: int = 0
    batches_rejected: int = 0
    graph_queries: int = 0
    diff_queries: int = 0
    retention_deletions: int = 0

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
                    "topology_backend_stored_edge_buckets",
                    "Edge buckets currently stored",
                    "gauge",
                    stored_buckets,
                ),
            ]

        lines: list[str] = []
        for name, help_text, metric_type, value in samples:
            lines.append(f"# HELP {name} {help_text}")
            lines.append(f"# TYPE {name} {metric_type}")
            lines.append(f"{name} {value}")
        return "\n".join(lines) + "\n"
