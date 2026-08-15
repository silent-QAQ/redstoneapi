-- Redstone operations, earnings, and content-governance foundation.
-- This migration deliberately does not recreate sub2 announcements, groups,
-- API keys, accounts, or payment orders. Those domains remain authoritative.

-- Wallet operation checks are defined in the immutable 9000 migration. Extend
-- them forward here so all operating money movements have their own ledger
-- reason and are never disguised as marketplace debits.
ALTER TABLE redstone_wallet_operations
    DROP CONSTRAINT IF EXISTS redstone_wallet_operations_operation;
ALTER TABLE redstone_wallet_operations
    ADD CONSTRAINT redstone_wallet_operations_operation CHECK (operation IN (
        'admin_grant', 'admin_adjustment', 'redeem_code', 'payment',
        'settlement', 'refund', 'referral_reward', 'activity_reward',
        'opening_balance', 'token_charge', 'token_hold', 'token_release',
        'marketplace_debit', 'withdrawal'
    ));

ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_operation;
ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_operation CHECK (operation IN (
        'admin_grant', 'admin_adjustment', 'redeem_code', 'payment',
        'settlement', 'refund', 'referral_reward', 'activity_reward',
        'opening_balance', 'token_charge', 'token_hold', 'token_release',
        'marketplace_debit', 'withdrawal'
    ));
ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_bound_operation;
ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_bound_operation CHECK (
        asset_type <> 'bound' OR operation IN (
            'admin_grant', 'redeem_code', 'token_charge', 'token_hold', 'token_release'
        )
    );
ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_marketplace_normal_debit;
ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_normal_debit CHECK (
        operation NOT IN ('marketplace_debit', 'withdrawal', 'admin_adjustment')
        OR (asset_type = 'normal' AND delta < 0)
    );

-- The existing proxy row stays owned by sub2. These attributes are only an
-- authorization and allocation policy, not a second proxy entity.
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS owner_user_id BIGINT;
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS max_accounts INTEGER NOT NULL DEFAULT 0;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'redstone_proxies_owner_user_id_fkey') THEN
        ALTER TABLE proxies ADD CONSTRAINT redstone_proxies_owner_user_id_fkey
            FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'redstone_proxies_max_accounts_nonnegative') THEN
        ALTER TABLE proxies ADD CONSTRAINT redstone_proxies_max_accounts_nonnegative
            CHECK (max_accounts >= 0);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_redstone_proxies_owner_capacity
    ON proxies(owner_user_id, max_accounts) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS redstone_operations_withdrawals (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    fee_amount DECIMAL(20,8) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
    total_debited DECIMAL(20,8) NOT NULL CHECK (total_debited > 0),
    payout_method VARCHAR(32) NOT NULL,
    -- This is an opaque reference to a payment integration/profile. Actual
    -- payout account details must never be persisted by this service.
    payout_reference VARCHAR(256) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'debit_pending'
        CHECK (status IN ('debit_pending', 'pending_review', 'approved', 'paid', 'rejected', 'cancelled', 'debit_failed')),
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    admin_note VARCHAR(1000) NOT NULL DEFAULT '',
    processed_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_withdrawal_user
    ON redstone_operations_withdrawals(user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_withdrawal_queue
    ON redstone_operations_withdrawals(status, created_at ASC, id ASC)
    WHERE status IN ('pending_review', 'approved');

CREATE TABLE IF NOT EXISTS redstone_operations_referral_rewards (
    id BIGSERIAL PRIMARY KEY,
    inviter_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invited_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    source_type VARCHAR(64) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    wallet_operation_key VARCHAR(128) NOT NULL UNIQUE,
    granted_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (inviter_user_id <> invited_user_id),
    UNIQUE (inviter_user_id, source_type, source_id)
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_referral_inviter
    ON redstone_operations_referral_rewards(inviter_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS redstone_operations_invoice_profiles (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invoice_type VARCHAR(32) NOT NULL CHECK (invoice_type IN ('personal_normal', 'enterprise_normal', 'enterprise_special')),
    title_name VARCHAR(255) NOT NULL,
    tax_id VARCHAR(64) NOT NULL DEFAULT '',
    recipient_email VARCHAR(255) NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_operations_invoice_default
    ON redstone_operations_invoice_profiles(user_id) WHERE is_default;

CREATE TABLE IF NOT EXISTS redstone_operations_invoice_requests (
    id BIGSERIAL PRIMARY KEY,
    request_number VARCHAR(64) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    profile_id BIGINT REFERENCES redstone_operations_invoice_profiles(id) ON DELETE SET NULL,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    currency VARCHAR(10) NOT NULL DEFAULT 'CNY',
    source_type VARCHAR(64) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'issued', 'rejected', 'cancelled')),
    invoice_number VARCHAR(128) NOT NULL DEFAULT '',
    file_reference VARCHAR(256) NOT NULL DEFAULT '',
    note VARCHAR(1000) NOT NULL DEFAULT '',
    processed_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, source_type, source_id)
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_invoice_user
    ON redstone_operations_invoice_requests(user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_invoice_queue
    ON redstone_operations_invoice_requests(status, created_at ASC, id ASC) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS redstone_operations_tickets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    subject VARCHAR(200) NOT NULL,
    category VARCHAR(32) NOT NULL DEFAULT 'support',
    status VARCHAR(24) NOT NULL DEFAULT 'pending_admin'
        CHECK (status IN ('open', 'pending_user', 'pending_admin', 'resolved', 'closed')),
    priority VARCHAR(16) NOT NULL DEFAULT 'normal'
        CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    assigned_admin_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_ticket_user
    ON redstone_operations_tickets(user_id, last_message_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_ticket_queue
    ON redstone_operations_tickets(status, priority, last_message_at ASC, id ASC)
    WHERE status IN ('open', 'pending_admin');

CREATE TABLE IF NOT EXISTS redstone_operations_ticket_messages (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES redstone_operations_tickets(id) ON DELETE RESTRICT,
    sender_kind VARCHAR(16) NOT NULL CHECK (sender_kind IN ('user', 'admin', 'system')),
    sender_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    body VARCHAR(8000) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((sender_kind = 'system') = (sender_user_id IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_ticket_message
    ON redstone_operations_ticket_messages(ticket_id, id);

CREATE TABLE IF NOT EXISTS redstone_operations_campaigns (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(120) NOT NULL,
    description VARCHAR(2000) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'ended')),
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    reward_amount DECIMAL(20,8) NOT NULL CHECK (reward_amount > 0),
    max_claims INTEGER NOT NULL DEFAULT 0 CHECK (max_claims >= 0),
    created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_campaign_active
    ON redstone_operations_campaigns(status, starts_at, ends_at) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS redstone_operations_campaign_claims (
    id BIGSERIAL PRIMARY KEY,
    campaign_id BIGINT NOT NULL REFERENCES redstone_operations_campaigns(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'granted', 'failed')),
    wallet_operation_key VARCHAR(128) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    granted_at TIMESTAMPTZ,
    UNIQUE (campaign_id, user_id)
);

CREATE TABLE IF NOT EXISTS redstone_operations_content_cases (
    id BIGSERIAL PRIMARY KEY,
    reporter_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(128) NOT NULL,
    reason VARCHAR(1000) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'restricted', 'removed')),
    decision_note VARCHAR(1000) NOT NULL DEFAULT '',
    decided_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (reporter_user_id, subject_type, subject_id)
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_content_case_queue
    ON redstone_operations_content_cases(status, created_at ASC, id ASC) WHERE status = 'open';

CREATE TABLE IF NOT EXISTS redstone_operations_audits (
    id BIGSERIAL PRIMARY KEY,
    actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(128) NOT NULL,
    action VARCHAR(64) NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_redstone_operations_audit_subject
    ON redstone_operations_audits(subject_type, subject_id, created_at DESC);

COMMENT ON TABLE redstone_operations_referral_rewards IS
    'Referral rewards paid only through redstone_wallet_ledger normal balance entries.';
COMMENT ON TABLE redstone_operations_campaigns IS
    'Operations campaigns. Points, lottery, shop, and subsite control-plane concepts are intentionally excluded.';
