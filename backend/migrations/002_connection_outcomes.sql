-- Historical failure measurement remains NULL; timing has no historical samples.
ALTER TABLE edge_buckets
    ADD COLUMN failed_connection_count BIGINT NULL CHECK (failed_connection_count >= 0),
    ADD COLUMN connect_latency_count BIGINT NOT NULL DEFAULT 0 CHECK (connect_latency_count >= 0),
    ADD COLUMN connect_latency_sum_us BIGINT NOT NULL DEFAULT 0 CHECK (connect_latency_sum_us >= 0),
    ADD CONSTRAINT edge_buckets_outcome_present
        CHECK (connection_count > 0 OR COALESCE(failed_connection_count, 0) > 0),
    ADD CONSTRAINT edge_buckets_latency_samples
        CHECK (connect_latency_count <= connection_count),
    ADD CONSTRAINT edge_buckets_latency_sum
        CHECK (connect_latency_count > 0 OR connect_latency_sum_us = 0);
