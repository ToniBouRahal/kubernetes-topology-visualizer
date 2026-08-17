"""Graph assembly.

Pure functions over stored aggregates. No I/O, no clock, no framework types beyond the response
models — so every rule here is testable without a repository (ADR-004 D-4.1).
"""

from __future__ import annotations

from datetime import datetime

from app.domain.models import (
    EffectiveFilters,
    GraphEdge,
    GraphNode,
    GraphResponse,
    GraphSummary,
    NodeDependency,
    NodeDetail,
    build_edge_id,
)
from app.domain.window import ResolvedWindow
from app.persistence.protocol import EdgeAggregate, StoredNode

EXTERNAL_NODE_ID = "external:EXTERNAL"


def assemble(
    *,
    edges: list[EdgeAggregate],
    nodes: dict[str, StoredNode],
    window: ResolvedWindow,
    filters: EffectiveFilters,
    generated_at: datetime,
    max_nodes: int,
    max_edges: int,
) -> GraphResponse:
    """Build a graph response from window-scoped aggregates.

    `edges` must already be filtered and sorted by the repository. Truncation happens *after*
    filtering and sorting so it is reproducible: the same query always drops the same edges
    (ADR-004 D-4.4).
    """
    kept, truncation_reason = _truncate(edges, nodes, max_nodes=max_nodes, max_edges=max_edges)

    # Nodes are derived ONLY from the edges that survived. A node with no edge in this window
    # does not appear, even though storage still knows about it — the graph is a statement about
    # the window, not a cumulative inventory (T-4.9).
    referenced: list[str] = []
    seen: set[str] = set()
    for edge in kept:
        for node_id in (edge.source_id, edge.target_id):
            if node_id not in seen:
                seen.add(node_id)
                referenced.append(node_id)

    graph_nodes = [_to_node(nodes[nid]) for nid in referenced if nid in nodes]
    graph_nodes.sort(key=lambda n: n.id)

    graph_edges = [_to_edge(e) for e in kept]

    return GraphResponse(
        generated_at=generated_at,
        window=window.as_model(),
        filters=filters,
        nodes=graph_nodes,
        edges=graph_edges,
        summary=GraphSummary(
            node_count=len(graph_nodes),
            edge_count=len(graph_edges),
            total_connections=sum(e.connection_count for e in kept),
            truncated=truncation_reason is not None,
            truncation_reason=truncation_reason,
        ),
    )


def _truncate(
    edges: list[EdgeAggregate],
    nodes: dict[str, StoredNode],
    *,
    max_nodes: int,
    max_edges: int,
) -> tuple[list[EdgeAggregate], str | None]:
    """Cap the response, reporting exactly what was dropped and why.

    Edges are capped first, then the resulting node set. Both caps trim from the end of an
    already-sorted list, which is what makes truncation deterministic rather than arbitrary.
    """
    reason: str | None = None
    kept = edges

    if len(kept) > max_edges:
        reason = (
            f"showing the first {max_edges} of {len(kept)} edges "
            f"(GRAPH_MAX_EDGES); narrow the window or add a namespace filter"
        )
        kept = kept[:max_edges]

    # Trimming edges is the only lever available for the node cap: a node exists solely because
    # an edge references it.
    distinct: set[str] = set()
    cut = len(kept)
    for index, edge in enumerate(kept):
        distinct.update((edge.source_id, edge.target_id))
        if len(distinct) > max_nodes:
            cut = index
            break

    if cut < len(kept):
        dropped = len(kept) - cut
        node_reason = (
            f"stopped at {max_nodes} nodes (GRAPH_MAX_NODES), dropping {dropped} further edges; "
            "narrow the window or add a namespace filter"
        )
        reason = f"{reason}; {node_reason}" if reason else node_reason
        kept = kept[:cut]

    return kept, reason


def _to_node(stored: StoredNode) -> GraphNode:
    # kind is echoed from storage. The external node is the one entry whose wire kind ("Pod")
    # differs from how the UI presents it, which is why the UI keys on the id, not the kind.
    kind = "External" if stored.id == EXTERNAL_NODE_ID else stored.kind
    return GraphNode(
        id=stored.id,
        kind=kind,  # type: ignore[arg-type]
        namespace=stored.namespace,
        name=stored.name,
        label=stored.label,
        first_seen=stored.first_seen,
        last_seen=stored.last_seen,
        attributes=stored.attributes,
    )


def _to_edge(aggregate: EdgeAggregate) -> GraphEdge:
    return GraphEdge(
        id=build_edge_id(
            aggregate.source_id,
            aggregate.target_id,
            aggregate.protocol,
            aggregate.destination_port,
        ),
        source_id=aggregate.source_id,
        target_id=aggregate.target_id,
        protocol=aggregate.protocol,  # type: ignore[arg-type]
        destination_port=aggregate.destination_port,
        connection_count=aggregate.connection_count,
        bytes_sent=aggregate.bytes_sent,
        bytes_received=aggregate.bytes_received,
        first_seen=aggregate.first_seen,
        last_seen=aggregate.last_seen,
    )


def node_detail(
    *,
    node_id: str,
    edges: list[EdgeAggregate],
    nodes: dict[str, StoredNode],
    window: ResolvedWindow,
) -> NodeDetail | None:
    """Assemble one node's incoming and outgoing dependencies within the window."""
    stored = nodes.get(node_id)
    if stored is None:
        return None

    incoming: list[NodeDependency] = []
    outgoing: list[NodeDependency] = []

    for edge in edges:
        if edge.source_id == node_id:
            peer = nodes.get(edge.target_id)
            outgoing.append(_dependency(edge, edge.target_id, peer))
        elif edge.target_id == node_id:
            peer = nodes.get(edge.source_id)
            incoming.append(_dependency(edge, edge.source_id, peer))

    incoming.sort(key=lambda d: (d.node_id, d.destination_port))
    outgoing.sort(key=lambda d: (d.node_id, d.destination_port))

    return NodeDetail(
        node=_to_node(stored),
        window=window.as_model(),
        incoming=incoming,
        outgoing=outgoing,
    )


def _dependency(edge: EdgeAggregate, peer_id: str, peer: StoredNode | None) -> NodeDependency:
    bytes_total: int | None = None
    if edge.bytes_sent is not None or edge.bytes_received is not None:
        bytes_total = (edge.bytes_sent or 0) + (edge.bytes_received or 0)

    return NodeDependency(
        node_id=peer_id,
        # Fall back to the id only when storage has no record — never by parsing it.
        label=peer.label if peer else peer_id,
        protocol=edge.protocol,  # type: ignore[arg-type]
        destination_port=edge.destination_port,
        connection_count=edge.connection_count,
        bytes_total=bytes_total,
        first_seen=edge.first_seen,
        last_seen=edge.last_seen,
    )
