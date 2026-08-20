"""Environment-driven configuration.

Variable names are fixed by ADR-001 §5.7 and templated by the Helm chart (ADR-007 D-7.5).
"""

from __future__ import annotations

from typing import Annotated

from pydantic import field_validator
from pydantic_settings import BaseSettings, NoDecode, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    # Identity. Must match the agent's CLUSTER_ID exactly — see contracts/ids.md §1.
    cluster_id: str = "kind-topology"

    # Storage. Empty selects the in-memory adapter (Phase 2 only, never deployed from Phase 3).
    database_url: str = ""
    retention_hours: int = 24

    # Response caps. Exceeding these truncates and sets the indicator (ADR-004 D-4.4).
    graph_max_nodes: int = 500
    graph_max_edges: int = 2000

    # Ingestion limits (ADR-003 D-3.4).
    max_batch_edges: int = 5000
    max_request_bytes: int = 5 * 1024 * 1024

    # Query limits (ADR-003 D-3.6).
    max_query_span_hours: int = 24

    # Diff (ADR-003 D-3.7).
    topology_diff_change_threshold_percent: float = 20.0

    # NoDecode is required, not decorative: without it EnvSettingsSource runs json.loads on the
    # raw env var before any validator sees it, so a comma-separated value raises during
    # settings construction and the process never starts.
    cors_allowed_origins: Annotated[list[str], NoDecode] = ["http://localhost:5173"]

    @field_validator("cors_allowed_origins", mode="before")
    @classmethod
    def _split_origins(cls, value: object) -> object:
        """Accept a comma-separated list, which is what the ConfigMap emits.

        pydantic-settings parses a complex-typed field from the environment as JSON, so
        CORS_ALLOWED_ORIGINS="http://a,http://b" raises rather than splitting. Requiring
        operators to write a JSON array in a ConfigMap would be a poor trade — and this failed
        only in the container, because local runs used the default and never exercised the
        env-var path.
        """
        if isinstance(value, str):
            return [origin.strip() for origin in value.split(",") if origin.strip()]
        return value

    # The only schema version this release accepts. Anything else is 400, not 422.
    supported_schema_version: int = 1


settings = Settings()
