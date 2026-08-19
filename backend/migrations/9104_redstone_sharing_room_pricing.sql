-- Room pricing is usage based. Legacy lease columns remain for lifecycle
-- compatibility and are no longer exposed by the room editor.
ALTER TABLE redstone_account_share_rooms
    ADD COLUMN IF NOT EXISTS room_rate_multiplier DECIMAL(10,8) NOT NULL DEFAULT 1
        CHECK (room_rate_multiplier > 0),
    ADD COLUMN IF NOT EXISTS hourly_fee DECIMAL(20,8) NOT NULL DEFAULT 0
        CHECK (hourly_fee >= 0),
    ADD COLUMN IF NOT EXISTS hourly_fee_free_threshold DECIMAL(20,8) NOT NULL DEFAULT 0
        CHECK (hourly_fee_free_threshold >= 0);

-- Existing rooms used the old one-shot price. Treat it as the first
-- generation's hourly fee so they keep an equivalent, non-breaking price.
UPDATE redstone_account_share_rooms
SET hourly_fee = lease_price
WHERE hourly_fee = 0 AND lease_price > 0;
