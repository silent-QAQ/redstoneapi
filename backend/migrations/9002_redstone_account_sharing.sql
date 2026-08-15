-- RedstoneAPI Account Sharing Migration
-- User-controlled account sharing with room-based access management
-- Migration number: 9002 (Redstone namespace)

-- ============================================================================
-- Sharing Rooms
-- ============================================================================

-- Room visibility
CREATE TYPE redstone_room_visibility AS ENUM ('private', 'public', 'plaza');

-- Room status
CREATE TYPE redstone_room_status AS ENUM ('active', 'paused', 'pending_review', 'rejected', 'closed');

-- Sharing rooms
CREATE TABLE redstone_sharing_rooms (
    id BIGSERIAL PRIMARY KEY,
    owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Basic info
    name VARCHAR(200) NOT NULL,
    description TEXT,

    -- Visibility & Access
    visibility redstone_room_visibility NOT NULL DEFAULT 'private',
    status redstone_room_status NOT NULL DEFAULT 'active',

    -- Pricing (for public/plaza rooms)
    price_per_hour NUMERIC(20,8), -- NULL for private rooms
    allows_bound_balance BOOLEAN NOT NULL DEFAULT FALSE, -- Whether bound balance can be used

    -- Capacity
    max_concurrent_users INTEGER NOT NULL DEFAULT 1,
    current_active_leases INTEGER NOT NULL DEFAULT 0,

    -- Quota & Scheduling
    quota_type VARCHAR(50), -- 'unlimited', 'hourly', 'daily', NULL for account-based quota
    quota_amount INTEGER,

    -- Queue management
    queue_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    max_queue_size INTEGER,
    current_queue_size INTEGER NOT NULL DEFAULT 0,

    -- Statistics
    total_leases INTEGER NOT NULL DEFAULT 0,
    total_revenue NUMERIC(20,8) NOT NULL DEFAULT 0,

    -- Review (for public/plaza rooms)
    reviewed_at TIMESTAMPTZ,
    reviewed_by BIGINT REFERENCES users(id),
    review_note TEXT,

    -- Metadata
    tags TEXT[],
    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_sharing_rooms_owner ON redstone_sharing_rooms(owner_id);
CREATE INDEX idx_redstone_sharing_rooms_visibility ON redstone_sharing_rooms(visibility, status);
CREATE INDEX idx_redstone_sharing_rooms_status ON redstone_sharing_rooms(status);
CREATE INDEX idx_redstone_sharing_rooms_tags ON redstone_sharing_rooms USING GIN(tags);

-- ============================================================================
-- Room-Account Bindings
-- ============================================================================

-- Room account bindings
CREATE TABLE redstone_room_accounts (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_sharing_rooms(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- Assignment priority
    priority INTEGER NOT NULL DEFAULT 0,

    -- Status
    is_active BOOLEAN NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(room_id, account_id)
);

CREATE INDEX idx_redstone_room_accounts_room ON redstone_room_accounts(room_id, priority DESC);
CREATE INDEX idx_redstone_room_accounts_account ON redstone_room_accounts(account_id);

-- ============================================================================
-- Leases (Active Sessions)
-- ============================================================================

-- Lease status
CREATE TYPE redstone_lease_status AS ENUM (
    'active',
    'expired',
    'terminated_by_user',
    'terminated_by_owner',
    'terminated_by_admin',
    'terminated_idle'
);

-- Leases
CREATE TABLE redstone_sharing_leases (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_sharing_rooms(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- Timing
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ,

    -- Billing
    hourly_rate NUMERIC(20,8) NOT NULL, -- Snapshot at lease creation
    total_cost NUMERIC(20,8) NOT NULL DEFAULT 0,
    paid_with_bound_balance NUMERIC(20,8) NOT NULL DEFAULT 0,
    paid_with_regular_balance NUMERIC(20,8) NOT NULL DEFAULT 0,

    -- Settlement (owner's revenue)
    owner_revenue NUMERIC(20,8) NOT NULL DEFAULT 0, -- After platform fee
    platform_fee NUMERIC(20,8) NOT NULL DEFAULT 0,
    settled_at TIMESTAMPTZ,
    settlement_transaction_id BIGINT REFERENCES redstone_wallet_transactions(id),

    -- Status
    status redstone_lease_status NOT NULL DEFAULT 'active',
    termination_reason TEXT,

    -- Metadata
    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_redstone_sharing_leases_room ON redstone_sharing_leases(room_id, status);
CREATE INDEX idx_redstone_sharing_leases_user ON redstone_sharing_leases(user_id, created_at DESC);
CREATE INDEX idx_redstone_sharing_leases_account ON redstone_sharing_leases(account_id, status);
CREATE INDEX idx_redstone_sharing_leases_active ON redstone_sharing_leases(status, expires_at) WHERE status = 'active';
CREATE INDEX idx_redstone_sharing_leases_settlement ON redstone_sharing_leases(settled_at) WHERE settled_at IS NULL AND status != 'active';

-- ============================================================================
-- Queue Management
-- ============================================================================

-- Queue status
CREATE TYPE redstone_queue_status AS ENUM ('waiting', 'notified', 'expired', 'cancelled');

-- Room queue
CREATE TABLE redstone_room_queue (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_sharing_rooms(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Position
    queue_position INTEGER NOT NULL,

    -- Notification
    status redstone_queue_status NOT NULL DEFAULT 'waiting',
    notified_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ, -- Notification expires after X minutes

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(room_id, user_id)
);

CREATE INDEX idx_redstone_room_queue_room ON redstone_room_queue(room_id, queue_position);
CREATE INDEX idx_redstone_room_queue_user ON redstone_room_queue(user_id);
CREATE INDEX idx_redstone_room_queue_status ON redstone_room_queue(status, expires_at);

-- ============================================================================
-- Reviews & Ratings
-- ============================================================================

-- User reviews for shared accounts
CREATE TABLE redstone_sharing_reviews (
    id BIGSERIAL PRIMARY KEY,
    lease_id BIGINT NOT NULL REFERENCES redstone_sharing_leases(id) ON DELETE CASCADE,
    room_id BIGINT NOT NULL REFERENCES redstone_sharing_rooms(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Rating
    rating INTEGER NOT NULL CHECK (rating >= 1 AND rating <= 5),
    content TEXT,

    -- Moderation
    is_reported BOOLEAN NOT NULL DEFAULT FALSE,
    is_hidden BOOLEAN NOT NULL DEFAULT FALSE,
    moderated_at TIMESTAMPTZ,
    moderated_by BIGINT REFERENCES users(id),
    moderation_reason TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(lease_id) -- One review per lease
);

CREATE INDEX idx_redstone_sharing_reviews_room ON redstone_sharing_reviews(room_id, created_at DESC);
CREATE INDEX idx_redstone_sharing_reviews_user ON redstone_sharing_reviews(user_id);
CREATE INDEX idx_redstone_sharing_reviews_owner ON redstone_sharing_reviews(owner_id);
CREATE INDEX idx_redstone_sharing_reviews_reported ON redstone_sharing_reviews(is_reported) WHERE is_reported = TRUE;

-- ============================================================================
-- Room Statistics Cache
-- ============================================================================

-- Cached room statistics (updated periodically)
CREATE TABLE redstone_room_statistics (
    room_id BIGINT PRIMARY KEY REFERENCES redstone_sharing_rooms(id) ON DELETE CASCADE,

    -- Usage
    total_leases INTEGER NOT NULL DEFAULT 0,
    total_hours NUMERIC(10,2) NOT NULL DEFAULT 0,
    avg_session_hours NUMERIC(10,2),

    -- Revenue
    total_revenue NUMERIC(20,8) NOT NULL DEFAULT 0,
    pending_settlement NUMERIC(20,8) NOT NULL DEFAULT 0,

    -- Ratings
    avg_rating NUMERIC(3,2),
    total_reviews INTEGER NOT NULL DEFAULT 0,

    -- Activity
    last_lease_at TIMESTAMPTZ,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================================
-- Migration Metadata
-- ============================================================================

COMMENT ON TABLE redstone_sharing_rooms IS 'User-created sharing rooms for controlled account access';
COMMENT ON TABLE redstone_room_accounts IS 'Account bindings for each room';
COMMENT ON TABLE redstone_sharing_leases IS 'Active and historical lease sessions';
COMMENT ON TABLE redstone_room_queue IS 'Queue management for full rooms';
COMMENT ON TABLE redstone_sharing_reviews IS 'User reviews and ratings for shared accounts';
COMMENT ON TABLE redstone_room_statistics IS 'Cached aggregated statistics for rooms';
