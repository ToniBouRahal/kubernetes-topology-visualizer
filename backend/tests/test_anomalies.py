"""Rule-based anomalies and canaries — ADR-016 T-16.1 to T-16.6.

The rules are thresholds, so every test pins one exactly at the threshold and one step below it:
a rule that fires a step early or late is the bug this file exists to catch.
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import Iterator
from datetime import UTC, datetime
from pathlib import Path

import pytest
import uvloop
from fastapi.testclient import TestClient

from app.domain.anomalies import CanarySet, Thresholds, detect
from app.domain.models import EXTERNAL_NODE_ID
from app.domain.window import ResolvedWindow
from app.main import create_app, evaluate_once
from app.persistence.protocol import EdgeAggregate

asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())

REPO_ROOT = Path(__file__).resolve().parents[2]
VALID_BATCH = REPO_ROOT / "contracts" / "examples" / "batch.valid.json"
FROZEN_NOW = datetime(2026, 8, 10, 12, 1, tzinfo=UTC)
REDIS = "k8s:kind-topology:data:StatefulSet:redis"

BASELINE = ResolvedWindow(
    start=datetime(2026, 8, 9, 11, tzinfo=UTC), end=datetime(2026, 8, 10, 11, tzinfo=UTC)
)
CURRENT = ResolvedWindow(
    start=datetime(2026, 8, 10, 11, tzinfo=UTC), end=datetime(2026, 8, 10, 12, tzinfo=UTC)
)
RULES = Thresholds(min_attempts=10, failure_ratio=0.2, setup_factor=3.0)


def edge(
    source: str = "client",
    target: str = "backend",
    count: int = 10,
    port: int = 8080,
    failed: int | None = 0,
    latency: tuple[int, int] = (0, 0),
) -> EdgeAggregate:
    target_id = target if target == EXTERNAL_NODE_ID else f"k8s:c:demo:Deployment:{target}"
    return EdgeAggregate(
        source_id=f"k8s:c:demo:Deployment:{source}",
        target_id=target_id,
        protocol="TCP",
        destination_port=port,
        connection_count=count,
        failed_connection_count=failed,
        connect_latency_count=latency[0],
        connect_latency_sum_us=latency[1],
        first_seen=datetime(2026, 8, 10, 11, tzinfo=UTC),
        last_seen=datetime(2026, 8, 10, 11, 30, tzinfo=UTC),
    )


def run(current, baseline, canaries=frozenset()):
    found, observed = detect(
        current_edges=current,
        baseline_edges=baseline,
        canary_targets=set(canaries),
        window=CURRENT,
        baseline_window=BASELINE,
        thresholds=RULES,
    )
    return found, observed


def types(found) -> list[str]:
    return [a.type for a in found]


# ── T-16.1: each rule at its threshold ─────────────────────────────────────────────────────


def test_an_edge_absent_from_the_baseline_is_new():
    found, observed = run([edge(), edge(target="cache")], [edge(target="cache")])
    assert observed
    assert types(found) == ["NEW_EDGE"], "only the edge missing from the baseline is new"
    assert found[0].reason == "not observed in the previous 24 h"


def test_new_traffic_to_external_is_its_own_critical_type():
    found, _ = run([edge(target=EXTERNAL_NODE_ID, port=443)], [edge()])
    assert types(found) == ["NEW_EXTERNAL_EGRESS"]
    assert found[0].severity == "critical"


def test_a_new_port_to_external_is_new_even_when_another_port_is_known():
    """The edge key includes the port, so 443 known does not make 4444 familiar."""
    known = edge(target=EXTERNAL_NODE_ID, port=443)
    found, _ = run([known, edge(target=EXTERNAL_NODE_ID, port=4444)], [known])
    assert [(a.type, a.destination_port) for a in found] == [("NEW_EXTERNAL_EGRESS", 4444)]


def test_failure_spike_fires_at_threshold_and_not_below():
    baseline = [edge(count=100, failed=0)]
    # 8 failed of 10 attempts total: 20 % exactly would be 2 of 10.
    at = run([edge(count=8, failed=2)], baseline)[0]
    below = run([edge(count=9, failed=1)], baseline)[0]
    assert types(at) == ["FAILURE_SPIKE"]
    assert "20% of 10 attempts failed, against 0%" in at[0].reason
    assert types(below) == []


def test_failure_spike_needs_enough_attempts():
    found, _ = run([edge(count=4, failed=5)], [edge(count=100)])
    assert types(found) == [], "nine attempts is below the minimum of ten"


def test_failure_spike_needs_a_rise_not_just_a_high_ratio():
    """A dependency that always fails 50 % is broken, but it is not news."""
    found, _ = run([edge(count=5, failed=5)], [edge(count=50, failed=50)])
    assert types(found) == []


def test_unmeasured_failures_never_spike():
    """Null is "not measured", not zero (contracts/ids.md §10)."""
    found, _ = run([edge(count=10, failed=None)], [edge(count=10, failed=None)])
    assert types(found) == []


def test_slow_setup_fires_at_three_times_and_not_below():
    baseline = [edge(latency=(10, 10 * 1000))]  # 1 ms mean
    at = run([edge(latency=(10, 10 * 3000))], baseline)[0]
    below = run([edge(latency=(10, 10 * 2999))], baseline)[0]
    assert types(at) == ["SLOW_SETUP"]
    assert at[0].current_mean_setup_us == 3000.0
    assert at[0].baseline_mean_setup_us == 1000.0
    assert types(below) == []


def test_slow_setup_ignores_a_large_ratio_of_tiny_times():
    found, _ = run([edge(latency=(10, 10 * 300))], [edge(latency=(10, 10 * 50))])
    assert types(found) == [], "250 µs slower is under the 1 ms floor"


def test_slow_setup_needs_samples_in_both_periods():
    found, _ = run([edge(latency=(10, 10 * 9000))], [edge(latency=(9, 9 * 1000))])
    assert types(found) == []


def test_canary_contact_fires_regardless_of_baseline():
    target = "k8s:c:demo:Deployment:decoy"
    found, _ = run([edge(target="decoy", count=0, failed=1)], [], canaries={target})
    assert types(found) == ["CANARY_CONTACT"]
    assert found[0].severity == "critical"


def test_one_edge_can_raise_several_rules_in_a_fixed_order():
    target = "k8s:c:demo:Deployment:decoy"
    found, _ = run(
        [edge(target="decoy", count=5, failed=5)], [edge(target="other")], canaries={target}
    )
    assert types(found) == ["CANARY_CONTACT", "NEW_EDGE", "FAILURE_SPIKE"]
    assert len({a.id for a in found}) == 3, "ids must stay distinct per rule"


# ── T-16.2 / T-16.3: empty baseline, ordering ──────────────────────────────────────────────


def test_an_empty_baseline_suppresses_novelty_but_not_other_rules():
    target = "k8s:c:demo:Deployment:decoy"
    found, observed = run(
        [edge(), edge(target="decoy"), edge(target=EXTERNAL_NODE_ID)], [], canaries={target}
    )
    assert not observed
    assert types(found) == ["CANARY_CONTACT"]


def test_output_is_ordered_by_edge_key_whatever_the_input_order():
    current = [edge(source="b"), edge(source="a"), edge(source="c")]
    found, _ = run(current, [edge(source="z")])
    assert [a.source_id.rsplit(":", 1)[-1] for a in found] == ["a", "b", "c"]


# ── T-16.4: canary selectors ───────────────────────────────────────────────────────────────


def test_canary_selectors_match_on_fields_with_and_without_kind():
    canaries = CanarySet.parse(["demo/decoy", "data/StatefulSet/vault"])
    assert canaries.matches("demo", "Deployment", "decoy")
    assert canaries.matches("demo", "Service", "decoy"), "no kind means any kind"
    assert canaries.matches("data", "StatefulSet", "vault")
    assert not canaries.matches("data", "Deployment", "vault")
    assert not canaries.matches(None, "Pod", "decoy"), "the external node is never a canary"


@pytest.mark.parametrize("entry", ["decoy", "a/b/c/d", "demo/", "/decoy"])
def test_a_malformed_canary_is_refused(entry):
    with pytest.raises(ValueError, match="namespace/name"):
        CanarySet.parse([entry])


# ── API: T-16.4, T-16.5, T-16.6 ────────────────────────────────────────────────────────────


@pytest.fixture
def client() -> Iterator[TestClient]:
    app = create_app()
    app.state.clock = lambda: FROZEN_NOW
    app.state.canaries = CanarySet.parse(["data/StatefulSet/redis"])
    with TestClient(app) as test_client:
        yield test_client


def ingest(client: TestClient) -> int:
    batch = json.loads(VALID_BATCH.read_text(encoding="utf-8"))
    return client.post("/api/v1/ingest/batches", json=batch).status_code


def test_graph_nodes_carry_the_canary_flag(client):
    assert ingest(client) == 202
    nodes = client.get("/api/v1/graph", params={"window": "5m"}).json()["nodes"]
    assert {n["id"]: n["canary"] for n in nodes if n["canary"]} == {REDIS: True}


def test_canary_contact_is_counted_once_and_logged(client, caplog):
    with caplog.at_level(logging.WARNING, logger="api.canary"):
        assert ingest(client) == 202
        assert ingest(client) == 200, "the replay must be recognised"

    metrics = client.get("/metrics").text
    assert "topology_backend_canary_connections_total 143\n" in metrics, (
        "143 attempts reached redis; a replayed batch must not add them again"
    )
    warnings = [r for r in caplog.records if r.getMessage() == "canary contacted"]
    assert len(warnings) == 1
    assert warnings[0].source == "demo/Deployment/backend"


def test_anomalies_route_reports_canary_contact_without_a_baseline(client):
    ingest(client)
    response = client.get("/api/v1/anomalies", params={"window": "5m"})
    assert response.status_code == 200, response.text
    body = response.json()
    assert [(a["type"], a["target_id"]) for a in body["anomalies"]] == [("CANARY_CONTACT", REDIS)]
    assert body["summary"] == {
        "critical_count": 1,
        "warning_count": 0,
        "baseline_observed": False,
        "truncated": False,
    }
    assert body["baseline"] == {
        "start": "2026-08-09T11:56:00Z",
        "end": "2026-08-10T11:56:00Z",
    }
    assert {n["id"] for n in body["nodes"]} == {
        REDIS,
        "k8s:kind-topology:demo:Deployment:backend",
    }, "exactly the nodes the anomalies refer to"


def test_anomalies_route_rejects_a_bad_window(client):
    assert client.get("/api/v1/anomalies", params={"window": "2d"}).status_code == 422


def test_metrics_expose_every_anomaly_type_before_any_evaluation(client):
    metrics = client.get("/metrics").text
    for name in ("canary_contact", "new_external_egress", "new_edge", "failure_spike"):
        assert f'topology_backend_active_anomalies{{type="{name}"}} 0\n' in metrics
    assert 'topology_backend_active_anomalies{type="slow_setup"} 0\n' in metrics


def test_the_evaluator_sets_gauges_and_logs_each_anomaly_once(client, caplog):
    ingest(client)
    app = client.app
    with caplog.at_level(logging.WARNING, logger="app.anomalies"):
        active = asyncio.run(evaluate_once(app, set()))
        asyncio.run(evaluate_once(app, active))

    assert 'topology_backend_active_anomalies{type="canary_contact"} 1\n' in (
        client.get("/metrics").text
    )
    logged = [r for r in caplog.records if r.getMessage() == "anomaly detected"]
    assert len(logged) == 1, "a standing anomaly is logged when it appears, not every pass"
    assert logged[0].target == "redis"
