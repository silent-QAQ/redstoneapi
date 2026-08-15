package sharing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalidSharingPolicy  = errors.New("account share governance policy is invalid")
	ErrInvalidQuotaPolicy    = errors.New("account share governance quota is invalid")
	ErrGovernanceUnavailable = errors.New("account share governance repository is unavailable")
	ErrPolicyNotFound        = errors.New("account share governance policy was not found")
	ErrQuotaPolicyNotFound   = errors.New("account share governance quota policy was not found")
)

// SharingPolicy is the immutable policy snapshot used by future room changes.
// Version corresponds to the append-only policy version stored by the database.
type SharingPolicy struct {
	Version                int64           `json:"version"`
	PublicRoomAllowed      bool            `json:"public_room_allowed"`
	MaxLeaseSeconds        int             `json:"max_lease_seconds"`
	DefaultPlatformFeeRate decimal.Decimal `json:"default_platform_fee_rate"`
	CreatedByUserID        *int64          `json:"created_by_user_id,omitempty"`
	Reason                 string          `json:"reason"`
	CreatedAt              time.Time       `json:"created_at"`

	recordID int64
}

// QuotaScope controls whether a quota is the global default or an owner
// override. Quota versions reuse the immutable row identifier because the
// initial schema intentionally stored quotas as append-only rows.
type QuotaScope string

const (
	QuotaScopeGlobal QuotaScope = "global"
	QuotaScopeOwner  QuotaScope = "owner"
)

// QuotaPolicy is the current global default or a current owner override.
// Version is an immutable, monotonically allocated row identifier.
type QuotaPolicy struct {
	Version               int64      `json:"version"`
	Scope                 QuotaScope `json:"scope"`
	OwnerUserID           *int64     `json:"owner_user_id,omitempty"`
	MaxLiveRooms          int        `json:"max_live_rooms"`
	MaxAccountsPerRoom    int        `json:"max_accounts_per_room"`
	MaxRoomsCreatedPerDay int        `json:"max_rooms_created_per_day"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	Reason                string     `json:"reason"`
	CreatedByUserID       *int64     `json:"created_by_user_id,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

type UpdateSharingPolicyRequest struct {
	ActorUserID            int64
	PublicRoomAllowed      bool
	MaxLeaseSeconds        int
	DefaultPlatformFeeRate decimal.Decimal
	Reason                 string
}

func (r UpdateSharingPolicyRequest) Validate() error {
	if r.ActorUserID <= 0 || r.MaxLeaseSeconds < 60 || r.MaxLeaseSeconds > 86400 ||
		r.DefaultPlatformFeeRate.IsNegative() || r.DefaultPlatformFeeRate.GreaterThan(decimal.NewFromInt(1)) ||
		!r.DefaultPlatformFeeRate.Equal(r.DefaultPlatformFeeRate.Round(8)) || !validGovernanceReason(r.Reason) {
		return ErrInvalidSharingPolicy
	}
	return nil
}

type UpdateQuotaPolicyRequest struct {
	ActorUserID           int64
	OwnerUserID           *int64
	MaxLiveRooms          int
	MaxAccountsPerRoom    int
	MaxRoomsCreatedPerDay int
	ExpiresAt             *time.Time
	Reason                string
}

func (r UpdateQuotaPolicyRequest) Validate() error {
	if r.ActorUserID <= 0 || r.MaxLiveRooms < 1 || r.MaxLiveRooms > 1000000 ||
		r.MaxAccountsPerRoom < 1 || r.MaxAccountsPerRoom > 30 ||
		r.MaxRoomsCreatedPerDay < 1 || r.MaxRoomsCreatedPerDay > 1000000 || !validGovernanceReason(r.Reason) {
		return ErrInvalidQuotaPolicy
	}
	if r.OwnerUserID == nil {
		// A global quota must always remain effective. Owner overrides may be
		// time-bound, but the platform-wide fallback must not silently expire.
		if r.ExpiresAt != nil {
			return ErrInvalidQuotaPolicy
		}
		return nil
	}
	if *r.OwnerUserID <= 0 || (r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now().UTC())) {
		return ErrInvalidQuotaPolicy
	}
	return nil
}

func (r UpdateQuotaPolicyRequest) scope() QuotaScope {
	if r.OwnerUserID == nil {
		return QuotaScopeGlobal
	}
	return QuotaScopeOwner
}

// GovernanceRepository is optional so integrations that only provide the
// runtime room state machine do not accidentally expose operator controls.
type GovernanceRepository interface {
	GetSharingPolicy(context.Context) (SharingPolicy, error)
	UpdateSharingPolicy(context.Context, UpdateSharingPolicyRequest) (SharingPolicy, error)
	GetQuotaPolicy(context.Context, *int64) (QuotaPolicy, error)
	UpdateQuotaPolicy(context.Context, UpdateQuotaPolicyRequest) (QuotaPolicy, error)
}

func (s *Service) governanceRepository() (GovernanceRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrGovernanceUnavailable
	}
	repository, ok := s.repository.(GovernanceRepository)
	if !ok {
		return nil, ErrGovernanceUnavailable
	}
	return repository, nil
}

func (s *Service) GetSharingPolicy(ctx context.Context) (SharingPolicy, error) {
	repository, err := s.governanceRepository()
	if err != nil {
		return SharingPolicy{}, applicationError(err)
	}
	policy, err := repository.GetSharingPolicy(ctx)
	return policy, applicationError(err)
}

func (s *Service) UpdateSharingPolicy(ctx context.Context, request UpdateSharingPolicyRequest) (SharingPolicy, error) {
	if err := request.Validate(); err != nil {
		return SharingPolicy{}, applicationError(err)
	}
	repository, err := s.governanceRepository()
	if err != nil {
		return SharingPolicy{}, applicationError(err)
	}
	request.Reason = strings.TrimSpace(request.Reason)
	policy, err := repository.UpdateSharingPolicy(ctx, request)
	return policy, applicationError(err)
}

func (s *Service) GetQuotaPolicy(ctx context.Context, ownerUserID *int64) (QuotaPolicy, error) {
	if ownerUserID != nil && *ownerUserID <= 0 {
		return QuotaPolicy{}, applicationError(ErrInvalidQuotaPolicy)
	}
	repository, err := s.governanceRepository()
	if err != nil {
		return QuotaPolicy{}, applicationError(err)
	}
	policy, err := repository.GetQuotaPolicy(ctx, ownerUserID)
	return policy, applicationError(err)
}

func (s *Service) UpdateQuotaPolicy(ctx context.Context, request UpdateQuotaPolicyRequest) (QuotaPolicy, error) {
	if err := request.Validate(); err != nil {
		return QuotaPolicy{}, applicationError(err)
	}
	repository, err := s.governanceRepository()
	if err != nil {
		return QuotaPolicy{}, applicationError(err)
	}
	request.Reason = strings.TrimSpace(request.Reason)
	policy, err := repository.UpdateQuotaPolicy(ctx, request)
	return policy, applicationError(err)
}

func (r *PostgresRepository) GetSharingPolicy(ctx context.Context) (SharingPolicy, error) {
	return scanSharingPolicy(r.db.QueryRowContext(ctx, `
		SELECT id, version, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason, created_at
		FROM redstone_account_share_policies
		WHERE status = 'active'
		LIMIT 1`))
}

func (r *PostgresRepository) UpdateSharingPolicy(ctx context.Context, request UpdateSharingPolicyRequest) (_ SharingPolicy, err error) {
	if err := request.Validate(); err != nil {
		return SharingPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SharingPolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()

	current, err := scanSharingPolicy(tx.QueryRowContext(ctx, `
		SELECT id, version, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason, created_at
		FROM redstone_account_share_policies
		WHERE status = 'active'
		FOR UPDATE`))
	if err != nil {
		return SharingPolicy{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE redstone_account_share_policies
		SET status = 'superseded'
		WHERE id = $1 AND status = 'active'`, current.recordID); err != nil {
		return SharingPolicy{}, err
	}
	next, err := scanSharingPolicy(tx.QueryRowContext(ctx, `
		INSERT INTO redstone_account_share_policies (
			version, status, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason
		) VALUES ($1, 'active', $2, $3, $4, $5, $6)
		RETURNING id, version, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason, created_at`,
		current.Version+1, request.PublicRoomAllowed, request.MaxLeaseSeconds, request.DefaultPlatformFeeRate,
		request.ActorUserID, request.Reason))
	if err != nil {
		return SharingPolicy{}, err
	}
	if err := appendGovernanceAudit(ctx, tx, request.ActorUserID, "policy_version_created", governanceAuditDetail("policy", next.Version, request.Reason)); err != nil {
		return SharingPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return SharingPolicy{}, err
	}
	return next, nil
}

func (r *PostgresRepository) GetQuotaPolicy(ctx context.Context, ownerUserID *int64) (QuotaPolicy, error) {
	if ownerUserID != nil {
		return scanQuotaPolicy(r.db.QueryRowContext(ctx, `
			SELECT id, scope, owner_user_id, max_live_rooms, max_accounts_per_room,
				max_rooms_created_per_day, expires_at, reason, created_by_user_id, created_at
			FROM redstone_account_share_quota_policies
			WHERE scope = 'owner' AND owner_user_id = $1 AND active
				AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY id DESC
			LIMIT 1`, *ownerUserID))
	}
	return scanQuotaPolicy(r.db.QueryRowContext(ctx, `
		SELECT id, scope, owner_user_id, max_live_rooms, max_accounts_per_room,
			max_rooms_created_per_day, expires_at, reason, created_by_user_id, created_at
		FROM redstone_account_share_quota_policies
		WHERE scope = 'global' AND active AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY id DESC
		LIMIT 1`))
}

func (r *PostgresRepository) UpdateQuotaPolicy(ctx context.Context, request UpdateQuotaPolicyRequest) (_ QuotaPolicy, err error) {
	if err := request.Validate(); err != nil {
		return QuotaPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return QuotaPolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// There is no unique active-owner constraint in the initial migration. A
	// short administrative table lock serializes first-time owner overrides as
	// well as global changes, keeping exactly one current version per scope.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE redstone_account_share_quota_policies IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return QuotaPolicy{}, err
	}

	var owner any
	if request.OwnerUserID != nil {
		owner = *request.OwnerUserID
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_quota_policies
		SET active = FALSE
		WHERE scope = $1 AND active AND owner_user_id IS NOT DISTINCT FROM $2`, request.scope(), owner); err != nil {
		return QuotaPolicy{}, err
	}
	next, err := scanQuotaPolicy(tx.QueryRowContext(ctx, `
		INSERT INTO redstone_account_share_quota_policies (
			scope, owner_user_id, max_live_rooms, max_accounts_per_room,
			max_rooms_created_per_day, active, expires_at, reason, created_by_user_id
		) VALUES ($1, $2, $3, $4, $5, TRUE, $6, $7, $8)
		RETURNING id, scope, owner_user_id, max_live_rooms, max_accounts_per_room,
			max_rooms_created_per_day, expires_at, reason, created_by_user_id, created_at`,
		request.scope(), owner, request.MaxLiveRooms, request.MaxAccountsPerRoom, request.MaxRoomsCreatedPerDay,
		request.ExpiresAt, request.Reason, request.ActorUserID))
	if err != nil {
		return QuotaPolicy{}, err
	}
	if err := appendGovernanceAudit(ctx, tx, request.ActorUserID, "quota_version_created", governanceAuditDetail(string(request.scope()), next.Version, request.Reason)); err != nil {
		return QuotaPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return QuotaPolicy{}, err
	}
	return next, nil
}

type sharingPolicyRow interface{ Scan(...any) error }

func scanSharingPolicy(row sharingPolicyRow) (SharingPolicy, error) {
	var policy SharingPolicy
	var createdBy sql.NullInt64
	if err := row.Scan(
		&policy.recordID, &policy.Version, &policy.PublicRoomAllowed, &policy.MaxLeaseSeconds,
		&policy.DefaultPlatformFeeRate, &createdBy, &policy.Reason, &policy.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SharingPolicy{}, ErrPolicyNotFound
		}
		return SharingPolicy{}, err
	}
	if createdBy.Valid {
		value := createdBy.Int64
		policy.CreatedByUserID = &value
	}
	policy.CreatedAt = policy.CreatedAt.UTC()
	return policy, nil
}

type quotaPolicyRow interface{ Scan(...any) error }

func scanQuotaPolicy(row quotaPolicyRow) (QuotaPolicy, error) {
	var policy QuotaPolicy
	var scope string
	var ownerUserID, createdBy sql.NullInt64
	var expiresAt sql.NullTime
	if err := row.Scan(
		&policy.Version, &scope, &ownerUserID, &policy.MaxLiveRooms, &policy.MaxAccountsPerRoom,
		&policy.MaxRoomsCreatedPerDay, &expiresAt, &policy.Reason, &createdBy, &policy.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QuotaPolicy{}, ErrQuotaPolicyNotFound
		}
		return QuotaPolicy{}, err
	}
	policy.Scope = QuotaScope(scope)
	if policy.Scope != QuotaScopeGlobal && policy.Scope != QuotaScopeOwner {
		return QuotaPolicy{}, ErrInvalidQuotaPolicy
	}
	if ownerUserID.Valid {
		value := ownerUserID.Int64
		policy.OwnerUserID = &value
	}
	if expiresAt.Valid {
		value := expiresAt.Time.UTC()
		policy.ExpiresAt = &value
	}
	if createdBy.Valid {
		value := createdBy.Int64
		policy.CreatedByUserID = &value
	}
	policy.CreatedAt = policy.CreatedAt.UTC()
	return policy, nil
}

func appendGovernanceAudit(ctx context.Context, tx *sql.Tx, actorUserID int64, action, detail string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_account_share_audits (room_id, actor_user_id, action, detail)
		VALUES (NULL, $1, $2, $3)`, actorUserID, action, detail)
	return err
}

func governanceAuditDetail(subject string, version int64, reason string) string {
	return fmt.Sprintf("%s_version=%d; reason=%s", subject, version, strings.TrimSpace(reason))
}

func validGovernanceReason(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= 500
}
