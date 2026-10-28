"""Delivery: one bounded queue, one task, best-effort (ADR-018 D-18.3 – D-18.5).

Ingest and the evaluator only ever put a message on the queue; neither waits on a network call.
A message that cannot be delivered is counted and logged, and the finding is still in the API,
the log and the gauges. Nothing here is persisted.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import time
import urllib.error
import urllib.request
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from datetime import datetime

from app.alerts import messages
from app.alerts.channels import Channel, channels_from
from app.api.metrics import Metrics
from app.domain.models import Anomaly, AnomalySeverity, GraphNode

log = logging.getLogger("app.alerts")

QUEUE_CAPACITY = 100
ATTEMPTS = 3
BACKOFF_SECONDS = (1.0, 4.0)
MAX_RETRY_AFTER_SECONDS = 30.0
TIMEOUT_SECONDS = 5.0
# Bounds the cooldown table: a scan touching many canaries from many sources cannot grow it
# without limit.
COOLDOWN_TABLE_LIMIT = 1024


@dataclass(frozen=True, slots=True)
class Reply:
    """What came back. `status` is None when no HTTP response arrived at all."""

    status: int | None
    retry_after: float | None = None


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """A 3xx is a failure, not a hop to somewhere the operator did not configure (D-18.2)."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


# Proxy settings from the environment (HTTPS_PROXY, NO_PROXY) still apply; certificates are
# verified against the image's CA bundle.
_opener = urllib.request.build_opener(_NoRedirect)


def _retry_after(header: str | None, body: bytes) -> float | None:
    if header:
        with contextlib.suppress(ValueError):
            return float(header)
    # Telegram states it in the body: {"parameters": {"retry_after": N}}.
    with contextlib.suppress(ValueError, TypeError, AttributeError):
        return float(json.loads(body)["parameters"]["retry_after"])
    return None


def post_json(url: str, body: dict, timeout: float) -> Reply:
    """Blocking POST, run in a worker thread. Never raises, never logs: the URL is a credential,
    and urllib's exception messages are not guaranteed to leave it out."""
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        method="POST",
        headers={"Content-Type": "application/json", "User-Agent": "topology-visualizer"},
    )
    try:
        with _opener.open(request, timeout=timeout) as response:
            response.read(65536)
            return Reply(response.status)
    except urllib.error.HTTPError as error:
        with error:
            return Reply(
                error.code, _retry_after(error.headers.get("Retry-After"), error.read(4096))
            )
    except (urllib.error.URLError, OSError, ValueError):
        return Reply(None)


Post = Callable[[str, dict, float], Reply]
Sleep = Callable[[float], Awaitable[None]]


class Dispatcher:
    def __init__(
        self,
        channels: tuple[Channel, ...],
        metrics: Metrics,
        *,
        cluster_id: str,
        min_severity: AnomalySeverity = "critical",
        canary_cooldown_seconds: float = 300.0,
        link: str = "",
        post: Post = post_json,
        sleep: Sleep = asyncio.sleep,
        monotonic: Callable[[], float] = time.monotonic,
    ) -> None:
        self.channels = channels
        self.min_severity = min_severity
        self._metrics = metrics
        self._cluster_id = cluster_id
        self._cooldown = canary_cooldown_seconds
        self._link = link
        self._post = post
        self._sleep = sleep
        self._monotonic = monotonic
        self._queue: asyncio.Queue[messages.Message] = asyncio.Queue(maxsize=QUEUE_CAPACITY)
        self._canary_sent: dict[tuple[str, str, int], float] = {}
        metrics.seed_alert_channels([c.name for c in channels])

    # ── Triggers (D-18.3, D-18.4) ───────────────────────────────────────────────────────────

    def notify_canary(
        self, *, source: str, canary: str, destination_port: int, attempts: int, at: datetime
    ) -> None:
        """Called from ingest. Once per (source, canary, port) per cooldown."""
        key = (source, canary, destination_port)
        now = self._monotonic()
        last = self._canary_sent.get(key)
        if last is not None and now - last < self._cooldown:
            return
        if len(self._canary_sent) >= COOLDOWN_TABLE_LIMIT:
            self._canary_sent = {
                k: t for k, t in self._canary_sent.items() if now - t < self._cooldown
            }
        self._canary_sent[key] = now
        self.submit(
            messages.canary_contact(
                source=source,
                canary=canary,
                destination_port=destination_port,
                attempts=attempts,
                cluster_id=self._cluster_id,
                at=at,
                link=self._link,
            )
        )

    def notify_anomalies(
        self, new: list[Anomaly], nodes: dict[str, GraphNode], *, at: datetime
    ) -> None:
        """Called by the evaluator with the anomalies that became active on this pass.

        CANARY_CONTACT is left out: ingest has already sent it, sooner.
        """
        floor = messages.SEVERITY_RANK[self.min_severity]
        found = [
            a
            for a in new
            if a.type != "CANARY_CONTACT" and messages.SEVERITY_RANK[a.severity] >= floor
        ]
        if found:
            self.submit(
                messages.anomalies(
                    found, nodes, cluster_id=self._cluster_id, at=at, link=self._link
                )
            )

    def submit(self, message: messages.Message) -> None:
        try:
            self._queue.put_nowait(message)
        except asyncio.QueueFull:
            for channel in self.channels:
                self._metrics.record_alert(channel.name, "dropped")
            log.warning("alert dropped: queue full", extra={"title": message.title})

    # ── Delivery (D-18.5) ───────────────────────────────────────────────────────────────────

    async def run(self) -> None:
        """The dispatcher task, for the life of the process."""
        while True:
            message = await self._queue.get()
            for channel in self.channels:
                try:
                    await self.deliver(channel, message)
                except asyncio.CancelledError:
                    raise
                except Exception as error:
                    # The type only: a message or traceback could carry the URL.
                    self._metrics.record_alert(channel.name, "failed")
                    log.error(
                        "alert delivery raised",
                        extra={"channel": channel.name, "error": type(error).__name__},
                    )

    async def deliver(self, channel: Channel, message: messages.Message) -> bool:
        body = channel.body(message)
        reply = Reply(None)
        for attempt in range(1, ATTEMPTS + 1):
            reply = await asyncio.to_thread(self._post, channel.url, body, TIMEOUT_SECONDS)
            if reply.status is not None and 200 <= reply.status < 300:
                self._metrics.record_alert(channel.name, "sent")
                log.info("alert sent", extra={"channel": channel.name, "attempts": attempt})
                return True
            if not _retryable(reply) or attempt == ATTEMPTS:
                break
            delay = BACKOFF_SECONDS[attempt - 1]
            if reply.retry_after is not None:
                delay = min(max(delay, reply.retry_after), MAX_RETRY_AFTER_SECONDS)
            await self._sleep(delay)

        self._metrics.record_alert(channel.name, "failed")
        extra = {
            "channel": channel.name,
            "status": reply.status if reply.status is not None else "no response",
            "attempts": attempt,
        }
        if _retryable(reply):
            log.warning("alert not delivered", extra=extra)
        else:
            # A 3xx or 4xx other than 429 does not get better by waiting: a revoked webhook, a
            # wrong chat id, a bot removed from the group.
            log.error("alert rejected by the channel; check its configuration", extra=extra)
        return False


def _retryable(reply: Reply) -> bool:
    return reply.status is None or reply.status == 429 or reply.status >= 500


def from_settings(settings, metrics: Metrics) -> Dispatcher | None:
    """The configured dispatcher, or None when alerts are off. Raises on a bad configuration,
    which stops the process at start-up (D-18.1)."""
    channels = channels_from(
        enabled=settings.alerts_enabled,
        slack_webhook_url=settings.alert_slack_webhook_url.get_secret_value(),
        teams_webhook_url=settings.alert_teams_webhook_url.get_secret_value(),
        telegram_bot_token=settings.alert_telegram_bot_token.get_secret_value(),
        telegram_chat_id=settings.alert_telegram_chat_id,
    )
    if not channels:
        return None
    return Dispatcher(
        channels,
        metrics,
        cluster_id=settings.cluster_id,
        min_severity=settings.alert_min_severity,
        canary_cooldown_seconds=settings.alert_canary_cooldown_seconds,
        link=settings.alert_ui_url,
    )
