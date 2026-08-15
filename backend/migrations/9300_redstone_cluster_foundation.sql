-- Redstone cluster control plane. PostgreSQL is the source of truth for
-- membership, task leases and cache invalidation epochs; Redis is optional.

CREATE TABLE IF NOT EXISTS redstone_cluster_nodes (
    node_id TEXT PRIMARY KEY,
    state TEXT NOT NULL DEFAULT 'active'
        CHECK (state IN ('active', 'draining', 'offline')),
    advertise_url TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    drain_requested_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_cluster_nodes_liveness
    ON redstone_cluster_nodes (state, last_heartbeat_at DESC);

CREATE TABLE IF NOT EXISTS redstone_cluster_task_leases (
    task_name TEXT PRIMARY KEY,
    holder_node_id TEXT NOT NULL,
    lease_token UUID NOT NULL,
    fencing_token BIGINT NOT NULL DEFAULT 1,
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_cluster_task_leases_expiry
    ON redstone_cluster_task_leases (lease_until);

CREATE TABLE IF NOT EXISTS redstone_cluster_cache_epochs (
    cache_name TEXT PRIMARY KEY,
    epoch BIGINT NOT NULL DEFAULT 1 CHECK (epoch > 0),
    invalidated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE redstone_cluster_task_leases IS
    'Fenced leases for singleton workers. A stale holder cannot renew or release a newer lease token.';
COMMENT ON TABLE redstone_cluster_cache_epochs IS
    'Monotonic cache namespaces. Consumers include epoch in their local or Redis cache key.';
