-- RedstoneAPI Marketplace Migration
-- User-to-user trading marketplace with encrypted content delivery
-- Migration number: 9001 (Redstone namespace)

-- ============================================================================
-- Seller System
-- ============================================================================

-- Seller status
CREATE TYPE redstone_seller_status AS ENUM ('active', 'frozen', 'banned');

-- Seller profiles
CREATE TABLE redstone_sellers (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,

    -- Status
    status redstone_seller_status NOT NULL DEFAULT 'active',
    is_official BOOLEAN NOT NULL DEFAULT FALSE, -- Official sellers have no service fee

    -- Quotas (based on balance)
    max_products INTEGER NOT NULL DEFAULT 3, -- Calculated from balance
    active_products_count INTEGER NOT NULL DEFAULT 0,

    -- Statistics
    total_sales INTEGER NOT NULL DEFAULT 0,
    total_revenue NUMERIC(20,8) NOT NULL DEFAULT 0,
    pending_settlement NUMERIC(20,8) NOT NULL DEFAULT 0,

    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_sellers_user ON redstone_sellers(user_id);
CREATE INDEX idx_redstone_sellers_status ON redstone_sellers(status);

-- ============================================================================
-- Products
-- ============================================================================

-- Product types
CREATE TYPE redstone_product_type AS ENUM ('text_key', 'account_credential', 'card_key', 'file');

-- Product status
CREATE TYPE redstone_product_status AS ENUM ('draft', 'pending_review', 'approved', 'rejected', 'delisted');

-- Products
CREATE TABLE redstone_products (
    id BIGSERIAL PRIMARY KEY,
    seller_id BIGINT NOT NULL REFERENCES redstone_sellers(id) ON DELETE CASCADE,

    -- Basic info
    title VARCHAR(200) NOT NULL,
    description TEXT,
    product_type redstone_product_type NOT NULL,

    -- Pricing
    price NUMERIC(20,8) NOT NULL CHECK (price >= 0), -- In platform regular balance units
    service_fee_rate NUMERIC(5,4) NOT NULL, -- Snapshot at creation time, e.g., 0.0500 for 5%

    -- Inventory
    stock INTEGER NOT NULL DEFAULT 0 CHECK (stock >= 0),
    sold_count INTEGER NOT NULL DEFAULT 0,

    -- Content (encrypted)
    encrypted_content TEXT, -- For text_key, account_credential, card_key
    encrypted_content_metadata JSONB, -- Encryption envelope: {version, algorithm, wrapped_key, iv}
    file_object_key VARCHAR(500), -- S3 object key for file type
    file_size_bytes BIGINT, -- Original file size
    file_mime_type VARCHAR(100),

    -- Security
    scan_status VARCHAR(50) NOT NULL DEFAULT 'pending', -- 'pending', 'passed', 'failed', 'skipped'
    scan_result JSONB, -- ClamAV or content moderation result
    scanned_at TIMESTAMPTZ,

    -- Status
    status redstone_product_status NOT NULL DEFAULT 'pending_review',
    approved_at TIMESTAMPTZ,
    delisted_at TIMESTAMPTZ,
    delisted_reason TEXT,

    -- Metadata
    tags TEXT[], -- Searchable tags
    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_products_seller ON redstone_products(seller_id);
CREATE INDEX idx_redstone_products_status ON redstone_products(status);
CREATE INDEX idx_redstone_products_type ON redstone_products(product_type);
CREATE INDEX idx_redstone_products_price ON redstone_products(price);
CREATE INDEX idx_redstone_products_tags ON redstone_products USING GIN(tags);

-- ============================================================================
-- Orders
-- ============================================================================

-- Order status
CREATE TYPE redstone_order_status AS ENUM (
    'pending_payment',
    'paid',
    'delivered',
    'appealed',
    'settled',
    'refunded',
    'cancelled'
);

-- Orders
CREATE TABLE redstone_orders (
    id BIGSERIAL PRIMARY KEY,
    order_number VARCHAR(50) NOT NULL UNIQUE, -- Human-readable order ID

    buyer_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    seller_id BIGINT NOT NULL REFERENCES redstone_sellers(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES redstone_products(id) ON DELETE CASCADE,

    -- Snapshot at purchase time
    product_title VARCHAR(200) NOT NULL,
    product_type redstone_product_type NOT NULL,
    unit_price NUMERIC(20,8) NOT NULL,
    quantity INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0),
    subtotal NUMERIC(20,8) NOT NULL,
    service_fee NUMERIC(20,8) NOT NULL,
    seller_net NUMERIC(20,8) NOT NULL, -- subtotal - service_fee

    -- Payment
    paid_at TIMESTAMPTZ,
    payment_transaction_id BIGINT REFERENCES redstone_wallet_transactions(id),

    -- Delivery
    delivered_at TIMESTAMPTZ,
    delivery_content TEXT, -- Decrypted content or download link (ephemeral, logged separately)
    delivery_viewed_at TIMESTAMPTZ,
    delivery_view_count INTEGER NOT NULL DEFAULT 0, -- Should be 0 or 1 for one-time delivery

    -- Settlement
    settlement_deadline TIMESTAMPTZ, -- paid_at + 24 hours
    settled_at TIMESTAMPTZ,
    settlement_transaction_id BIGINT REFERENCES redstone_wallet_transactions(id),

    -- Status
    status redstone_order_status NOT NULL DEFAULT 'pending_payment',

    -- Metadata
    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_orders_buyer ON redstone_orders(buyer_id, created_at DESC);
CREATE INDEX idx_redstone_orders_seller ON redstone_orders(seller_id, created_at DESC);
CREATE INDEX idx_redstone_orders_product ON redstone_orders(product_id);
CREATE INDEX idx_redstone_orders_status ON redstone_orders(status);
CREATE INDEX idx_redstone_orders_settlement ON redstone_orders(settlement_deadline) WHERE status = 'delivered';
CREATE INDEX idx_redstone_orders_number ON redstone_orders(order_number);

-- ============================================================================
-- Appeals & Reports
-- ============================================================================

-- Appeal status
CREATE TYPE redstone_appeal_status AS ENUM ('pending', 'approved', 'rejected', 'withdrawn');

-- Order appeals (buyer disputes)
CREATE TABLE redstone_order_appeals (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES redstone_orders(id) ON DELETE CASCADE,
    buyer_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    reason TEXT NOT NULL,
    evidence JSONB, -- Screenshots, descriptions, etc.

    -- Admin resolution
    status redstone_appeal_status NOT NULL DEFAULT 'pending',
    resolved_at TIMESTAMPTZ,
    resolved_by BIGINT REFERENCES users(id),
    resolution_note TEXT,
    refund_amount NUMERIC(20,8),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_order_appeals_order ON redstone_order_appeals(order_id);
CREATE INDEX idx_redstone_order_appeals_status ON redstone_order_appeals(status);

-- Product reports (user reports)
CREATE TABLE redstone_product_reports (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES redstone_products(id) ON DELETE CASCADE,
    reporter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    reason VARCHAR(100) NOT NULL, -- 'fraud', 'inappropriate', 'spam', 'copyright', 'other'
    description TEXT,
    evidence JSONB,

    -- Admin resolution
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- 'pending', 'investigating', 'resolved', 'dismissed'
    resolved_at TIMESTAMPTZ,
    resolved_by BIGINT REFERENCES users(id),
    resolution_note TEXT,
    action_taken VARCHAR(100), -- 'delisted', 'seller_warned', 'seller_banned', 'no_action'

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_product_reports_product ON redstone_product_reports(product_id);
CREATE INDEX idx_redstone_product_reports_status ON redstone_product_reports(status);
CREATE INDEX idx_redstone_product_reports_reporter ON redstone_product_reports(reporter_id);

-- ============================================================================
-- Delivery Audit Log
-- ============================================================================

-- Audit log for content access (for security and compliance)
CREATE TABLE redstone_delivery_audit_log (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES redstone_orders(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    action VARCHAR(50) NOT NULL, -- 'view', 'download'
    ip_address INET,
    user_agent TEXT,

    -- Do NOT log actual content, only metadata
    content_hash VARCHAR(64), -- SHA256 of delivered content for verification
    file_size_bytes BIGINT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_delivery_audit_order ON redstone_delivery_audit_log(order_id, created_at DESC);
CREATE INDEX idx_redstone_delivery_audit_user ON redstone_delivery_audit_log(user_id, created_at DESC);

-- ============================================================================
-- Migration Metadata
-- ============================================================================

COMMENT ON TABLE redstone_sellers IS 'User sellers with quota management';
COMMENT ON TABLE redstone_products IS 'Marketplace products with encrypted content';
COMMENT ON TABLE redstone_orders IS 'Order lifecycle from payment to settlement';
COMMENT ON TABLE redstone_order_appeals IS 'Buyer dispute resolution';
COMMENT ON TABLE redstone_product_reports IS 'User reports for inappropriate products';
COMMENT ON TABLE redstone_delivery_audit_log IS 'Audit trail for content delivery (security compliance)';
