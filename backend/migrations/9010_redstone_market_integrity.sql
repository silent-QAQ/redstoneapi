-- Marketplace cross-row invariants cannot be represented by CHECK clauses.
-- Enforce them at the database boundary so every writer, including future
-- workers, retains product/order/delivery consistency and append-only audits.

CREATE OR REPLACE FUNCTION redstone_market_validate_delivery_item()
RETURNS TRIGGER AS $$
DECLARE
    expected_type VARCHAR(24);
    expected_account_id BIGINT;
BEGIN
    SELECT product_type, account_id INTO expected_type, expected_account_id
    FROM redstone_market_products WHERE id = NEW.product_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'redstone marketplace delivery item product is missing';
    END IF;
    IF expected_type = 'account_reference' THEN
        IF NEW.account_id IS NULL OR NEW.account_id <> expected_account_id THEN
            RAISE EXCEPTION 'redstone marketplace account delivery must match product account';
        END IF;
    ELSIF NEW.account_id IS NOT NULL THEN
        RAISE EXCEPTION 'redstone marketplace non-account delivery cannot reference an account';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_validate_delivery_item ON redstone_market_delivery_items;
CREATE TRIGGER trg_redstone_market_validate_delivery_item
    BEFORE INSERT OR UPDATE OF product_id, account_id ON redstone_market_delivery_items
    FOR EACH ROW EXECUTE FUNCTION redstone_market_validate_delivery_item();

CREATE OR REPLACE FUNCTION redstone_market_validate_order_integrity()
RETURNS TRIGGER AS $$
DECLARE
    expected_seller_id BIGINT;
    delivery_product_id BIGINT;
BEGIN
    SELECT seller_user_id INTO expected_seller_id
    FROM redstone_market_products WHERE id = NEW.product_id;
    IF NOT FOUND OR expected_seller_id <> NEW.seller_user_id THEN
        RAISE EXCEPTION 'redstone marketplace order seller does not match product';
    END IF;
    SELECT product_id INTO delivery_product_id
    FROM redstone_market_delivery_items WHERE id = NEW.delivery_item_id;
    IF NOT FOUND OR delivery_product_id <> NEW.product_id THEN
        RAISE EXCEPTION 'redstone marketplace order delivery does not match product';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_validate_order_integrity ON redstone_market_orders;
CREATE TRIGGER trg_redstone_market_validate_order_integrity
    BEFORE INSERT OR UPDATE OF product_id, seller_user_id, delivery_item_id ON redstone_market_orders
    FOR EACH ROW EXECUTE FUNCTION redstone_market_validate_order_integrity();

CREATE OR REPLACE FUNCTION redstone_market_delivery_audit_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone_market_delivery_audit is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_delivery_audit_immutable ON redstone_market_delivery_audit;
CREATE TRIGGER trg_redstone_market_delivery_audit_immutable
    BEFORE UPDATE OR DELETE ON redstone_market_delivery_audit
    FOR EACH ROW EXECUTE FUNCTION redstone_market_delivery_audit_immutable();
