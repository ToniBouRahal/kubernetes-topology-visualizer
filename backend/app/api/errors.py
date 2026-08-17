"""Error handling.

One stable envelope for every failure, carrying the request id so a user-visible error can be
tied to a log line.

The hard rule: a response never carries a stack trace, a DSN, or a credential (ADR-004 D-4.6,
test T-4.11). Unhandled exceptions are logged in full for the operator and reduced to a generic
message for the client — SQLAlchemy connection errors in particular embed the DSN in their string
representation, so returning `str(exc)` would leak the database password to anyone who could
trigger a failure.
"""

from __future__ import annotations

import logging

from fastapi import FastAPI, HTTPException, Request, status
from fastapi.encoders import jsonable_encoder
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from app.domain.models import ErrorResponse
from app.domain.window import WindowError

log = logging.getLogger("api.error")


def _envelope(request: Request, status_code: int, error: str, detail: str) -> JSONResponse:
    return JSONResponse(
        status_code=status_code,
        content=jsonable_encoder(
            ErrorResponse(
                error=error,
                detail=detail,
                request_id=getattr(request.state, "request_id", None),
            )
        ),
    )


def register(app: FastAPI) -> None:
    @app.exception_handler(HTTPException)
    async def _http_error(request: Request, exc: HTTPException) -> JSONResponse:
        return _envelope(
            request,
            exc.status_code,
            error=_slug(exc.status_code),
            detail=str(exc.detail),
        )

    @app.exception_handler(RequestValidationError)
    async def _validation_error(request: Request, exc: RequestValidationError) -> JSONResponse:
        # Field-level detail is safe and useful: it tells an agent operator exactly which field
        # of their batch was wrong, which is the difference between a fixable 422 and a mystery.
        problems = "; ".join(
            f"{'.'.join(str(p) for p in err['loc'])}: {err['msg']}" for err in exc.errors()
        )
        return _envelope(
            request,
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            error="validation_error",
            detail=problems or "request failed validation",
        )

    @app.exception_handler(WindowError)
    async def _window_error(request: Request, exc: WindowError) -> JSONResponse:
        return _envelope(
            request,
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            error="invalid_window",
            detail=str(exc),
        )

    @app.exception_handler(Exception)
    async def _unhandled(request: Request, exc: Exception) -> JSONResponse:
        # Full detail to the operator's log...
        log.exception(
            "unhandled exception",
            extra={"method": request.method, "path": request.url.path},
        )
        # ...and nothing but a correlation id to the client.
        return _envelope(
            request,
            status.HTTP_500_INTERNAL_SERVER_ERROR,
            error="internal_error",
            detail="an internal error occurred; quote the request_id when reporting it",
        )


def _slug(status_code: int) -> str:
    return {
        400: "unsupported_schema_version",
        404: "not_found",
        413: "payload_too_large",
        422: "validation_error",
        501: "not_implemented",
        503: "storage_unavailable",
    }.get(status_code, "error")
