-- Initial schema. ADR-005 D-5.2.
--
-- Migrations are plain SQL applied in filename order inside a transaction, with each applied
-- version recorded. Forward-only: there are no down migrations, because rolling a schema
-- backwards over data that has already been written is not a recovery strategy.

CREATE TABLE IF NOT EXISTS ingest_batches (
    batch_id    TEXT PRIMARY KEY,
    cluster_id  TEXT        NOT NULL,
    agent_id    TEXT        NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Retention deletes by age, and the batch table is pruned alongside the buckets.
CREATE INDEX IF NOT EXISTS ingest_batches_received_at_idx
    ON ingest_batches (received_at);

CREATE TABLE IF NOT EXISTS nodes (
    id              TEXT PRIMARY KEY,
    cluster_id      TEXT        NOT NULL,
    -- The six allowed kinds, enforced by the database as well as by the API. Two independent
    -- layers deliberately: a future code path that forgets to validate still cannot store an
    -- out-of-contract kind (contracts/ids.md §1).
    kind            TEXT        NOT NULL
                    CHECK (kind IN ('Service','Deployment','StatefulSet','DaemonSet','Job','Pod')),
    namespace       TEXT        NULL,   -- NULL only for external:EXTERNAL
    name            TEXT        NOT NULL,
    label           TEXT        NOT NULL,
    attributes_json JSONB       NOT NULL DEFAULT '{}'::jsonb,
    first_seen      TIMESTAMPTZ NOT NULL,
    last_seen       TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS nodes_cluster_namespace_kind_idx
    ON nodes (cluster_id, namespace, kind);

-- Case-insensitive label search backs the `query` filter without a sequential scan.
CREATE INDEX IF NOT EXISTS nodes_label_lower_idx
    ON nodes (lower(label));

CREATE TABLE IF NOT EXISTS edge_buckets (
    bucket_start     TIMESTAMPTZ NOT NULL,   -- truncated to the minute, UTC
    cluster_id       TEXT        NOT NULL,
    source_id        TEXT        NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    target_id        TEXT        NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    protocol         TEXT        NOT NULL CHECK (protocol IN ('TCP')),
    destination_port INTEGER     NOT NULL CHECK (destination_port BETWEEN 1 AND 65535),
    connection_count BIGINT      NOT NULL CHECK (connection_count >= 0),
    -- NULL until the Phase 4 byte-accounting gate passes. NULL and 0 mean different things:
    -- "not measured" versus "measured as zero" (contracts/ids.md §10).
    bytes_sent       BIGINT      NULL CHECK (bytes_sent IS NULL OR bytes_sent >= 0),
    bytes_received   BIGINT      NULL CHECK (bytes_received IS NULL OR bytes_received >= 0),
    first_seen       TIMESTAMPTZ NOT NULL,
    last_seen        TIMESTAMPTZ NOT NULL,

    -- The edge key, with the bucket prepended. This tuple is the same one the agent aggregates
    -- on and the graph query groups by — asserted by T-3.4.
    PRIMARY KEY (bucket_start, cluster_id, source_id, target_id, protocol, destination_port)
);

-- Every graph and diff query is a bounded bucket_start range scan; this index carries the
-- 500 ms p95 target for 500 nodes / 2,000 edges (ADR-001 §6).
CREATE INDEX IF NOT EXISTS edge_buckets_cluster_time_idx
    ON edge_buckets (cluster_id, bucket_start DESC);

-- Node detail views walk edges from one endpoint.
CREATE INDEX IF NOT EXISTS edge_buckets_source_time_idx
    ON edge_buckets (source_id, bucket_start DESC);

CREATE INDEX IF NOT EXISTS edge_buckets_target_time_idx
    ON edge_buckets (target_id, bucket_start DESC);
