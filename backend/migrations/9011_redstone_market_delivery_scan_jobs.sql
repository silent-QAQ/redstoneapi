-- Encrypted delivery scans are coordinated separately from uploads. Plaintext
-- is never persisted: workers decrypt a claimed private object only in memory,
-- scan it locally, clear the buffer, then persist only the verdict.

CREATE TABLE IF NOT EXISTS redstone_market_delivery_scan_jobs (
    delivery_item_id BIGINT PRIMARY KEY REFERENCES redstone_market_delivery_items(id) ON DELETE RESTRICT,
    product_id BIGINT NOT NULL REFERENCES redstone_market_products(id) ON DELETE RESTRICT,
    state VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'processing', 'passed', 'rejected')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token UUID NULL,
    lease_expires_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_delivery_scan_jobs_claim
    ON redstone_market_delivery_scan_jobs (state, available_at, delivery_item_id);

CREATE OR REPLACE FUNCTION redstone_market_validate_delivery_scan_job()
RETURNS TRIGGER AS $$
DECLARE
    item_product_id BIGINT;
BEGIN
    SELECT product_id INTO item_product_id
    FROM redstone_market_delivery_items WHERE id = NEW.delivery_item_id;
    IF NOT FOUND OR item_product_id <> NEW.product_id THEN
        RAISE EXCEPTION 'redstone marketplace delivery scan job product does not match delivery item';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_validate_delivery_scan_job ON redstone_market_delivery_scan_jobs;
CREATE TRIGGER trg_redstone_market_validate_delivery_scan_job
    BEFORE INSERT OR UPDATE OF delivery_item_id, product_id ON redstone_market_delivery_scan_jobs
    FOR EACH ROW EXECUTE FUNCTION redstone_market_validate_delivery_scan_job();

-- Upgrade deployments that had an in-flight synchronous upload before this
-- queue existed. Only pending encrypted products are backfilled; completed
-- active products never re-enter the scanner unnecessarily.
INSERT INTO redstone_market_delivery_scan_jobs (delivery_item_id, product_id, state, available_at)
SELECT di.id, di.product_id, 'pending', NOW()
FROM redstone_market_delivery_items di
JOIN redstone_market_products p ON p.id = di.product_id
WHERE p.status = 'pending_scan'
  AND di.account_id IS NULL
  AND di.encrypted_object_key IS NOT NULL
  AND di.wrapped_dek IS NOT NULL
ON CONFLICT (delivery_item_id) DO NOTHING;
