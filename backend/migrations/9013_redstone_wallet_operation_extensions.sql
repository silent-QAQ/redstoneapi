-- 9013_redstone_wallet_operation_extensions.sql
-- Keep persisted operation constraints aligned with the append-only wallet
-- domain. 9000 predates token holds and the non-admin grant sources below.

ALTER TABLE redstone_wallet_operations
    DROP CONSTRAINT IF EXISTS redstone_wallet_operations_operation;

ALTER TABLE redstone_wallet_operations
    ADD CONSTRAINT redstone_wallet_operations_operation CHECK (operation IN (
        'admin_grant', 'redeem_code', 'payment', 'settlement', 'refund',
        'promo_code', 'provider_grant', 'opening_balance', 'token_charge',
        'token_hold', 'token_release', 'marketplace_debit'
    ));

ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_operation;

ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_operation CHECK (operation IN (
        'admin_grant', 'redeem_code', 'payment', 'settlement', 'refund',
        'promo_code', 'provider_grant', 'opening_balance', 'token_charge',
        'token_hold', 'token_release', 'marketplace_debit'
    ));

ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_bound_operation;

ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_bound_operation CHECK (
        asset_type <> 'bound' OR operation IN (
            'admin_grant', 'redeem_code', 'token_charge', 'token_hold', 'token_release'
        )
    );
