"""`python -m app.alerts test`: one message to every configured channel (ADR-018 D-18.8).

Run inside the backend pod (`make alerts-test`), where the settings and the egress path are the
real ones. Exits 0 only if every channel accepted the message.
"""

from __future__ import annotations

import asyncio
import sys

from app.alerts import messages
from app.alerts.dispatch import from_settings
from app.api.dependencies import utc_now
from app.api.logging import configure_logging
from app.api.metrics import Metrics
from app.settings import settings


async def _send_test() -> int:
    dispatcher = from_settings(settings, Metrics())
    if dispatcher is None:
        print("alerts are off (ALERTS_ENABLED is false); nothing to test", file=sys.stderr)
        return 2
    message = messages.working_message(
        cluster_id=settings.cluster_id, at=utc_now(), link=settings.alert_ui_url
    )
    failed = 0
    for channel in dispatcher.channels:
        ok = await dispatcher.deliver(channel, message)
        print(f"{channel.name}: {'sent' if ok else 'FAILED (see the log line above)'}")
        failed += not ok
    return 1 if failed else 0


def main(argv: list[str]) -> int:
    if argv != ["test"]:
        print("usage: python -m app.alerts test", file=sys.stderr)
        return 2
    configure_logging()
    try:
        return asyncio.run(_send_test())
    except ValueError as error:
        # A configuration error names the setting, never its value (channels.py).
        print(f"alerts misconfigured: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
