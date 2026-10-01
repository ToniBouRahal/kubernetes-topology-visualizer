"""The backend's three listeners, over real TLS (ADR-014 T-14.1, T-14.2).

A real server on real sockets, with three throwaway CAs laid out exactly as the chart lays them
out: one signs the server certificate, one signs agent client certificates, one signs frontend
client certificates. Nothing here is mocked, because what is being tested is the handshake.
"""

from __future__ import annotations

import asyncio
import http.client
import json
import socket
import ssl
import threading
import time
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

import pytest
import trustme
import uvloop

from app import serve
from app.settings import Settings

VALID_BATCH = Path(__file__).resolve().parents[2] / "contracts" / "examples" / "batch.valid.json"


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@dataclass
class Pki:
    server_ca: trustme.CA
    agent: trustme.LeafCert
    frontend: trustme.LeafCert
    stranger: trustme.LeafCert
    config: Settings


@pytest.fixture(scope="module")
def pki(tmp_path_factory: pytest.TempPathFactory) -> Pki:
    d = tmp_path_factory.mktemp("pki")
    server_ca, ingest_ca, api_ca, other_ca = (trustme.CA() for _ in range(4))
    server = server_ca.issue_cert("localhost")
    agent = ingest_ca.issue_cert("topology-agent")
    frontend = api_ca.issue_cert("topology-frontend")
    # Right shape, wrong CA: what an attacker with their own CA would present.
    stranger = other_ca.issue_cert("topology-agent")

    server.cert_chain_pems[0].write_to_path(str(d / "server.crt"))
    server.private_key_pem.write_to_path(str(d / "server.key"))
    ingest_ca.cert_pem.write_to_path(str(d / "ingest-ca.crt"))
    api_ca.cert_pem.write_to_path(str(d / "api-ca.crt"))

    config = Settings(
        ops_port=_free_port(),
        ingest_port=_free_port(),
        api_port=_free_port(),
        tls_cert_file=str(d / "server.crt"),
        tls_key_file=str(d / "server.key"),
        tls_ingest_client_ca_file=str(d / "ingest-ca.crt"),
        tls_api_client_ca_file=str(d / "api-ca.crt"),
    )
    return Pki(server_ca, agent, frontend, stranger, config)


@pytest.fixture(scope="module")
def running(pki: Pki) -> Iterator[Settings]:
    """The real serve(), on its own loop in a thread, until the module's tests are done."""
    loop = uvloop.new_event_loop()
    stop = asyncio.Event()
    thread = threading.Thread(target=lambda: loop.run_until_complete(serve.serve(pki.config, stop)))
    thread.start()
    deadline = time.monotonic() + 10
    for port in (pki.config.ops_port, pki.config.ingest_port, pki.config.api_port):
        while True:
            try:
                socket.create_connection(("127.0.0.1", port), timeout=0.2).close()
                break
            except OSError:
                if time.monotonic() > deadline:
                    raise
                time.sleep(0.05)
    yield pki.config
    loop.call_soon_threadsafe(stop.set)
    thread.join(timeout=10)


def _client(pki: Pki, cert: trustme.LeafCert | None, *, tls12: bool = False) -> ssl.SSLContext:
    context = ssl.create_default_context()
    pki.server_ca.configure_trust(context)
    if cert is not None:
        cert.configure_cert(context)
    if tls12:
        context.maximum_version = ssl.TLSVersion.TLSv1_2
    return context


def _request(
    port: int, method: str, path: str, *, context: ssl.SSLContext | None, body: bytes | None = None
) -> int:
    conn = (
        http.client.HTTPSConnection("localhost", port, context=context, timeout=5)
        if context
        else http.client.HTTPConnection("localhost", port, timeout=5)
    )
    try:
        conn.request(
            method, path, body=body, headers={"Content-Type": "application/json"} if body else {}
        )
        return conn.getresponse().status
    finally:
        conn.close()


def _refused(port: int, context: ssl.SSLContext) -> bool:
    """True when the server will not serve this client. Under TLS 1.3 a rejected client
    certificate surfaces on the first read, not in the handshake, so a request is made."""
    try:
        _request(port, "GET", "/health/live", context=context)
    except (ssl.SSLError, ConnectionError, http.client.RemoteDisconnected):
        return True
    return False


# ── T-14.1: who can connect ─────────────────────────────────────────────────────────────────


def test_ingest_refuses_no_certificate(pki: Pki, running: Settings) -> None:
    assert _refused(running.ingest_port, _client(pki, None))


def test_ingest_refuses_the_frontends_certificate(pki: Pki, running: Settings) -> None:
    assert _refused(running.ingest_port, _client(pki, pki.frontend))


def test_ingest_refuses_a_certificate_from_another_ca(pki: Pki, running: Settings) -> None:
    assert _refused(running.ingest_port, _client(pki, pki.stranger))


def test_api_refuses_the_agents_certificate(pki: Pki, running: Settings) -> None:
    assert _refused(running.api_port, _client(pki, pki.agent))


def test_api_refuses_no_certificate(pki: Pki, running: Settings) -> None:
    assert _refused(running.api_port, _client(pki, None))


def test_tls_below_1_3_is_refused(pki: Pki, running: Settings) -> None:
    with pytest.raises(ssl.SSLError):
        _request(running.ingest_port, "GET", "/", context=_client(pki, pki.agent, tls12=True))


def test_the_agent_can_ingest(pki: Pki, running: Settings) -> None:
    batch = json.loads(VALID_BATCH.read_text(encoding="utf-8"))
    status = _request(
        running.ingest_port,
        "POST",
        "/api/v1/ingest/batches",
        context=_client(pki, pki.agent),
        body=json.dumps(batch).encode(),
    )
    # 202 stored, or 200 if an earlier test already stored this batch id.
    assert status in (200, 202)


def test_the_frontend_can_read(pki: Pki, running: Settings) -> None:
    assert (
        _request(
            running.api_port,
            "GET",
            "/api/v1/namespaces?window=5m",
            context=_client(pki, pki.frontend),
        )
        == 200
    )


# ── T-14.2: what each listener serves ───────────────────────────────────────────────────────


@pytest.mark.parametrize(
    ("listener", "method", "path", "expected"),
    [
        # The frontend's listener cannot write, and does not expose ops.
        ("api", "POST", "/api/v1/ingest/batches", 404),
        ("api", "GET", "/metrics", 404),
        ("api", "GET", "/health/live", 404),
        # The agent's listener cannot read.
        ("ingest", "GET", "/api/v1/graph?window=5m", 404),
        ("ingest", "GET", "/api/v1/namespaces?window=5m", 404),
        # The plain listener carries no topology, in either direction.
        ("ops", "GET", "/api/v1/graph?window=5m", 404),
        ("ops", "POST", "/api/v1/ingest/batches", 404),
        ("ops", "GET", "/health/live", 200),
        ("ops", "GET", "/metrics", 200),
    ],
)
def test_each_listener_serves_only_its_own_paths(
    pki: Pki, running: Settings, listener: str, method: str, path: str, expected: int
) -> None:
    port, context = {
        "api": (running.api_port, _client(pki, pki.frontend)),
        "ingest": (running.ingest_port, _client(pki, pki.agent)),
        "ops": (running.ops_port, None),
    }[listener]
    body = b"{}" if method == "POST" else None
    assert _request(port, method, path, context=context, body=body) == expected


def test_half_a_tls_configuration_refuses_to_start(pki: Pki) -> None:
    config = pki.config.model_copy(update={"tls_api_client_ca_file": ""})
    with pytest.raises(ValueError, match="must be set together"):
        serve.listeners(config)


def test_no_tls_configuration_is_one_plain_listener_for_development() -> None:
    first, rest = serve.listeners(Settings())
    assert rest == []


def test_a_failed_startup_opens_no_data_listener(pki: Pki) -> None:
    """If the ops listener cannot start, the process exits 1 and the mTLS ports never open —
    they must not accept batches before the database is attached."""
    blocker = socket.socket()
    blocker.bind(("0.0.0.0", 0))
    blocker.listen()
    config = pki.config.model_copy(
        update={
            "ops_port": blocker.getsockname()[1],
            "ingest_port": _free_port(),
            "api_port": _free_port(),
        }
    )
    loop = uvloop.new_event_loop()
    try:
        with pytest.raises(SystemExit) as exited:
            loop.run_until_complete(serve.serve(config, asyncio.Event()))
        assert exited.value.code == 1
        with pytest.raises(OSError):
            socket.create_connection(("127.0.0.1", config.ingest_port), timeout=0.5).close()
    finally:
        loop.close()
        blocker.close()


def _logged_users(caplog: pytest.LogCaptureFixture, path: str) -> list[str | None]:
    return [
        getattr(r, "user", None)
        for r in caplog.records
        if r.name == "api.request" and getattr(r, "path", None) == path
    ]


def test_the_signed_in_user_is_logged_from_the_api_listener(
    pki: Pki, running: Settings, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level("INFO", logger="api.request")
    conn = http.client.HTTPSConnection(
        "localhost", running.api_port, context=_client(pki, pki.frontend), timeout=5
    )
    conn.request(
        "GET", "/api/v1/namespaces?window=1h", headers={"X-Forwarded-Email": "ada@example.org"}
    )
    assert conn.getresponse().status == 200
    conn.close()
    time.sleep(0.2)
    assert "ada@example.org" in _logged_users(caplog, "/api/v1/namespaces")


def test_a_user_header_on_the_plain_listener_is_not_trusted(
    pki: Pki, running: Settings, caplog: pytest.LogCaptureFixture
) -> None:
    """Anyone can send any header to the plain ops port; it must not end up in the audit log."""
    caplog.set_level("INFO", logger="api.request")
    conn = http.client.HTTPConnection("localhost", running.ops_port, timeout=5)
    conn.request("GET", "/health/live", headers={"X-Forwarded-Email": "mallory@example.org"})
    assert conn.getresponse().status == 200
    conn.close()
    time.sleep(0.2)
    users = _logged_users(caplog, "/health/live")
    assert users, "the request should have been logged"
    assert "mallory@example.org" not in users
