# Backend image.
#
# Runs as a non-root user with a read-only root filesystem in the chart (ADR-001 §6), so
# everything it needs must be baked in at build time — nothing is written at runtime.

FROM python:3.13-slim-bookworm AS builder

# uv resolves and installs far faster than pip, and is already the documented local toolchain,
# so the image and the developer environment stay in step.
COPY --from=ghcr.io/astral-sh/uv:0.12.3 /uv /usr/local/bin/uv

WORKDIR /build
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy

# Install from pyproject.toml — never a hardcoded list.
#
# This previously duplicated the dependency names here, and adding asyncpg to pyproject.toml
# silently did not reach the image: the backend started, failed to import it inside the lifespan,
# and crash-looped. One source of truth is worth the slightly worse layer caching.
COPY pyproject.toml ./
COPY app ./app
# The schema ships with the image; without it migrate() has nothing to apply.
COPY migrations ./migrations
RUN uv venv /opt/venv && VIRTUAL_ENV=/opt/venv uv pip install --no-cache .

# ── Runtime ─────────────────────────────────────────────────────────────────────────────────
FROM python:3.13-slim-bookworm

# A fixed uid so the chart's runAsUser matches, and no shell for the account.
RUN groupadd --gid 10001 topology \
    && useradd --uid 10001 --gid topology --no-create-home --shell /usr/sbin/nologin topology

COPY --from=builder /opt/venv /opt/venv
COPY --from=builder /build/app /srv/app
COPY --from=builder /build/migrations /srv/migrations

ENV PATH="/opt/venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

WORKDIR /srv
USER 10001

EXPOSE 8000

# One worker: ingestion is idempotent per batch, but the in-memory adapter used in Phase 2 is
# per-process, so a second worker would serve a different half of the data. Phase 3's shared
# PostgreSQL removes that constraint.
ENTRYPOINT ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1"]
