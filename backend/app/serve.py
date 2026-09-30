"""Process entry point: `python -m app.serve` (ADR-014 D-14.3).

Without TLS settings: one plain listener serving everything, exactly as before (development and
tests, D-14.1). With them: three listeners over the one application —

    ops     plain HTTP   /health/*, /metrics          kubelet probes, Prometheus
    ingest  mutual TLS   POST /api/v1/ingest/batches   agents (ingest-client CA only)
    api     mutual TLS   GET  /api/v1/*                the frontend (api-client CA only)

Ordering is the part that matters. The application starts on an in-memory repository and swaps
in PostgreSQL during startup, so a batch accepted before startup finished would be written to
memory and lost. The ops listener therefore runs startup, and the ingest and api listeners open
only after it has finished. Shutdown runs the other way: stop taking data first, then close the
database.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import signal
import ssl
from collections.abc import Generator

import uvicorn

from app.api.listeners import ALL, API, INGEST, OPS, Listener, restrict
from app.main import app
from app.settings import Settings, settings

log = logging.getLogger("app.serve")


class _Listener(uvicorn.Server):
    """A uvicorn server that leaves signals to the process.

    uvicorn installs its own SIGTERM handler per server with signal.signal, so with several
    servers the last one started would be the only one told to stop, and a pod would sit out its
    grace period on shutdown. The process installs one handler that stops all of them in order.
    """

    @contextlib.contextmanager
    def capture_signals(self) -> Generator[None, None, None]:
        yield


def mtls_context(cert_file: str, key_file: str, client_ca_file: str) -> ssl.SSLContext:
    """TLS 1.3 only, a client certificate required, and trusted from exactly one CA.

    TLS 1.3 alone because every client is ours — the agent (Go) and nginx both speak it — so
    there is no legacy client to keep a weaker version open for, and 1.3 has no cipher choices
    to get wrong.
    """
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.load_cert_chain(cert_file, key_file)
    context.verify_mode = ssl.CERT_REQUIRED
    context.load_verify_locations(client_ca_file)
    return context


def _server(
    listener: Listener, port: int, *, lifespan: str, ssl_context: ssl.SSLContext | None = None
) -> _Listener:
    config = uvicorn.Config(
        restrict(app, listener),
        host="0.0.0.0",  # noqa: S104 — inside a pod; the NetworkPolicy decides who reaches it
        port=port,
        lifespan=lifespan,
        # Access logging is the application's own (RequestContextMiddleware).
        access_log=False,
        ssl_context_factory=(lambda _config, _default: ssl_context) if ssl_context else None,
        # Behind nginx on the api listener; the peer address in logs is nginx either way.
        proxy_headers=False,
        server_header=False,
    )
    return _Listener(config)


def listeners(config: Settings) -> tuple[_Listener, list[_Listener]]:
    """(the listener that runs startup, the listeners that open after it)."""
    if not config.tls_enabled:
        log.warning(
            "no TLS configured: serving every path in plain HTTP on port %d. "
            "Development and tests only — a deployment sets TLS_* (ADR-014 D-14.1)",
            config.ops_port,
        )
        return _server(ALL, config.ops_port, lifespan="on"), []

    cert, key, ingest_ca, api_ca = config.tls_files
    return _server(OPS, config.ops_port, lifespan="on"), [
        _server(
            INGEST,
            config.ingest_port,
            lifespan="off",
            ssl_context=mtls_context(cert, key, ingest_ca),
        ),
        _server(API, config.api_port, lifespan="off", ssl_context=mtls_context(cert, key, api_ca)),
    ]


async def _run(server: _Listener, name: str) -> None:
    """One listener. uvicorn ends a failed startup with sys.exit(), and a SystemExit escaping a
    task tears the loop down with a traceback about an unretrieved exception; turned into an
    ordinary error here, it is reported once and the process exits 1."""
    try:
        await server.serve()
    except SystemExit as exc:
        raise RuntimeError(f"the {name} listener stopped (exit {exc.code})") from None


async def serve(config: Settings = settings, stop: asyncio.Event | None = None) -> None:
    first, rest = listeners(config)
    stop = stop or asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        with contextlib.suppress(NotImplementedError, RuntimeError, ValueError):  # not main thread
            loop.add_signal_handler(sig, stop.set)

    first_task = asyncio.create_task(_run(first, "ops"))
    while not first.started:
        if first_task.done():
            # Startup failed (the database never came up, a port was taken): nothing opens.
            log.error("startup failed; no listener opened: %s", first_task.exception())
            raise SystemExit(1)
        await asyncio.sleep(0.05)

    rest_tasks = [asyncio.create_task(_run(s, f"port {s.config.port}")) for s in rest]
    stopped = asyncio.create_task(stop.wait())
    # Any listener ending on its own is a failure; a signal is a shutdown.
    done, _ = await asyncio.wait(
        [stopped, first_task, *rest_tasks], return_when=asyncio.FIRST_COMPLETED
    )
    failed = stopped not in done
    if failed:
        for task in done:
            if task.exception():
                log.error("%s", task.exception())

    for server in rest:
        server.should_exit = True
    await asyncio.gather(*rest_tasks, return_exceptions=True)
    first.should_exit = True
    await asyncio.gather(first_task, return_exceptions=True)
    stopped.cancel()
    if failed:
        raise SystemExit(1)


if __name__ == "__main__":
    asyncio.run(serve())
