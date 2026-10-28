"""What an alert says, independent of where it is sent (ADR-018 D-18.4, D-18.6).

A message is plain text in parts — title, lines, footer, link — and each channel decides how to
render those parts safely. Nothing here escapes anything: that is the channel's job, because what
counts as markup differs between all three.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime

from app.domain.models import Anomaly, AnomalySeverity, AnomalyType, GraphNode

# Read by people who may not know what an "edge" is (D-18.6).
TITLES: dict[AnomalyType, str] = {
    "CANARY_CONTACT": "Decoy touched",
    "NEW_EXTERNAL_EGRESS": "New connection out of the cluster",
    "FAILURE_SPIKE": "Connections failing",
    "NEW_EDGE": "New connection",
    "SLOW_SETUP": "Connections slower to set up",
}

SEVERITY_RANK: dict[AnomalySeverity, int] = {"warning": 0, "critical": 1}

# One evaluator pass sends one message; past this many findings the rest are counted, not listed.
MAX_LISTED = 5


@dataclass(frozen=True, slots=True)
class Message:
    severity: AnomalySeverity
    title: str
    lines: tuple[str, ...]
    footer: str
    link: str = ""

    def plain_text(self) -> str:
        parts = [self.title, "", *self.lines, "", self.footer]
        if self.link:
            parts.append(self.link)
        return "\n".join(parts)


def describe(namespace: str | None, kind: str, name: str) -> str:
    """`demo/intruder (Job)`; the external node has no namespace and reads as itself."""
    if namespace is None:
        return name
    return f"{namespace}/{name} ({kind})"


def _footer(cluster_id: str, at: datetime) -> str:
    return f"Cluster {cluster_id} · {at.strftime('%Y-%m-%d %H:%M:%S')} UTC"


def canary_contact(
    *,
    source: str,
    canary: str,
    destination_port: int,
    attempts: int,
    cluster_id: str,
    at: datetime,
    link: str = "",
) -> Message:
    """Sent from ingest, the moment a decoy is touched (D-18.3)."""
    noun = "attempt" if attempts == 1 else "attempts"
    return Message(
        severity="critical",
        title=f"{TITLES['CANARY_CONTACT']}: {canary}",
        lines=(
            f"{source} connected to it on TCP port {destination_port} ({attempts} {noun}).",
            "Nothing legitimate calls this workload, so any connection to it is worth a look.",
        ),
        footer=_footer(cluster_id, at),
        link=link,
    )


def anomalies(
    found: list[Anomaly],
    nodes: dict[str, GraphNode],
    *,
    cluster_id: str,
    at: datetime,
    link: str = "",
) -> Message:
    """One message for everything one evaluator pass sends (D-18.4). `found` is non-empty."""

    def node(node_id: str) -> str:
        n = nodes.get(node_id)
        return describe(n.namespace, n.kind, n.name) if n else node_id

    types = {a.type for a in found}
    if len(found) == 1:
        title = TITLES[found[0].type]
    elif len(types) == 1:
        title = f"{TITLES[found[0].type]} ({len(found)})"
    else:
        title = f"{len(found)} anomalies"

    lines = [
        f"{TITLES[a.type]}: {node(a.source_id)} → {node(a.target_id)} on TCP port "
        f"{a.destination_port}. {a.reason}"
        for a in found[:MAX_LISTED]
    ]
    if len(found) > MAX_LISTED:
        lines.append(f"…and {len(found) - MAX_LISTED} more.")

    severity: AnomalySeverity = max((a.severity for a in found), key=lambda s: SEVERITY_RANK[s])
    return Message(
        severity=severity,
        title=title,
        lines=tuple(lines),
        footer=_footer(cluster_id, at),
        link=link,
    )


def working_message(*, cluster_id: str, at: datetime, link: str = "") -> Message:
    """`python -m app.alerts test` (D-18.8)."""
    return Message(
        severity="warning",
        title="Alerts are working",
        lines=("This is a test message from the topology visualizer. No anomaly was found.",),
        footer=_footer(cluster_id, at),
        link=link,
    )
