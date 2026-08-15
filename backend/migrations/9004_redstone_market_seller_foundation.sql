-- Redstone seller-center lifecycle support. Product content and account
-- credentials remain outside this table; account_reference requires a future
-- verified user-account ownership domain and must never infer ownership from
-- accounts alone.

ALTER TABLE redstone_market_products
    ADD COLUMN IF NOT EXISTS scan_requested_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS scan_completed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS scan_failure_reason VARCHAR(160) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_redstone_market_products_seller_lifecycle
    ON redstone_market_products (seller_user_id, status, risk_status, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_redstone_market_delivery_items_product_status
    ON redstone_market_delivery_items (product_id, status, ordinal, id);

COMMENT ON COLUMN redstone_market_products.account_id IS
    'Existing accounts.id reference only. Redstone must verify user ownership through the dedicated account-upload/sharing domain; never infer it from accounts rows.';
COMMENT ON COLUMN redstone_market_products.scan_failure_reason IS
    'Short, non-sensitive scanner state only; do not persist item plaintext or scanner excerpts.';
