# Backend image.
#
# Runs as a non-root user with a read-only root filesystem in the chart (ADR-001 §6), so
# everything it needs must be baked in at build time — nothing is written at runtime.

FROM python:3.13-slim-bookworm@sha256:2325bb286ec344af3e5898cc224b5844e2707ac6e26b1632516fd3edc84a5e26 AS builder

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
FROM python:3.13-slim-bookworm@sha256:2325bb286ec344af3e5898cc224b5844e2707ac6e26b1632516fd3edc84a5e26

# A fixed uid so the chart's runAsUser matches, and no shell for the account.
#
# pip is removed in the same layer. The application runs from /opt/venv, which uv built in the
# builder stage, so pip is never used at runtime — but it ships a vendored copy of msgpack and
# other libraries that an image scan reports as vulnerabilities in this image. They were the only
# fixable HIGH findings against the backend, and they are fixable by deletion: code that is not
# there cannot be exploited, and nothing in the runtime path referenced it.
RUN groupadd --gid 10001 topology \
    && useradd --uid 10001 --gid topology --no-create-home --shell /usr/sbin/nologin topology \
    && python -m pip uninstall -y pip setuptools wheel 2>/dev/null || true \
    && rm -rf /usr/local/lib/python3.13/site-packages/pip \
              /usr/local/lib/python3.13/site-packages/pip-* \
              /usr/local/lib/python3.13/site-packages/setuptools \
              /usr/local/lib/python3.13/site-packages/setuptools-* \
              /usr/local/lib/python3.13/site-packages/pkg_resources \
              /usr/local/bin/pip /usr/local/bin/pip3 /usr/local/bin/pip3.13

COPY --from=builder /opt/venv /opt/venv
COPY --from=builder /build/app /srv/app
COPY --from=builder /build/migrations /srv/migrations

ENV PATH="/opt/venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

WORKDIR /srv
USER 10001

# ops (plain: probes, /metrics) · ingest (mTLS, agents) · api (mTLS, the frontend) — ADR-014 D-14.3.
# Without TLS_* set the process serves everything on 8000 in plain HTTP, as it always has.
EXPOSE 8000 8443 8444

ENTRYPOINT ["python", "-m", "app.serve"]
