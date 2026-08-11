"""Contract fixture corpus tests — T-3.1, T-3.2, T-3.6, T-3.7.

The corpus in contracts/examples/ is shared with the Go agent and the frontend. Each fixture's
expected outcome is declared in manifest.json, so all three languages test the same expectations.

Layering of the assertions is deliberate:
  * 422 cases are *model* failures  — Pydantic rejects the shape.
  * 400 and 413 are *route* rules   — the payload is well-formed but unacceptable.
Testing them at the wrong layer would pass for the wrong reason.
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from pydantic import ValidationError

from app.domain.models import (
    EXTERNAL_NODE_ID,
    IngestBatch,
    build_edge_id,
    build_node_id,
)
from app.main import create_app

REPO_ROOT = Path(__file__).resolve().parents[2]
EXAMPLES = REPO_ROOT / "contracts" / "examples"
MANIFEST = json.loads((EXAMPLES / "manifest.json").read_text(encoding="utf-8"))
CASES = MANIFEST["cases"]

VALID = [c for c in CASES if c["expected_status"] == 202]
MODEL_INVALID = [c for c in CASES if c["expected_status"] == 422]
ROUTE_INVALID = [c for c in CASES if c["expected_status"] not in (202, 422)]


def load(fixture: str) -> dict:
    return json.loads((EXAMPLES / fixture).read_text(encoding="utf-8"))


@pytest.fixture(scope="module")
def client() -> TestClient:
    return TestClient(create_app())


# ── T-3.1 / T-3.2: every fixture behaves as its manifest entry declares ─────────────────────


def test_manifest_covers_every_fixture_file():
    """No fixture may exist without a declared expectation, and vice versa."""
    on_disk = {p.name for p in EXAMPLES.glob("*.json")} - {"manifest.json"}
    declared = {c["fixture"] for c in CASES}
    assert on_disk == declared, (
        f"fixture/manifest mismatch: only on disk={on_disk - declared}, "
        f"only in manifest={declared - on_disk}"
    )


@pytest.mark.parametrize("case", VALID, ids=lambda c: c["fixture"])
def test_valid_fixtures_pass_model_validation(case):
    batch = IngestBatch.model_validate(load(case["fixture"]))
    assert batch.edges, "a valid batch must contain at least one edge"
    assert all(e.connection_count >= 1 for e in batch.edges)


@pytest.mark.parametrize("case", MODEL_INVALID, ids=lambda c: c["fixture"])
def test_invalid_fixtures_are_rejected_by_the_model(case):
    with pytest.raises(ValidationError):
        IngestBatch.model_validate(load(case["fixture"]))


@pytest.mark.parametrize("case", VALID, ids=lambda c: c["fixture"])
def test_valid_fixtures_clear_request_validation(client, case):
    """A valid batch must not be rejected as malformed.

    Phase 0 handlers are unimplemented, so a batch that clears validation surfaces as 501.
    Phase 2 (P2-B4) tightens this to exactly 202/200.
    """
    r = client.post("/api/v1/ingest/batches", json=load(case["fixture"]))
    assert r.status_code not in (400, 413, 422), r.text
    assert r.status_code in (202, 200, 501)


@pytest.mark.parametrize("case", MODEL_INVALID, ids=lambda c: c["fixture"])
def test_malformed_batches_return_422(client, case):
    r = client.post("/api/v1/ingest/batches", json=load(case["fixture"]))
    assert r.status_code == 422, f"{case['fixture']}: {case['rule']}"


# ── T-3.7: version rejection is distinct from shape rejection ──────────────────────────────


@pytest.mark.parametrize("case", ROUTE_INVALID, ids=lambda c: c["fixture"])
def test_route_level_rejections(client, case):
    r = client.post("/api/v1/ingest/batches", json=load(case["fixture"]))
    assert r.status_code == case["expected_status"], f"{case['fixture']}: {case['rule']}"


def test_unsupported_version_is_400_not_422(client):
    """The 400/422 split is load-bearing: it tells an agent whether retrying could ever help."""
    doc = load("batch.valid.json")
    doc["schema_version"] = 99
    r = client.post("/api/v1/ingest/batches", json=doc)
    assert r.status_code == 400
    assert "schema_version" in r.text


def test_edge_count_limit_is_413(client):
    from app.settings import settings

    doc = load("batch.valid.json")
    doc["edges"] = [doc["edges"][0]] * (settings.max_batch_edges + 1)
    r = client.post("/api/v1/ingest/batches", json=doc)
    assert r.status_code == 413


# ── Identity grammar — contracts/ids.md §1, §4 ─────────────────────────────────────────────


def test_canonical_node_id_grammar():
    assert (
        build_node_id("kind-topology", "demo", "Deployment", "client")
        == "k8s:kind-topology:demo:Deployment:client"
    )


def test_external_node_id_is_a_single_constant():
    assert EXTERNAL_NODE_ID == "external:EXTERNAL"


@pytest.mark.parametrize(
    "segments",
    [
        ("kind-topology", "demo", "Deployment", ""),
        ("kind-topology", "", "Deployment", "client"),
        ("kind-topology", "demo", "Deployment", "has:colon"),
        ("has:colon", "demo", "Deployment", "client"),
    ],
)
def test_node_id_rejects_empty_or_ambiguous_segments(segments):
    """A name containing the separator would make the ID ambiguous — and quietly so."""
    with pytest.raises(ValueError):
        build_node_id(*segments)


def test_edge_id_encodes_the_edge_key():
    """The edge key is (cluster, source, target, protocol, port) — contracts/ids.md §4."""
    edge_id = build_edge_id(
        "k8s:c:demo:Deployment:client", "k8s:c:demo:Service:backend", "TCP", 8080
    )
    assert edge_id == "k8s:c:demo:Deployment:client|k8s:c:demo:Service:backend|TCP|8080"


def test_allowed_kinds_are_exactly_six():
    """ReplicaSet must never appear: pods collapse through it to the Deployment."""
    from typing import get_args

    from app.domain.models import NodeKind

    assert set(get_args(NodeKind)) == {
        "Service",
        "Deployment",
        "StatefulSet",
        "DaemonSet",
        "Job",
        "Pod",
    }


# ── T-3.6: the committed contract matches what the app generates ───────────────────────────


def test_committed_openapi_matches_application():
    result = subprocess.run(
        [sys.executable, str(REPO_ROOT / "scripts" / "export_openapi.py"), "--check"],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stdout + result.stderr
