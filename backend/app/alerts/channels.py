"""The three channels: where each sends, and how it renders a message safely (ADR-018 D-18.1,
D-18.2, D-18.6).

Workload names come from whoever can create workloads (ADR-015 §3). Every channel renders them so
they cannot become markup: no Telegram parse mode, Slack's three control characters escaped and
formatting off, Teams Markdown escaped inside separate text blocks.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Literal
from urllib.parse import urlsplit

from app.alerts.messages import Message

ChannelName = Literal["slack", "teams", "telegram"]

TELEGRAM_API = "https://api.telegram.org"
# Telegram rejects a longer message outright rather than truncating it.
TELEGRAM_MAX_TEXT = 4096

_SEVERITY_MARK = {"critical": "🚨", "warning": "⚠️"}


@dataclass(frozen=True, slots=True)
class Channel:
    name: ChannelName
    # A credential: posting to it is the only thing anyone needs to send to the channel. Never
    # logged, never part of a repr (D-18.2).
    url: str = ""
    # Telegram only. Not a secret: without the bot token it addresses nothing.
    chat_id: str = ""

    def __repr__(self) -> str:
        return f"Channel(name={self.name!r})"

    def body(self, message: Message) -> dict:
        if self.name == "slack":
            return slack_body(message)
        if self.name == "teams":
            return teams_body(message)
        return telegram_body(message, self.chat_id)


def _https(url: str, *, setting: str) -> str:
    parts = urlsplit(url)
    if parts.scheme != "https" or not parts.hostname:
        # Name the setting, never echo the value: it is the credential.
        raise ValueError(f"{setting} must be an https:// URL")
    return parts.hostname


def channels_from(
    *,
    enabled: bool,
    slack_webhook_url: str,
    teams_webhook_url: str,
    telegram_bot_token: str,
    telegram_chat_id: str,
) -> tuple[Channel, ...]:
    """Validate the configuration once, at start-up; a mistake stops the process (D-18.1)."""
    if not enabled:
        return ()

    channels: list[Channel] = []
    if slack_webhook_url:
        host = _https(slack_webhook_url, setting="ALERT_SLACK_WEBHOOK_URL")
        if host != "hooks.slack.com":
            raise ValueError("ALERT_SLACK_WEBHOOK_URL must be a https://hooks.slack.com/ URL")
        channels.append(Channel("slack", slack_webhook_url))
    if teams_webhook_url:
        # Workflows hosts vary by tenant and region, so only the scheme is checked.
        _https(teams_webhook_url, setting="ALERT_TEAMS_WEBHOOK_URL")
        channels.append(Channel("teams", teams_webhook_url))
    if bool(telegram_bot_token) != bool(telegram_chat_id):
        raise ValueError("ALERT_TELEGRAM_BOT_TOKEN and ALERT_TELEGRAM_CHAT_ID must be set together")
    if telegram_bot_token:
        # A token is `<digits>:<letters, digits, - and _>`. Checked because it becomes a URL path.
        if not re.fullmatch(r"[0-9]+:[A-Za-z0-9_-]+", telegram_bot_token):
            raise ValueError("ALERT_TELEGRAM_BOT_TOKEN is not a bot token")
        channels.append(
            Channel(
                "telegram",
                f"{TELEGRAM_API}/bot{telegram_bot_token}/sendMessage",
                telegram_chat_id,
            )
        )

    if not channels:
        raise ValueError(
            "ALERTS_ENABLED is true but no channel is configured: set a Slack or Teams webhook "
            "URL, or a Telegram bot token and chat id"
        )
    return tuple(channels)


# ── Slack ───────────────────────────────────────────────────────────────────────────────────


def _slack_escape(text: str) -> str:
    """The three characters Slack requires escaped; with them gone, a name cannot open a link
    or a `<!channel>` mention."""
    return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def slack_body(message: Message) -> dict:
    text = f"{_SEVERITY_MARK[message.severity]} {message.plain_text()}"
    # mrkdwn off: names cannot turn into bold, code or strike-through either.
    return {"text": _slack_escape(text), "mrkdwn": False}


# ── Microsoft Teams (Workflows webhook, Adaptive Card) ──────────────────────────────────────

# The inline Markdown Adaptive Cards render: emphasis, code, strike-through and `[text](url)`
# links. Block syntax (lists, headings) only acts at the start of a line, and every line starts
# with this module's own text, never a name.
_TEAMS_MARKDOWN = re.compile(r"([\\`*_~\[\]])")


def _teams_escape(text: str) -> str:
    return _TEAMS_MARKDOWN.sub(r"\\\1", text)


def teams_body(message: Message) -> dict:
    blocks: list[dict] = [
        {
            "type": "TextBlock",
            "text": _teams_escape(f"{_SEVERITY_MARK[message.severity]} {message.title}"),
            "weight": "Bolder",
            "size": "Medium",
            "wrap": True,
        },
        *(
            {"type": "TextBlock", "text": _teams_escape(line), "wrap": True}
            for line in message.lines
        ),
        {
            "type": "TextBlock",
            "text": _teams_escape(message.footer),
            "isSubtle": True,
            "size": "Small",
            "wrap": True,
        },
    ]
    card: dict = {
        "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
        "type": "AdaptiveCard",
        "version": "1.4",
        "body": blocks,
    }
    if message.link:
        # The operator's own URL from the chart, not cluster data.
        card["actions"] = [
            {"type": "Action.OpenUrl", "title": "Open topology", "url": message.link}
        ]
    return {
        "type": "message",
        "attachments": [
            {"contentType": "application/vnd.microsoft.card.adaptive", "content": card}
        ],
    }


# ── Telegram ────────────────────────────────────────────────────────────────────────────────


def telegram_body(message: Message, chat_id: str) -> dict:
    text = f"{_SEVERITY_MARK[message.severity]} {message.plain_text()}"
    if len(text) > TELEGRAM_MAX_TEXT:
        text = text[: TELEGRAM_MAX_TEXT - 1] + "…"
    # No parse_mode: the text is shown exactly as sent, so a name cannot become markup.
    return {"chat_id": chat_id, "text": text, "link_preview_options": {"is_disabled": True}}
