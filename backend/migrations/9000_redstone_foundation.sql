-- RedstoneAPI Foundation Migration
-- This migration establishes the core Redstone-specific tables that extend sub2api
-- All Redstone tables use the 'redstone_' prefix and migration numbers 9000-9999
--
-- Design Principles:
-- 1. All tables are additive - never modify sub2api core tables
-- 2. Use foreign keys to reference sub2api tables (users, accounts, etc.)
-- 3. All timestamps are timestamptz for consistency
-- 4. All monetary values use NUMERIC(20,8) for precision

-- ============================================================================
-- Wallet System: Dual Balance Model
-- ============================================================================

-- Wallet balance types
CREATE TYPE redstone_balance_type AS ENUM ('regular', 'bound');

-- Wallet transaction ledger (immutable)
CREATE TABLE redstone_wallet_transactions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Transaction identification
    transaction_type VARCHAR(50) NOT NULL, -- 'recharge', 'withdraw', 'api_consume', 'sharing_income', 'market_income', 'refund', 'admin_adjust', 'redeem_code'
    idempotency_key VARCHAR(255) UNIQUE, -- For deduplication

    -- Balance changes
    balance_type redstone_balance_type NOT NULL,
    amount NUMERIC(20,8) NOT NULL, -- Positive for credit, negative for debit
    balance_after NUMERIC(20,8) NOT NULL, -- Snapshot after this transaction

    -- Reference information
    reference_type VARCHAR(50), -- 'order', 'lease', 'withdrawal', 'product', 'token_usage', etc.
    reference_id BIGINT, -- ID of the related entity

    -- Metadata
    description TEXT,
    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_wallet_transactions_user_id ON redstone_wallet_transactions(user_id);
CREATE INDEX idx_redstone_wallet_transactions_user_created ON redstone_wallet_transactions(user_id, created_at DESC);
CREATE INDEX idx_redstone_wallet_transactions_type ON redstone_wallet_transactions(transaction_type);
CREATE INDEX idx_redstone_wallet_transactions_reference ON redstone_wallet_transactions(reference_type, reference_id);
CREATE INDEX idx_redstone_wallet_transactions_idempotency ON redstone_wallet_transactions(idempotency_key) WHERE idempotency_key IS NOT NULL;

-- Bound balance tracking (separate table for clarity)
CREATE TABLE redstone_user_bound_balances (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    balance NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (balance >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================================
-- Account Verification System
-- ============================================================================

-- Verification status enum
CREATE TYPE redstone_verification_status AS ENUM ('pending', 'success', 'failed', 'skipped');

-- Account verification records
CREATE TABLE redstone_account_verifications (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- Verification execution
    verification_type VARCHAR(50) NOT NULL, -- 'oauth_refresh', 'api_connectivity', 'manual'
    status redstone_verification_status NOT NULL,

    -- Results
    verified_at TIMESTAMPTZ,
    response_time_ms INTEGER, -- Network latency
    error_message TEXT,
    error_code VARCHAR(50),

    -- Metadata
    metadata JSONB DEFAULT '{}'::jsonb, -- Store protocol-specific details

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_account_verifications_account ON redstone_account_verifications(account_id, created_at DESC);
CREATE INDEX idx_redstone_account_verifications_status ON redstone_account_verifications(status);

-- Account verification summary (for quick lookups)
CREATE TABLE redstone_account_verification_summary (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,

    last_verified_at TIMESTAMPTZ,
    last_verification_status redstone_verification_status,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    total_verifications INTEGER NOT NULL DEFAULT 0,
    success_count INTEGER NOT NULL DEFAULT 0,

    -- Auto-disable logic
    is_auto_disabled BOOLEAN NOT NULL DEFAULT FALSE,
    auto_disabled_at TIMESTAMPTZ,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_account_verification_summary_status ON redstone_account_verification_summary(last_verification_status);
CREATE INDEX idx_redstone_account_verification_summary_disabled ON redstone_account_verification_summary(is_auto_disabled) WHERE is_auto_disabled = TRUE;

-- ============================================================================
-- Data Migration: Initialize existing users with bound balance = 0
-- ============================================================================

INSERT INTO redstone_user_bound_balances (user_id, balance, updated_at)
SELECT id, 0, NOW()
FROM users
ON CONFLICT (user_id) DO NOTHING;

-- ============================================================================
-- Migration Metadata
-- ============================================================================

COMMENT ON TABLE redstone_wallet_transactions IS 'Immutable ledger of all wallet operations for audit trail';
COMMENT ON TABLE redstone_user_bound_balances IS 'Non-withdrawable bonus balance for API usage only';
COMMENT ON TABLE redstone_account_verifications IS 'Historical record of account verification attempts';
COMMENT ON TABLE redstone_account_verification_summary IS 'Aggregated verification status for accounts';
