-- Redstone user marketplace. Product delivery content is never stored in these rows;
-- it lives encrypted in an object store and is referenced by opaque object keys only.

CREATE TABLE IF NOT EXISTS redstone_market_products (
    id BIGSERIAL PRIMARY KEY,
    seller_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    seller_kind VARCHAR(16) NOT NULL DEFAULT 'user'
        CHECK (seller_kind IN ('user', 'official')),
    product_type VARCHAR(24) NOT NULL
        CHECK (product_type IN ('text_key', 'account_reference', 'card_key', 'file')),
    title VARCHAR(160) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    unit_price DECIMAL(20,8) NOT NULL CHECK (unit_price > 0),
    inventory_total INTEGER NOT NULL DEFAULT 0 CHECK (inventory_total >= 0),
    inventory_reserved INTEGER NOT NULL DEFAULT 0 CHECK (inventory_reserved >= 0),
    status VARCHAR(24) NOT NULL DEFAULT 'pending_scan'
        CHECK (status IN ('draft', 'pending_scan', 'active', 'suspended', 'sold_out', 'archived')),
    risk_status VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (risk_status IN ('pending', 'passed', 'rejected', 'flagged')),
    account_id BIGINT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ NULL,
    CHECK ((product_type = 'account_reference' AND account_id IS NOT NULL)
        OR (product_type <> 'account_reference' AND account_id IS NULL)),
    CHECK (inventory_reserved <= inventory_total)
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_products_listing
    ON redstone_market_products (status, risk_status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_redstone_market_products_seller
    ON redstone_market_products (seller_user_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS redstone_market_delivery_items (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES redstone_market_products(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    status VARCHAR(24) NOT NULL DEFAULT 'available'
        CHECK (status IN ('available', 'reserved', 'delivered', 'revoked')),
    encrypted_object_key TEXT NULL,
    key_version VARCHAR(80) NULL,
    wrapped_dek BYTEA NULL,
    content_sha256 CHAR(64) NULL,
    content_type VARCHAR(120) NULL,
    byte_size BIGINT NULL CHECK (byte_size IS NULL OR byte_size >= 0),
    account_id BIGINT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((account_id IS NOT NULL AND encrypted_object_key IS NULL AND wrapped_dek IS NULL)
        OR (account_id IS NULL AND encrypted_object_key IS NOT NULL AND wrapped_dek IS NOT NULL)),
    UNIQUE (product_id, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_delivery_items_available
    ON redstone_market_delivery_items (product_id, status, ordinal);

CREATE TABLE IF NOT EXISTS redstone_market_orders (
    id BIGSERIAL PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL UNIQUE,
    buyer_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    seller_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    product_id BIGINT NOT NULL REFERENCES redstone_market_products(id) ON DELETE RESTRICT,
    delivery_item_id BIGINT NOT NULL REFERENCES redstone_market_delivery_items(id) ON DELETE RESTRICT,
    status VARCHAR(24) NOT NULL DEFAULT 'paid'
        CHECK (status IN ('paid', 'delivered', 'appealed', 'refunded', 'settled', 'reversed', 'cancelled')),
    unit_price DECIMAL(20,8) NOT NULL CHECK (unit_price > 0),
    service_fee_rate DECIMAL(9,8) NOT NULL DEFAULT 0 CHECK (service_fee_rate >= 0 AND service_fee_rate <= 1),
    service_fee_amount DECIMAL(20,8) NOT NULL DEFAULT 0 CHECK (service_fee_amount >= 0),
    seller_net_amount DECIMAL(20,8) NOT NULL CHECK (seller_net_amount >= 0),
    settlement_due_at TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ NULL,
    settled_at TIMESTAMPTZ NULL,
    refunded_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (buyer_user_id <> seller_user_id),
    CHECK (seller_net_amount + service_fee_amount = unit_price)
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_orders_buyer
    ON redstone_market_orders (buyer_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_redstone_market_orders_seller_settlement
    ON redstone_market_orders (seller_user_id, status, settlement_due_at);
CREATE INDEX IF NOT EXISTS idx_redstone_market_orders_settlement
    ON redstone_market_orders (status, settlement_due_at);

CREATE TABLE IF NOT EXISTS redstone_market_delivery_audit (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES redstone_market_orders(id) ON DELETE RESTRICT,
    event_type VARCHAR(32) NOT NULL CHECK (event_type IN ('viewed', 'downloaded', 'revoked')),
    actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id VARCHAR(128) NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_redstone_market_delivery_one_view
    ON redstone_market_delivery_audit (order_id)
    WHERE event_type IN ('viewed', 'downloaded');

CREATE TABLE IF NOT EXISTS redstone_market_appeals (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL UNIQUE REFERENCES redstone_market_orders(id) ON DELETE RESTRICT,
    buyer_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status VARCHAR(24) NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'resolved_refund', 'resolved_release', 'closed')),
    reason TEXT NOT NULL,
    resolution_note TEXT NOT NULL DEFAULT '',
    resolved_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ NULL
);

CREATE TABLE IF NOT EXISTS redstone_market_reports (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES redstone_market_products(id) ON DELETE RESTRICT,
    reporter_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status VARCHAR(24) NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'dismissed', 'actioned')),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ NULL,
    resolved_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_reports_open
    ON redstone_market_reports (status, created_at);
