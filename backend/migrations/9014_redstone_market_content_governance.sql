-- Marketplace-local content moderation. This table stores only stable finding
-- codes and SHA-256 digests, never raw delivery plaintext or account credentials.

CREATE TABLE IF NOT EXISTS redstone_market_content_reviews (
    id BIGSERIAL PRIMARY KEY,
    seller_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    product_id BIGINT NULL REFERENCES redstone_market_products(id) ON DELETE RESTRICT,
    delivery_item_id BIGINT NULL REFERENCES redstone_market_delivery_items(id) ON DELETE RESTRICT,
    scope VARCHAR(32) NOT NULL CHECK (scope IN ('product_metadata', 'delivery_content', 'account_reference')),
    verdict VARCHAR(24) NOT NULL CHECK (verdict IN ('passed', 'manual_review', 'rejected')),
    review_state VARCHAR(16) NOT NULL CHECK (review_state IN ('open', 'closed', 'resolved')),
    finding_codes JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(finding_codes) = 'array'),
    content_sha256 CHAR(64) NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    resolution VARCHAR(32) NOT NULL DEFAULT '',
    resolution_note VARCHAR(500) NOT NULL DEFAULT '',
    resolved_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ NULL,
    CHECK ((review_state = 'open' AND resolved_at IS NULL AND resolved_by_user_id IS NULL)
        OR (review_state IN ('closed', 'resolved') AND resolved_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_content_reviews_open
    ON redstone_market_content_reviews (created_at ASC, id ASC)
    WHERE review_state = 'open';
CREATE INDEX IF NOT EXISTS idx_redstone_market_content_reviews_product
    ON redstone_market_content_reviews (product_id, created_at DESC, id DESC)
    WHERE product_id IS NOT NULL;

-- Content-review holds use the existing marketplace settlement hold mechanism.
ALTER TABLE redstone_market_order_holds
    DROP CONSTRAINT IF EXISTS redstone_market_order_holds_source_check;
ALTER TABLE redstone_market_order_holds
    ADD CONSTRAINT redstone_market_order_holds_source_check
    CHECK (source IN ('seller_freeze', 'report', 'content_review'));

COMMENT ON TABLE redstone_market_content_reviews IS
    'Marketplace content moderation decisions and human review state. Stores finding codes and digests only; never raw delivery or credentials.';
