"""End-to-end API contract tests for the in-memory backend — T-4.2 through T-4.13."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Iterator
from copy import deepcopy
from datetime import UTC, datetime
from pathlib import Path
from urllib.parse import quote

import pytest
import uvloop
from fastapi.testclient import TestClient

from app.main import create_app
from app.persistence.memory import InMemoryRepository
from app.settings import settings

# The managed test runner forbids reads from asyncio's selector socketpair. TestClient's AnyIO
# portal therefore needs uvloop's permitted cross-thread wake-up mechanism in this environment.
asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())

REPO_ROOT = Path(__file__).resolve().parents[2]
VALID_BATCH = REPO_ROOT / "contracts" / "examples" / "batch.valid.json"

FROZEN_NOW = datetime(2026, 8, 10, 12, 1, tzinfo=UTC)
QUERY_WINDOW = {
    "from": "2026-08-10T11:59:00Z",
    "to": "2026-08-10T12:01:00Z",
}
SECOND_BATCH_ID = "01J8ZQ9X7K4M2N6P8R3T5V7W9Z"
OUTSIDE_BATCH_ID = "01J8ZQ9X7K4M2N6P8R3T5V7W9X"


def load_batch() -> dict:
    """Load a fresh copy of the shared canonical batch for each mutation."""
    return json.loads(VALID_BATCH.read_text(encoding="utf-8"))


@pytest.fixture
def client() -> Iterator[TestClient]:
    app = create_app()
    app.state.clock = lambda: FROZEN_NOW
    with TestClient(app) as test_client:
        yield test_client


def ingest_valid_batch(client: TestClient, batch: dict | None = None) -> dict:
    response = client.post("/api/v1/ingest/batches", json=batch or load_batch())
    assert response.status_code == 202, (
        "fixture-derived setup batch was not accepted; downstream API assertions would "
        f"otherwise be testing an empty repository: {response.text}"
    )
    return response.json()


def graph(client: TestClient, **extra_params: object) -> dict:
    params: dict[str, object] = {**QUERY_WINDOW, **extra_params}
    response = client.get("/api/v1/graph", params=params)
    assert response.status_code == 200, (
        "graph setup query failed, so its topology invariants cannot be evaluated: "
        f"{response.text}"
    )
    return response.json()


# ── T-4.2: ingestion idempotency ───────────────────────────────────────────────────────────


def test_replaying_a_batch_is_a_noop_but_a_new_batch_id_increases_counts(client):
    batch = load_batch()

    first = client.post("/api/v1/ingest/batches", json=batch)
    assert first.status_code == 202, "a new batch must be accepted asynchronously"
    assert first.json()["edges_accepted"] == 3, "all three fixture edges must be accepted"

    before_replay = graph(client)["summary"]["total_connections"]
    replay = client.post("/api/v1/ingest/batches", json=batch)
    after_replay = graph(client)["summary"]["total_connections"]

    assert replay.status_code == 200, (
        "a repeated batch_id must be acknowledged as already stored, not accepted again"
    )
    assert replay.json()["edges_accepted"] == 0, (
        "an idempotent replay must report zero accepted edges so agents can safely drop it"
    )
    assert after_replay == before_replay, (
        "replaying one batch_id changed total_connections; retries would inflate topology data"
    )

    batch["batch_id"] = SECOND_BATCH_ID
    distinct = client.post("/api/v1/ingest/batches", json=batch)
    after_distinct_batch = graph(client)["summary"]["total_connections"]

    assert distinct.status_code == 202, "a different valid batch_id must be treated as new data"
    assert after_distinct_batch == before_replay * 2, (
        "the same observations under a new batch_id must increase aggregate counts"
    )


# ── T-4.4 / T-4.5: windows ─────────────────────────────────────────────────────────────────


@pytest.mark.parametrize(
    ("preset", "expected_seconds"),
    [("1m", 60), ("5m", 300), ("15m", 900), ("1h", 3600), ("6h", 21600), ("24h", 86400)],
)
def test_every_window_preset_returns_its_exact_frozen_clock_span(
    client, preset, expected_seconds
):
    response = client.get("/api/v1/graph", params={"window": preset})

    assert response.status_code == 200, f"the documented {preset} preset must remain queryable"
    body = response.json()
    start = datetime.fromisoformat(body["window"]["start"])
    end = datetime.fromisoformat(body["window"]["end"])
    assert end == FROZEN_NOW, f"{preset} must resolve against the injected clock"
    assert (end - start).total_seconds() == expected_seconds, (
        f"{preset} resolved to the wrong span; preset labels must not silently change meaning"
    )


def test_preset_and_explicit_window_together_are_rejected(client):
    response = client.get(
        "/api/v1/graph",
        params={"window": "1h", **QUERY_WINDOW},
    )
    assert response.status_code == 422, (
        "supplying both window forms must fail because silently choosing one is ambiguous"
    )


def test_from_equal_to_is_rejected(client):
    response = client.get(
        "/api/v1/graph",
        params={"from": "2026-08-10T12:00:00Z", "to": "2026-08-10T12:00:00Z"},
    )
    assert response.status_code == 422, (
        "from >= to must be rejected because an empty or inverted half-open window is invalid"
    )


def test_window_beyond_configured_maximum_span_is_rejected(client):
    response = client.get(
        "/api/v1/graph",
        params={
            "from": "2026-08-09T11:59:00Z",
            "to": "2026-08-10T12:00:00Z",
        },
    )
    assert response.status_code == 422, (
        f"queries beyond max_query_span_hours={settings.max_query_span_hours} must be bounded"
    )


def test_naive_window_timestamp_is_rejected(client):
    response = client.get(
        "/api/v1/graph",
        params={"from": "2026-08-10T11:59:00", "to": "2026-08-10T12:01:00Z"},
    )
    assert response.status_code == 422, (
        "timezone-naive timestamps must not be silently interpreted in the server timezone"
    )


# ── T-4.7: graph filters ───────────────────────────────────────────────────────────────────


def test_repeatable_namespace_filter_keeps_an_edge_leaving_a_selected_namespace(client):
    batch = load_batch()
    leaving_data = batch["edges"][1]
    leaving_data["source"], leaving_data["target"] = (
        deepcopy(leaving_data["target"]),
        deepcopy(leaving_data["source"]),
    )
    ingest_valid_batch(client, batch)

    response = client.get(
        "/api/v1/graph",
        params=[
            ("from", QUERY_WINDOW["from"]),
            ("to", QUERY_WINDOW["to"]),
            ("namespace", "absent"),
            ("namespace", "data"),
        ],
    )

    assert response.status_code == 200, "repeatable namespace query parameters must be accepted"
    body = response.json()
    assert body["filters"]["namespaces"] == ["absent", "data"], (
        "the effective filter must preserve every repeated namespace in deterministic order"
    )
    assert len(body["edges"]) == 1, (
        "namespace filtering must keep cross-namespace traffic without unrelated edges"
    )
    assert body["edges"][0]["source_id"] == "k8s:kind-topology:data:StatefulSet:redis", (
        "an edge leaving the selected namespace was dropped; filters must match either endpoint"
    )
    assert body["edges"][0]["target_id"] == "k8s:kind-topology:demo:Deployment:backend", (
        "namespace filtering must retain the cross-namespace destination as graph context"
    )


def test_kind_filter_keeps_edges_touching_that_kind(client):
    ingest_valid_batch(client)
    body = graph(client, kind="StatefulSet")

    assert len(body["edges"]) == 1, "kind filtering must remove edges unrelated to the kind"
    assert body["edges"][0]["target_id"] == "k8s:kind-topology:data:StatefulSet:redis", (
        "the edge touching the requested StatefulSet kind must remain visible"
    )


def test_query_filter_is_a_case_insensitive_label_substring(client):
    ingest_valid_batch(client)
    body = graph(client, query="ReDi")

    assert len(body["edges"]) == 1, (
        "mixed-case substring filtering must find labels without becoming case-sensitive"
    )
    assert body["edges"][0]["target_id"] == "k8s:kind-topology:data:StatefulSet:redis", (
        "substring filtering returned an edge whose endpoint label does not contain the query"
    )


def test_include_external_false_removes_external_node_and_its_edge(client):
    ingest_valid_batch(client)
    body = graph(client, include_external=False)

    node_ids = {node["id"] for node in body["nodes"]}
    assert "external:EXTERNAL" not in node_ids, (
        "include_external=false leaked the synthetic external node into the graph"
    )
    assert all(
        "external:EXTERNAL" not in (edge["source_id"], edge["target_id"])
        for edge in body["edges"]
    ), "removing the external node must also remove edges that reference it"


# ── T-4.9 / T-4.10: window scoping and deterministic ordering ─────────────────────────────


def test_graph_nodes_are_derived_only_from_edges_inside_the_requested_window(client):
    ingest_valid_batch(client)

    outside = load_batch()
    outside["batch_id"] = OUTSIDE_BATCH_ID
    outside["edges"] = [outside["edges"][0]]
    outside["edges"][0]["source"] = {
        "id": "k8s:kind-topology:archive:Deployment:old-client",
        "kind": "Deployment",
        "namespace": "archive",
        "name": "old-client",
    }
    outside["edges"][0]["target"] = {
        "id": "k8s:kind-topology:archive:Service:old-api",
        "kind": "Service",
        "namespace": "archive",
        "name": "old-api",
    }
    outside["edges"][0]["first_seen"] = "2026-08-10T08:59:50Z"
    outside["edges"][0]["last_seen"] = "2026-08-10T09:00:00Z"
    ingest_valid_batch(client, outside)

    body = graph(client)
    node_ids = {node["id"] for node in body["nodes"]}

    assert "k8s:kind-topology:demo:Deployment:client" in node_ids, (
        "the control node from an in-window edge must be present"
    )
    assert "k8s:kind-topology:archive:Deployment:old-client" not in node_ids, (
        "a stored node whose only edge is outside the window leaked into the topology"
    )
    assert "k8s:kind-topology:archive:Service:old-api" not in node_ids, (
        "nodes must be derived from the query result, not from cumulative inventory"
    )


def test_repeated_graph_queries_have_identical_explicit_edge_order(client):
    ingest_valid_batch(client)

    first_edges = graph(client)["edges"]
    second_edges = graph(client)["edges"]
    first_keys = [
        (
            edge["source_id"],
            edge["target_id"],
            edge["protocol"],
            edge["destination_port"],
        )
        for edge in first_edges
    ]
    second_keys = [
        (
            edge["source_id"],
            edge["target_id"],
            edge["protocol"],
            edge["destination_port"],
        )
        for edge in second_edges
    ]

    assert first_keys == second_keys, (
        "identical graph queries reordered edges; unstable order breaks repeatable UI layout"
    )
    assert first_keys == sorted(first_keys), (
        "edges are not sorted by (source_id, target_id, protocol, destination_port)"
    )


# ── T-4.13: URL-encoded detail identifiers ─────────────────────────────────────────────────


def detail_batch() -> dict:
    batch = load_batch()
    batch["edges"][0]["target"] = deepcopy(batch["edges"][1]["source"])
    return batch


def test_url_encoded_node_id_returns_incoming_and_outgoing_and_unknown_is_404(client):
    ingest_valid_batch(client, detail_batch())
    node_id = "k8s:kind-topology:demo:Deployment:backend"

    response = client.get(
        f"/api/v1/nodes/{quote(node_id, safe='')}",
        params=QUERY_WINDOW,
    )

    assert response.status_code == 200, (
        "a URL-encoded canonical node id containing ':' must resolve to its detail view"
    )
    body = response.json()
    assert body["node"]["id"] == node_id, (
        "URL decoding resolved the request to a different node identity"
    )
    assert len(body["incoming"]) == 1, "node detail omitted its in-window incoming dependency"
    assert len(body["outgoing"]) == 2, "node detail omitted its in-window outgoing dependencies"

    missing_id = "k8s:kind-topology:demo:Service:missing"
    missing = client.get(
        f"/api/v1/nodes/{quote(missing_id, safe='')}",
        params=QUERY_WINDOW,
    )
    assert missing.status_code == 404, (
        "an unknown node id must be 404 rather than an empty detail object"
    )


def test_url_encoded_edge_id_returns_endpoints_and_unknown_is_404(client):
    batch = detail_batch()
    ingest_valid_batch(client, batch)
    source_id = "k8s:kind-topology:demo:Deployment:client"
    target_id = "k8s:kind-topology:demo:Deployment:backend"
    edge_id = f"{source_id}|{target_id}|TCP|8080"

    response = client.get(
        f"/api/v1/edges/{quote(edge_id, safe='')}",
        params=QUERY_WINDOW,
    )

    assert response.status_code == 200, (
        "a URL-encoded edge id containing ':' and '|' must resolve to its detail view"
    )
    body = response.json()
    assert body["edge"]["id"] == edge_id, (
        "URL decoding resolved the request to a different edge identity"
    )
    assert body["source"]["id"] == source_id, "edge detail returned the wrong source endpoint"
    assert body["target"]["id"] == target_id, "edge detail returned the wrong target endpoint"

    unknown_edge_id = f"{source_id}|{target_id}|TCP|9999"
    missing = client.get(
        f"/api/v1/edges/{quote(unknown_edge_id, safe='')}",
        params=QUERY_WINDOW,
    )
    assert missing.status_code == 404, (
        "an unknown edge id must be 404 rather than an empty detail object"
    )


# ── T-4.11 and operational endpoints ──────────────────────────────────────────────────────


class CredentialLeakingRepository(InMemoryRepository):
    async def query_edges(self, window, filters):
        raise RuntimeError(
            "Traceback while connecting to postgresql://admin:super-secret-password@db/topology"
        )


def assert_safe_error(response, request_id: str) -> None:
    body = response.json()
    rendered = json.dumps(body).casefold()
    assert body.get("request_id") == request_id, (
        "error responses need their request_id so operators can correlate them with logs"
    )
    for forbidden in ("traceback", "postgresql://", "super-secret-password"):
        assert forbidden not in rendered, (
            f"error response leaked {forbidden!r}; client-visible failures must not expose secrets"
        )


def test_expected_error_responses_are_safe_and_carry_the_request_id(client):
    cases = [
        client.get(
            "/api/v1/graph",
            params={"window": "1h", **QUERY_WINDOW},
            headers={"x-request-id": "validation-case"},
        ),
        client.get(
            f"/api/v1/nodes/{quote('k8s:kind-topology:demo:Service:missing', safe='')}",
            params=QUERY_WINDOW,
            headers={"x-request-id": "not-found-case"},
        ),
        client.get(
            "/api/v1/diff",
            params={
                "baseline_from": "2026-08-10T10:00:00Z",
                "baseline_to": "2026-08-10T11:00:00Z",
                "current_from": "2026-08-10T11:00:00Z",
                "current_to": "2026-08-10T12:00:00Z",
            },
            headers={"x-request-id": "not-implemented-case"},
        ),
    ]

    assert [response.status_code for response in cases] == [422, 404, 501], (
        "the error-safety test did not reach validation, not-found, and not-implemented paths"
    )
    for response, request_id in zip(
        cases,
        ("validation-case", "not-found-case", "not-implemented-case"),
        strict=True,
    ):
        assert_safe_error(response, request_id)


def test_unhandled_error_hides_traceback_dsn_and_password_but_keeps_request_id():
    repository = CredentialLeakingRepository(cluster_id="kind-topology")
    app = create_app(repository=repository)
    app.state.clock = lambda: FROZEN_NOW

    with TestClient(app, raise_server_exceptions=False) as test_client:
        response = test_client.get(
            "/api/v1/graph",
            params={"window": "1m"},
            headers={"x-request-id": "internal-error-case"},
        )

    assert response.status_code == 500, "repository failures must be reduced to a stable 500"
    assert_safe_error(response, "internal-error-case")


def test_health_endpoints_distinguish_process_liveness_from_storage_readiness(client):
    live = client.get("/health/live")
    ready = client.get("/health/ready")

    assert live.status_code == 200, "process liveness must always return 200 while serving requests"
    assert live.json()["checks"] == {"process": "ok"}, (
        "liveness must report only the process check and must not depend on storage"
    )
    assert ready.status_code == 200, "the working in-memory repository must be ready"
    assert "storage" in ready.json()["checks"], (
        "readiness must report storage so Kubernetes can gate traffic on repository health"
    )


def test_metrics_are_plain_text_and_ingest_and_graph_counters_move(client):
    before = client.get("/metrics")
    assert before.status_code == 200, "the Prometheus scrape endpoint must remain available"
    assert before.headers["content-type"].startswith("text/plain"), (
        "Prometheus metrics must use text/plain rather than the JSON API content type"
    )
    assert "topology_backend_batches_accepted_total 0\n" in before.text, (
        "a fresh app must not inherit accepted-batch counter state from another app instance"
    )
    assert "topology_backend_graph_queries_total 0\n" in before.text, (
        "a fresh app must not inherit graph-query counter state from another app instance"
    )
    assert "topology_backend_stored_edge_buckets 0\n" in before.text, (
        "a fresh in-memory repository must expose zero stored buckets"
    )

    ingest_valid_batch(client)
    graph(client)
    after = client.get("/metrics")

    assert "topology_backend_batches_accepted_total 1\n" in after.text, (
        "the accepted-batch counter did not move after a successful ingest"
    )
    assert "topology_backend_graph_queries_total 1\n" in after.text, (
        "the graph-query counter did not move after a successful graph request"
    )
    assert "topology_backend_stored_edge_buckets 3\n" in after.text, (
        "the storage gauge must expose the three distinct fixture edge buckets"
    )


def test_diff_endpoint_remains_explicitly_not_implemented(client):
    response = client.get(
        "/api/v1/diff",
        params={
            "baseline_from": "2026-08-10T10:00:00Z",
            "baseline_to": "2026-08-10T11:00:00Z",
            "current_from": "2026-08-10T11:00:00Z",
            "current_to": "2026-08-10T12:00:00Z",
        },
    )
    assert response.status_code == 501, (
        "diff must stay explicitly unavailable until its contract is implemented"
    )
