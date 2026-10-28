"""Chat alerts — ADR-018 T-18.1 to T-18.7.

Nothing here reaches the Internet: delivery is tested against a stub `post` and, for the real
urllib path, against an HTTP server on 127.0.0.1.
"""

from __future__ import annotations

import asyncio
import json
import logging
import threading
from collections.abc import Iterator
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest
import uvloop
from fastapi.testclient import TestClient

from app.alerts import messages
from app.alerts.channels import Channel, channels_from, slack_body, teams_body, telegram_body
from app.alerts.dispatch import (
    QUEUE_CAPACITY,
    Dispatcher,
    Reply,
    from_settings,
    post_json,
)
from app.api.logging import JsonFormatter
from app.api.metrics import Metrics
from app.domain.anomalies import CanarySet, Thresholds, detect
from app.domain.models import GraphNode
from app.domain.window import ResolvedWindow
from app.main import create_app, evaluate_once
from app.persistence.protocol import EdgeAggregate
from app.settings import Settings

asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())

REPO_ROOT = Path(__file__).resolve().parents[2]
VALID_BATCH = REPO_ROOT / "contracts" / "examples" / "batch.valid.json"
FROZEN_NOW = datetime(2026, 8, 10, 12, 1, tzinfo=UTC)

SLACK = "https://hooks.slack.com/services/T000/B000/SECRETSLACKPART"
TEAMS = "https://prod-00.westus.logic.azure.com/workflows/abc/triggers/manual/run?sig=SECRETSIG"
TOKEN = "123456:SECRET-telegram_token"

HOSTILE = "<!channel> *bold* [click](https://evil.example) & `code`"


def configure(**overrides: object) -> tuple[Channel, ...]:
    values: dict = {
        "enabled": True,
        "slack_webhook_url": "",
        "teams_webhook_url": "",
        "telegram_bot_token": "",
        "telegram_chat_id": "",
    }
    values.update(overrides)
    return channels_from(**values)


def message(**overrides: object) -> messages.Message:
    values: dict = {
        "severity": "critical",
        "title": "Decoy touched: demo/legacy-admin (Deployment)",
        "lines": ("demo/intruder (Job) connected to it on TCP port 2222 (5 attempts).",),
        "footer": "Cluster kind-topology · 2026-08-10 12:01:00 UTC",
    }
    values.update(overrides)
    return messages.Message(**values)


class Recorder(Dispatcher):
    """A dispatcher that keeps what it would have queued."""

    def __init__(self, metrics: Metrics | None = None, **kwargs: object) -> None:
        super().__init__(
            (Channel("telegram", "https://api.telegram.org/botX/sendMessage", "42"),),
            metrics or Metrics(),
            cluster_id="kind-topology",
            **kwargs,
        )
        self.sent: list[messages.Message] = []
        self.anomaly_calls: list[list] = []

    def submit(self, message: messages.Message) -> None:
        self.sent.append(message)


# ── T-18.1: configuration ───────────────────────────────────────────────────────────────────


def test_alerts_are_off_by_default():
    assert Settings().alerts_enabled is False
    assert from_settings(Settings(), Metrics()) is None


def test_off_means_off_whatever_credentials_are_present():
    assert configure(enabled=False, slack_webhook_url=SLACK) == ()


def test_enabled_without_a_channel_refuses_to_start():
    with pytest.raises(ValueError, match="no channel is configured"):
        configure()


def test_enabled_without_a_channel_stops_the_app(monkeypatch):
    monkeypatch.setattr("app.main.settings.alerts_enabled", True)
    with pytest.raises(ValueError, match="no channel is configured"):
        create_app()


@pytest.mark.parametrize(
    ("setting", "value", "error"),
    [
        ("slack_webhook_url", "http://hooks.slack.com/services/x", "https://"),
        ("slack_webhook_url", "https://evil.example/services/x", "hooks.slack.com"),
        ("teams_webhook_url", "http://prod-00.logic.azure.com/x", "https://"),
    ],
)
def test_webhook_urls_are_checked_and_the_value_is_never_echoed(setting, value, error):
    with pytest.raises(ValueError, match=error) as raised:
        configure(**{setting: value})
    assert value not in str(raised.value)


def test_telegram_needs_token_and_chat_id_together():
    with pytest.raises(ValueError, match="together"):
        configure(telegram_bot_token=TOKEN)
    with pytest.raises(ValueError, match="together"):
        configure(telegram_chat_id="42")


def test_a_malformed_telegram_token_is_refused_without_echo():
    bad = "123/../../evil"
    with pytest.raises(ValueError, match="not a bot token") as raised:
        configure(telegram_bot_token=bad, telegram_chat_id="42")
    assert bad not in str(raised.value)


def test_every_channel_together():
    channels = configure(
        slack_webhook_url=SLACK, teams_webhook_url=TEAMS, telegram_bot_token=TOKEN,
        telegram_chat_id="-1001",
    )  # fmt: skip
    assert [c.name for c in channels] == ["slack", "teams", "telegram"]
    assert channels[2].url == f"https://api.telegram.org/bot{TOKEN}/sendMessage"
    assert "SECRET" not in repr(channels), "a channel's repr must not carry its credential"


def test_settings_keep_credentials_out_of_their_repr(monkeypatch):
    monkeypatch.setenv("ALERT_SLACK_WEBHOOK_URL", SLACK)
    monkeypatch.setenv("ALERT_TELEGRAM_BOT_TOKEN", TOKEN)
    assert "SECRET" not in repr(Settings())


# ── T-18.2: request bodies, and names that stay inert ───────────────────────────────────────


def test_slack_body_escapes_control_characters_and_turns_formatting_off():
    body = slack_body(message(lines=(HOSTILE,)))
    assert body["mrkdwn"] is False
    assert "<!channel>" not in body["text"]
    assert "&lt;!channel&gt;" in body["text"]
    assert "&amp; `code`" in body["text"]
    assert body["text"].startswith("🚨 Decoy touched")


def test_teams_body_is_one_adaptive_card_with_markdown_escaped():
    body = teams_body(message(lines=(HOSTILE,), link="https://topology.example"))
    assert body["type"] == "message"
    (attachment,) = body["attachments"]
    assert attachment["contentType"] == "application/vnd.microsoft.card.adaptive"
    card = attachment["content"]
    assert card["type"] == "AdaptiveCard" and card["version"] == "1.4"
    texts = [b["text"] for b in card["body"]]
    assert texts[0] == "🚨 Decoy touched: demo/legacy-admin (Deployment)"
    assert texts[1] == r"<!channel> \*bold\* \[click\](https://evil.example) & \`code\`"
    assert card["actions"] == [
        {"type": "Action.OpenUrl", "title": "Open topology", "url": "https://topology.example"}
    ]


def test_teams_body_has_no_action_without_a_link():
    card = teams_body(message())["attachments"][0]["content"]
    assert "actions" not in card


def test_telegram_body_is_plain_text_without_a_parse_mode():
    body = telegram_body(message(lines=(HOSTILE,), link="https://topology.example"), "-1001")
    assert body["chat_id"] == "-1001"
    assert "parse_mode" not in body
    assert HOSTILE in body["text"], "plain text is shown as sent; nothing to escape"
    assert body["text"].endswith("https://topology.example")


def test_telegram_text_is_capped_at_the_api_limit():
    body = telegram_body(message(lines=("x" * 5000,)), "1")
    assert len(body["text"]) == 4096
    assert body["text"].endswith("…")


def test_canary_message_reads_in_plain_words():
    m = messages.canary_contact(
        source="demo/intruder (Job)",
        canary="demo/legacy-admin (Deployment)",
        destination_port=2222,
        attempts=1,
        cluster_id="kind-topology",
        at=FROZEN_NOW,
    )
    assert m.title == "Decoy touched: demo/legacy-admin (Deployment)"
    assert m.lines[0] == "demo/intruder (Job) connected to it on TCP port 2222 (1 attempt)."
    assert m.footer == "Cluster kind-topology · 2026-08-10 12:01:00 UTC"


def test_the_external_node_reads_as_itself():
    assert messages.describe(None, "External", "EXTERNAL") == "EXTERNAL"
    assert messages.describe("demo", "Job", "intruder") == "demo/intruder (Job)"


# ── T-18.3: canary contact at ingest ────────────────────────────────────────────────────────


@pytest.fixture
def recorder_client() -> Iterator[tuple[TestClient, Recorder]]:
    app = create_app()
    app.state.clock = lambda: FROZEN_NOW
    app.state.canaries = CanarySet.parse(["data/StatefulSet/redis"])
    recorder = Recorder(app.state.metrics)
    app.state.alerts = recorder
    with TestClient(app) as test_client:
        yield test_client, recorder


def ingest(client: TestClient, batch_id: str | None = None) -> int:
    batch = json.loads(VALID_BATCH.read_text(encoding="utf-8"))
    if batch_id:
        batch["batch_id"] = batch_id
    return client.post("/api/v1/ingest/batches", json=batch).status_code


def test_canary_contact_at_ingest_queues_one_message(recorder_client):
    client, recorder = recorder_client
    assert ingest(client) == 202
    assert ingest(client) == 200, "a replay is recognised and queues nothing"

    (sent,) = recorder.sent
    assert sent.title == "Decoy touched: data/redis (StatefulSet)"
    assert sent.lines[0].startswith("demo/backend (Deployment) connected to it on TCP port ")
    assert "(143 attempts)" in sent.lines[0]


def test_repeat_contact_is_suppressed_within_the_cooldown(recorder_client):
    client, recorder = recorder_client
    clock = [1000.0]
    recorder._monotonic = lambda: clock[0]

    assert ingest(client, "01J00000000000000000000001") == 202
    clock[0] += 299
    assert ingest(client, "01J00000000000000000000002") == 202
    assert len(recorder.sent) == 1, "inside the cooldown: one message, however many contacts"

    clock[0] += 1
    assert ingest(client, "01J00000000000000000000003") == 202
    assert len(recorder.sent) == 2, "the cooldown has passed"


def test_the_cooldown_is_per_source_canary_and_port():
    recorder = Recorder()
    for source, port in [("a", 1), ("a", 1), ("b", 1), ("a", 2)]:
        recorder.notify_canary(
            source=source, canary="c", destination_port=port, attempts=1, at=FROZEN_NOW
        )
    assert len(recorder.sent) == 3


# ── T-18.4: the evaluator ───────────────────────────────────────────────────────────────────

RULES = Thresholds(min_attempts=10, failure_ratio=0.2, setup_factor=3.0)
WINDOW = ResolvedWindow(start=datetime(2026, 8, 10, 11, tzinfo=UTC), end=FROZEN_NOW)
BASELINE = ResolvedWindow(start=datetime(2026, 8, 9, 11, tzinfo=UTC), end=WINDOW.start)


def edge(target: str, failed: int = 0, port: int = 8080) -> EdgeAggregate:
    return EdgeAggregate(
        source_id="k8s:c:demo:Deployment:client",
        target_id=target if target.startswith("external") else f"k8s:c:demo:Deployment:{target}",
        protocol="TCP",
        destination_port=port,
        connection_count=10,
        failed_connection_count=failed,
        connect_latency_count=0,
        connect_latency_sum_us=0,
        first_seen=WINDOW.start,
        last_seen=WINDOW.start,
    )


def found_anomalies():
    """One of each severity, plus a canary contact: NEW_EDGE (warning), FAILURE_SPIKE and
    CANARY_CONTACT (critical)."""
    found, _ = detect(
        current_edges=[edge("new"), edge("flaky", failed=10), edge("decoy")],
        baseline_edges=[edge("flaky"), edge("decoy")],
        canary_targets={"k8s:c:demo:Deployment:decoy"},
        window=WINDOW,
        baseline_window=BASELINE,
        thresholds=RULES,
    )
    return found


def node(name: str) -> GraphNode:
    return GraphNode(
        id=f"k8s:c:demo:Deployment:{name}",
        kind="Deployment",
        namespace="demo",
        name=name,
        label=name,
        first_seen=WINDOW.start,
        last_seen=WINDOW.start,
    )


NODES = {n.id: n for n in map(node, ["client", "new", "flaky", "decoy"])}


def test_the_default_floor_sends_critical_only_and_never_canary_contact():
    found = found_anomalies()
    assert sorted(a.type for a in found) == ["CANARY_CONTACT", "FAILURE_SPIKE", "NEW_EDGE"]

    recorder = Recorder()
    recorder.notify_anomalies(found, NODES, at=FROZEN_NOW)
    (sent,) = recorder.sent
    assert sent.title == "Connections failing"
    assert sent.lines[0].startswith(
        "Connections failing: demo/client (Deployment) → demo/flaky (Deployment) on TCP port 8080."
    )


def test_a_warning_floor_includes_warnings():
    recorder = Recorder(min_severity="warning")
    recorder.notify_anomalies(found_anomalies(), NODES, at=FROZEN_NOW)
    (sent,) = recorder.sent
    assert sent.title == "2 anomalies"
    assert sent.severity == "critical"


def test_nothing_to_send_sends_nothing():
    recorder = Recorder()
    recorder.notify_anomalies(
        [a for a in found_anomalies() if a.type != "FAILURE_SPIKE"], NODES, at=FROZEN_NOW
    )
    assert recorder.sent == []


def test_one_message_per_pass_lists_five_then_counts():
    many, _ = detect(
        current_edges=[edge("new", port=p) for p in range(1, 9)],
        baseline_edges=[edge("other")],
        canary_targets=set(),
        window=WINDOW,
        baseline_window=BASELINE,
        thresholds=RULES,
    )
    recorder = Recorder(min_severity="warning")
    recorder.notify_anomalies(many, NODES, at=FROZEN_NOW)
    (sent,) = recorder.sent
    assert sent.title == "New connection (8)"
    assert len(sent.lines) == 6
    assert sent.lines[-1] == "…and 3 more."


class EvaluatorRecorder(Recorder):
    def notify_anomalies(self, new, nodes, *, at):
        self.anomaly_calls.append([a.type for a in new])


def test_the_first_pass_after_start_up_sends_nothing(recorder_client):
    client, _ = recorder_client
    app = client.app
    recorder = EvaluatorRecorder(app.state.metrics)
    app.state.alerts = recorder
    ingest(client)

    active = asyncio.run(evaluate_once(app, None))
    assert active, "the canary contact is active"
    assert recorder.anomaly_calls == [], "first pass: recorded, not sent"

    asyncio.run(evaluate_once(app, active))
    assert recorder.anomaly_calls == [[]], "a standing anomaly is not new on the next pass"

    asyncio.run(evaluate_once(app, set()))
    assert recorder.anomaly_calls[-1] == ["CANARY_CONTACT"], "newly active ids are handed over"


# ── T-18.5: delivery ────────────────────────────────────────────────────────────────────────


class StubPost:
    def __init__(self, *replies: Reply) -> None:
        self.replies = list(replies)
        self.calls: list[tuple[str, dict]] = []

    def __call__(self, url: str, body: dict, timeout: float) -> Reply:
        self.calls.append((url, body))
        return self.replies.pop(0)


def deliver(*replies: Reply, metrics: Metrics | None = None):
    post = StubPost(*replies)
    slept: list[float] = []

    async def sleep(seconds: float) -> None:
        slept.append(seconds)

    metrics = metrics or Metrics()
    dispatcher = Dispatcher(
        configure(slack_webhook_url=SLACK), metrics, cluster_id="c", post=post, sleep=sleep
    )
    ok = asyncio.run(dispatcher.deliver(dispatcher.channels[0], message()))
    return ok, post, slept, metrics


def test_a_2xx_is_delivered_first_time():
    ok, post, slept, metrics = deliver(Reply(200))
    assert ok and len(post.calls) == 1 and slept == []
    assert post.calls[0][0] == SLACK
    assert metrics.alerts[("slack", "sent")] == 1


def test_429_and_5xx_and_no_response_are_retried_with_backoff():
    ok, post, slept, _ = deliver(Reply(503), Reply(None), Reply(202))
    assert ok and len(post.calls) == 3
    assert slept == [1.0, 4.0]


def test_retry_after_is_honoured_up_to_thirty_seconds():
    _, _, slept, _ = deliver(Reply(429, 12.0), Reply(429, 999.0), Reply(200))
    assert slept == [12.0, 30.0]


def test_three_failures_give_up_and_count():
    ok, post, _, metrics = deliver(Reply(500), Reply(500), Reply(500))
    assert not ok and len(post.calls) == 3
    assert metrics.alerts[("slack", "failed")] == 1


@pytest.mark.parametrize("status", [301, 400, 403, 404])
def test_other_statuses_are_configuration_errors_and_not_retried(status):
    ok, post, slept, _ = deliver(Reply(status))
    assert not ok and len(post.calls) == 1 and slept == []


def test_a_full_queue_drops_and_counts():
    metrics = Metrics()
    dispatcher = Dispatcher(configure(slack_webhook_url=SLACK), metrics, cluster_id="c")
    for _ in range(QUEUE_CAPACITY + 2):
        dispatcher.submit(message())
    assert metrics.alerts[("slack", "dropped")] == 2


class _Handler(BaseHTTPRequestHandler):
    responses: dict[str, tuple[int, dict[str, str], bytes]] = {}
    received: list[tuple[str, dict]] = []

    def do_POST(self) -> None:
        length = int(self.headers["Content-Length"])
        self.received.append((self.path, json.loads(self.rfile.read(length))))
        status, headers, body = self.responses[self.path]
        self.send_response(status)
        for key, value in headers.items():
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args: object) -> None:
        pass


@pytest.fixture
def server() -> Iterator[str]:
    httpd = HTTPServer(("127.0.0.1", 0), _Handler)
    _Handler.received = []
    _Handler.responses = {
        "/ok": (200, {}, b"ok"),
        "/redirect": (302, {"Location": "/ok"}, b""),
        "/slow-down": (429, {"Retry-After": "7"}, b""),
        "/telegram-429": (429, {}, b'{"ok":false,"parameters":{"retry_after":11}}'),
    }
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{httpd.server_port}"
    httpd.shutdown()


def test_post_json_sends_json_and_reads_the_status(server):
    assert post_json(f"{server}/ok", {"text": "hi"}, 5) == Reply(200)
    assert _Handler.received == [("/ok", {"text": "hi"})]


def test_post_json_does_not_follow_a_redirect(server):
    assert post_json(f"{server}/redirect", {}, 5).status == 302
    assert [path for path, _ in _Handler.received] == ["/redirect"]


def test_post_json_reads_retry_after_from_the_header_or_telegrams_body(server):
    assert post_json(f"{server}/slow-down", {}, 5) == Reply(429, 7.0)
    assert post_json(f"{server}/telegram-429", {}, 5) == Reply(429, 11.0)


def test_post_json_never_raises_on_a_dead_endpoint():
    assert post_json("http://127.0.0.1:9/", {}, 1) == Reply(None)


# ── T-18.6: credentials never reach a log line ──────────────────────────────────────────────


def test_no_url_or_token_is_logged_on_success_or_failure(caplog):
    formatter = JsonFormatter()
    with caplog.at_level(logging.DEBUG):
        deliver(Reply(200))
        deliver(Reply(500), Reply(None), Reply(503))
        deliver(Reply(403))

        dispatcher = Dispatcher(
            configure(telegram_bot_token=TOKEN, telegram_chat_id="1"),
            Metrics(),
            cluster_id="c",
            post=lambda *a: (_ for _ in ()).throw(RuntimeError(f"boom {a[0]}")),
        )

        async def one_pass() -> None:
            dispatcher.submit(message())
            task = asyncio.create_task(dispatcher.run())
            await asyncio.sleep(0.1)
            task.cancel()

        asyncio.run(one_pass())

    logged = "\n".join(formatter.format(r) for r in caplog.records)
    assert "alert delivery raised" in logged and "alert sent" in logged
    assert "rejected by the channel" in logged and "not delivered" in logged
    for secret in ("SECRET", "hooks.slack.com/services", TOKEN):
        assert secret not in logged


# ── T-18.7: metrics ─────────────────────────────────────────────────────────────────────────


def test_metrics_expose_every_outcome_for_each_configured_channel():
    metrics = Metrics()
    Dispatcher(
        configure(slack_webhook_url=SLACK, telegram_bot_token=TOKEN, telegram_chat_id="1"),
        metrics,
        cluster_id="c",
    )
    text = metrics.render(stored_buckets=0)
    for channel in ("slack", "telegram"):
        for outcome in ("sent", "failed", "dropped"):
            assert (
                f'topology_backend_alerts_total{{channel="{channel}",outcome="{outcome}"}} 0\n'
                in (text)
            )
    assert 'channel="teams"' not in text


def test_metrics_have_no_alert_series_when_alerts_are_off():
    assert "topology_backend_alerts_total" not in Metrics().render(stored_buckets=0)
