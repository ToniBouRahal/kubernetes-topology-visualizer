"""What each backend listener is allowed to serve (ADR-014 D-14.3).

The backend runs three listeners over one application. Each is wrapped so it answers only its own
paths; anything else is a 404 on that listener, never a quiet pass-through. Together with one
client CA per mTLS listener, this is the authorisation model: the ingest listener only completes a
handshake with an agent certificate AND only serves the ingest path, so neither a frontend
certificate nor a request routed to the wrong port can write topology.
"""

from __future__ import annotations

import json
from collections.abc import Callable
from dataclasses import dataclass

from starlette.types import ASGIApp, Receive, Scope, Send

INGEST_PATH = "/api/v1/ingest/batches"
LISTENER_SCOPE_KEY = "topology.listener"


@dataclass(frozen=True)
class Listener:
    name: str
    allows: Callable[[str, str], bool]
    """(method, path) -> may this listener serve it."""


INGEST = Listener("ingest", lambda method, path: method == "POST" and path == INGEST_PATH)

API = Listener(
    "api",
    lambda method, path: (
        method in ("GET", "HEAD")
        and path.startswith("/api/v1/")
        and not path.startswith("/api/v1/ingest/")
    ),
)

OPS = Listener(
    "ops",
    lambda method, path: (
        method in ("GET", "HEAD") and path in ("/health/live", "/health/ready", "/metrics")
    ),
)

# Development and tests: no TLS configured, one listener, everything (ADR-014 D-14.1).
ALL = Listener("all", lambda method, path: True)


def restrict(app: ASGIApp, listener: Listener) -> ASGIApp:
    """`app`, answering only what `listener` allows. Lifespan events always pass through."""

    async def restricted(scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http" or listener.allows(scope["method"], scope["path"]):
            # Which listener this arrived on, for what the application may trust about it: only
            # the API listener's one client (nginx) sets the signed-in user's header.
            scope[LISTENER_SCOPE_KEY] = listener.name
            await app(scope, receive, send)
            return
        # The error envelope's shape (errors.py), without a request id: this answer is given
        # before the request reaches the middleware that assigns one.
        body = json.dumps(
            {"error": "not_found", "detail": f"not served on the {listener.name} listener"}
        ).encode()
        await send(
            {
                "type": "http.response.start",
                "status": 404,
                "headers": [
                    (b"content-type", b"application/json"),
                    (b"content-length", str(len(body)).encode()),
                ],
            }
        )
        await send({"type": "http.response.body", "body": body})

    return restricted
