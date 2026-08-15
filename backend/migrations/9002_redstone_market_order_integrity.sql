-- A delivery item represents one secret/account/file entitlement and must not
-- be attached to more than one order. The application also locks and changes
-- the item status atomically; this index makes that invariant durable.
CREATE UNIQUE INDEX IF NOT EXISTS idx_redstone_market_orders_delivery_item_unique
    ON redstone_market_orders (delivery_item_id);
