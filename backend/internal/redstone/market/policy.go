package market

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrFeePolicyUnavailable = errors.New("market fee policy is unavailable")
	ErrInvalidFeeRate       = errors.New("market user service fee rate is invalid")
	ErrFeePolicyReason      = errors.New("market fee policy reason is required")
)

// FeePolicy is the current policy for future user-seller orders. Each order
// stores its own rate, so this value never changes historical settlements.
type FeePolicy struct {
	UserServiceFeeRate decimal.Decimal `json:"user_service_fee_rate"`
	UpdatedByUserID    *int64          `json:"updated_by_user_id,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type UpdateFeePolicyRequest struct {
	ActorUserID        int64
	UserServiceFeeRate decimal.Decimal
	Reason             string
}

func (r UpdateFeePolicyRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.UserServiceFeeRate.IsNegative() || r.UserServiceFeeRate.GreaterThan(decimal.NewFromInt(1)) ||
		!r.UserServiceFeeRate.Equal(r.UserServiceFeeRate.Round(8)) {
		return ErrInvalidFeeRate
	}
	if strings.TrimSpace(r.Reason) == "" || len([]rune(strings.TrimSpace(r.Reason))) > 500 {
		return ErrFeePolicyReason
	}
	return nil
}

// FeePolicyRepository is optional so existing marketplace test doubles retain
// source compatibility. The production PostgreSQL repository implements it.
type FeePolicyRepository interface {
	GetFeePolicy(context.Context) (FeePolicy, error)
	UpdateFeePolicy(context.Context, UpdateFeePolicyRequest) (FeePolicy, error)
}

func (s *Service) feePolicyRepository() (FeePolicyRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrFeePolicyUnavailable
	}
	repository, ok := s.repository.(FeePolicyRepository)
	if !ok {
		return nil, ErrFeePolicyUnavailable
	}
	return repository, nil
}

func (s *Service) GetFeePolicy(ctx context.Context) (FeePolicy, error) {
	repository, err := s.feePolicyRepository()
	if err != nil {
		return FeePolicy{}, marketApplicationError(err)
	}
	policy, err := repository.GetFeePolicy(ctx)
	return policy, marketApplicationError(err)
}

func (s *Service) UpdateFeePolicy(ctx context.Context, request UpdateFeePolicyRequest) (FeePolicy, error) {
	if err := request.Validate(); err != nil {
		return FeePolicy{}, marketApplicationError(err)
	}
	repository, err := s.feePolicyRepository()
	if err != nil {
		return FeePolicy{}, marketApplicationError(err)
	}
	request.Reason = strings.TrimSpace(request.Reason)
	policy, err := repository.UpdateFeePolicy(ctx, request)
	return policy, marketApplicationError(err)
}

func defaultUserServiceFeeRate() decimal.Decimal {
	return decimal.RequireFromString(DefaultUserServiceFeeRate)
}

func (r *sqlRepository) GetFeePolicy(ctx context.Context) (FeePolicy, error) {
	return scanFeePolicy(r.db.QueryRowContext(ctx, `
		SELECT user_service_fee_rate, updated_by_user_id, updated_at
		FROM redstone_market_fee_policy WHERE singleton = TRUE
	`))
}

func (r *sqlRepository) UpdateFeePolicy(ctx context.Context, request UpdateFeePolicyRequest) (_ FeePolicy, err error) {
	if err := request.Validate(); err != nil {
		return FeePolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return FeePolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()
	previous, err := scanFeePolicy(tx.QueryRowContext(ctx, `
		SELECT user_service_fee_rate, updated_by_user_id, updated_at
		FROM redstone_market_fee_policy WHERE singleton = TRUE FOR UPDATE
	`))
	if err != nil {
		return FeePolicy{}, err
	}
	now := time.Now().UTC()
	if !previous.UserServiceFeeRate.Equal(request.UserServiceFeeRate) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_fee_policy
			SET user_service_fee_rate = $1, updated_by_user_id = $2, updated_at = $3
			WHERE singleton = TRUE
		`, request.UserServiceFeeRate, request.ActorUserID, now); err != nil {
			return FeePolicy{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO redstone_market_fee_policy_audit (
				prior_user_service_fee_rate, next_user_service_fee_rate, actor_user_id, reason, created_at
			) VALUES ($1, $2, $3, $4, $5)
		`, previous.UserServiceFeeRate, request.UserServiceFeeRate, request.ActorUserID, request.Reason, now); err != nil {
			return FeePolicy{}, err
		}
		previous.UserServiceFeeRate = request.UserServiceFeeRate
		previous.UpdatedByUserID = &request.ActorUserID
		previous.UpdatedAt = now
	}
	if err := tx.Commit(); err != nil {
		return FeePolicy{}, err
	}
	return previous, nil
}

func (r *sqlRepository) lockUserServiceFeeRate(ctx context.Context, tx *sql.Tx) (decimal.Decimal, error) {
	var feeRate decimal.Decimal
	err := tx.QueryRowContext(ctx, `
		SELECT user_service_fee_rate
		FROM redstone_market_fee_policy WHERE singleton = TRUE FOR SHARE
	`).Scan(&feeRate)
	if errors.Is(err, sql.ErrNoRows) {
		// A fresh migration always inserts the singleton. Keeping this fallback
		// makes an interrupted legacy rollout fail safe at the documented 5%.
		return defaultUserServiceFeeRate(), nil
	}
	if err != nil {
		return decimal.Zero, err
	}
	if feeRate.IsNegative() || feeRate.GreaterThan(decimal.NewFromInt(1)) || !feeRate.Equal(feeRate.Round(8)) {
		return decimal.Zero, ErrInvalidFeeRate
	}
	return feeRate, nil
}

type feePolicyRow interface {
	Scan(dest ...any) error
}

func scanFeePolicy(row feePolicyRow) (FeePolicy, error) {
	var policy FeePolicy
	var updatedBy sql.NullInt64
	if err := row.Scan(&policy.UserServiceFeeRate, &updatedBy, &policy.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FeePolicy{}, ErrFeePolicyUnavailable
		}
		return FeePolicy{}, err
	}
	if updatedBy.Valid {
		value := updatedBy.Int64
		policy.UpdatedByUserID = &value
	}
	return policy, nil
}
