package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dbent "github.com/silent-QAQ/redstoneapi/ent"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/logger"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

type usageBillingRepository struct {
	db     *sql.DB
	wallet *wallet.Service
}

func NewUsageBillingRepository(_ *dbent.Client, sqlDB *sql.DB) service.UsageBillingRepository {
	// Keep the existing provider signature stable while routing every gateway
	// balance charge through the Redstone wallet policy and ledger.
	walletRepository, err := wallet.NewPostgresRepository(sqlDB)
	if err != nil {
		return &usageBillingRepository{db: sqlDB}
	}
	walletService, err := wallet.NewService(walletRepository)
	if err != nil {
		return &usageBillingRepository{db: sqlDB}
	}
	return &usageBillingRepository{db: sqlDB, wallet: walletService}
}

func (r *usageBillingRepository) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (_ *service.UsageBillingApplyResult, err error) {
	if cmd == nil {
		return &service.UsageBillingApplyResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}

	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) claimUsageBillingKey(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (bool, error) {
	return r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
}

func (r *usageBillingRepository) claimUsageBillingRequest(ctx context.Context, tx *sql.Tx, requestID string, apiKeyID int64, requestFingerprint string) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id
	`, requestID, apiKeyID, requestFingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var existingFingerprint string
		if err := tx.QueryRowContext(ctx, `
			SELECT request_fingerprint
			FROM usage_billing_dedup
			WHERE request_id = $1 AND api_key_id = $2
		`, requestID, apiKeyID).Scan(&existingFingerprint); err != nil {
			return false, err
		}
		if strings.TrimSpace(existingFingerprint) != strings.TrimSpace(requestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var archivedFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT request_fingerprint
		FROM usage_billing_dedup_archive
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, apiKeyID).Scan(&archivedFingerprint)
	if err == nil {
		if strings.TrimSpace(archivedFingerprint) != strings.TrimSpace(requestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return true, nil
}

func (r *usageBillingRepository) ReserveBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageWalletHold(ctx, cmd, batchImageWalletReserve)
}

func (r *usageBillingRepository) CaptureBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageWalletHold(ctx, cmd, batchImageWalletCapture)
}

func (r *usageBillingRepository) ReleaseBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageWalletHold(ctx, cmd, batchImageWalletRelease)
}

type batchImageWalletAction uint8

const (
	batchImageWalletReserve batchImageWalletAction = iota
	batchImageWalletCapture
	batchImageWalletRelease
)

// applyBatchImageWalletHold delegates every pre-charge mutation to the
// Redstone wallet. The old frozen_balance-only implementation lost the source
// asset split, so a failed request could not safely return bound funds.
func (r *usageBillingRepository) applyBatchImageWalletHold(
	ctx context.Context,
	cmd *service.BatchImageBalanceHoldCommand,
	action batchImageWalletAction,
) (*service.BatchImageBalanceHoldResult, error) {
	if cmd == nil {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if r == nil || r.wallet == nil {
		return nil, errors.New("usage billing repository wallet is nil")
	}
	cmd.Normalize()
	if cmd.RequestID == "" || cmd.UserID <= 0 || cmd.APIKeyID <= 0 || strings.TrimSpace(cmd.BatchID) == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	holdKey := batchImageWalletOperationKey("hold", cmd)
	reference := wallet.Reference{Type: "batch_image", ID: strings.TrimSpace(cmd.BatchID)}
	var result wallet.TokenHoldResult
	var err error
	switch action {
	case batchImageWalletReserve:
		if cmd.HoldAmount <= 0 {
			return &service.BatchImageBalanceHoldResult{Applied: true}, nil
		}
		result, err = r.wallet.ReserveTokenHold(ctx, wallet.TokenHoldRequest{
			UserID:             cmd.UserID,
			Amount:             decimal.NewFromFloat(cmd.HoldAmount).Round(wallet.MonetaryScale),
			Reference:          reference,
			IdempotencyKey:     holdKey,
			RequestFingerprint: cmd.RequestFingerprint,
		})
	case batchImageWalletCapture:
		result, err = r.wallet.CaptureTokenHold(ctx, wallet.TokenHoldCaptureRequest{
			UserID:             cmd.UserID,
			HoldKey:            holdKey,
			ActualAmount:       decimal.NewFromFloat(cmd.ActualAmount).Round(wallet.MonetaryScale),
			IdempotencyKey:     batchImageWalletOperationKey("capture", cmd),
			RequestFingerprint: cmd.RequestFingerprint,
		})
	case batchImageWalletRelease:
		result, err = r.wallet.ReleaseTokenHold(ctx, wallet.TokenHoldReleaseRequest{
			UserID:             cmd.UserID,
			HoldKey:            holdKey,
			IdempotencyKey:     batchImageWalletOperationKey("release", cmd),
			RequestFingerprint: cmd.RequestFingerprint,
		})
	default:
		return nil, errors.New("unsupported batch image wallet action")
	}
	if err != nil {
		switch {
		case errors.Is(err, wallet.ErrIdempotencyConflict):
			// A release may be retried after the worker reconstructed a
			// different payload hash. The batch-image service recognizes this
			// as an already-settled release and must not loop forever.
			return nil, service.ErrUsageBillingRequestConflict
		case errors.Is(err, wallet.ErrInsufficientFunds):
			return nil, service.ErrBatchImageInsufficientBalance
		case errors.Is(err, wallet.ErrTokenHoldAmountExceeded):
			return nil, service.ErrBatchImageSettlementCostExceedsHold
		case errors.Is(err, wallet.ErrInvalidUserID):
			return nil, service.ErrUserNotFound
		}
		return nil, err
	}

	normal, _ := result.Balances.Normal.Float64()
	bound, _ := result.Balances.Bound.Float64()
	return &service.BatchImageBalanceHoldResult{
		Applied:             result.Applied,
		NewBalance:          &normal,
		BoundBalance:        &bound,
		BoundBalanceHeld:    decimalToFloatPtr(result.Held.Bound),
		NormalBalanceHeld:   decimalToFloatPtr(result.Held.Normal),
		BoundBalanceRefund:  decimalToFloatPtr(result.Released.Bound),
		NormalBalanceRefund: decimalToFloatPtr(result.Released.Normal),
	}, nil
}

func batchImageWalletOperationKey(action string, cmd *service.BatchImageBalanceHoldCommand) string {
	value := action + "|" + strconv.FormatInt(cmd.UserID, 10) + "|" + strconv.FormatInt(cmd.APIKeyID, 10) + "|" + strings.TrimSpace(cmd.BatchID)
	sum := sha256.Sum256([]byte(value))
	return "batch-wallet-" + action + "-" + hex.EncodeToString(sum[:])
}

func decimalToFloatPtr(value decimal.Decimal) *float64 {
	if value.IsZero() {
		return nil
	}
	converted, _ := value.Float64()
	return &converted
}

func (r *usageBillingRepository) applyBatchImageBalanceHold(
	ctx context.Context,
	cmd *service.BatchImageBalanceHoldCommand,
	apply func(context.Context, *sql.Tx, *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error),
) (_ *service.BatchImageBalanceHoldResult, err error) {
	if cmd == nil {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.BatchImageBalanceHoldResult{Applied: false}, nil
	}

	result, err := apply(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &service.BatchImageBalanceHoldResult{}
	}
	result.Applied = true

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) applyUsageBillingEffects(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	walletCost := cmd.BalanceCost
	if cmd.SubscriptionCost > 0 && cmd.SubscriptionID != nil {
		covered, err := consumeUsageBillingSubscription(ctx, tx, *cmd.SubscriptionID, cmd.SubscriptionCost)
		if err != nil {
			return err
		}
		result.SubscriptionCost = &covered
		// Subscription requests pass their full cost as BalanceCost. Deducting
		// the covered amount here makes the wallet charge only the overflow.
		walletCost = cmd.BalanceCost - covered
		if walletCost < 0 {
			walletCost = 0
		}
	}

	if walletCost > 0 {
		if r.wallet == nil {
			return errors.New("usage billing repository wallet is nil")
		}

		// Subscription usage is recorded above. Any remaining token charge then
		// consumes bound balance before normal balance, with both ledger rows
		// written inside this caller-owned transaction.
		walletRequestID := walletUsageRequestID(cmd.RequestID, cmd.APIKeyID)
		charge, err := r.wallet.ChargeTokenInTransaction(ctx, tx, wallet.TokenChargeRequest{
			UserID: cmd.UserID,
			Amount: decimal.NewFromFloat(walletCost).Round(wallet.MonetaryScale),
			Reference: wallet.Reference{
				Type: "usage_billing",
				ID:   walletRequestID,
			},
			IdempotencyKey: walletRequestID,
		})
		if err != nil {
			switch {
			case errors.Is(err, wallet.ErrInsufficientFunds):
				return service.ErrInsufficientBalance
			case errors.Is(err, wallet.ErrInvalidUserID), isWalletOperationUserForeignKeyError(err):
				return service.ErrUserNotFound
			}
			return err
		}
		newBalanceFloat, _ := charge.Allocation.NormalBalanceAfter.Float64()
		result.NewBalance = &newBalanceFloat
		boundCostFloat, _ := charge.Allocation.Bound.Float64()
		result.BoundBalanceCost = &boundCostFloat
		normalCostFloat, _ := charge.Allocation.Normal.Float64()
		result.NormalBalanceCost = &normalCostFloat
		result.BalanceOverdrafted = false
	}

	if cmd.APIKeyQuotaCost > 0 {
		exhausted, err := incrementUsageBillingAPIKeyQuota(ctx, tx, cmd.APIKeyID, cmd.APIKeyQuotaCost)
		if err != nil {
			return err
		}
		result.APIKeyQuotaExhausted = exhausted
	}

	if cmd.APIKeyRateLimitCost > 0 {
		if err := incrementUsageBillingAPIKeyRateLimit(ctx, tx, cmd.APIKeyID, cmd.APIKeyRateLimitCost); err != nil {
			return err
		}
	}

	if cmd.AccountQuotaCost > 0 && (strings.EqualFold(cmd.AccountType, service.AccountTypeAPIKey) || strings.EqualFold(cmd.AccountType, service.AccountTypeBedrock)) {
		quotaState, err := incrementUsageBillingAccountQuota(ctx, tx, cmd.AccountID, cmd.AccountQuotaCost)
		if err != nil {
			return err
		}
		result.QuotaState = quotaState
	}

	return nil
}

// consumeUsageBillingSubscription locks one subscription row and consumes the
// portion of cost still permitted by every configured usage window. The caller
// owns the SQL transaction and charges the remaining tail through the wallet,
// so a wallet error rolls back this update with no stranded subscription use.
func consumeUsageBillingSubscription(ctx context.Context, tx *sql.Tx, subscriptionID int64, costUSD float64) (float64, error) {
	var (
		dailyUsage, weeklyUsage, monthlyUsage float64
		dailyLimit, weeklyLimit, monthlyLimit sql.NullFloat64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT us.daily_usage_usd,
		       us.weekly_usage_usd,
		       us.monthly_usage_usd,
		       g.daily_limit_usd,
		       g.weekly_limit_usd,
		       g.monthly_limit_usd
		FROM user_subscriptions us
		JOIN groups g ON g.id = us.group_id AND g.deleted_at IS NULL
		WHERE us.id = $1 AND us.deleted_at IS NULL
		FOR UPDATE OF us
	`, subscriptionID).Scan(&dailyUsage, &weeklyUsage, &monthlyUsage, &dailyLimit, &weeklyLimit, &monthlyLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.ErrSubscriptionNotFound
	}
	if err != nil {
		return 0, err
	}

	covered := costUSD
	for _, window := range []struct {
		usage float64
		limit sql.NullFloat64
	}{
		{dailyUsage, dailyLimit},
		{weeklyUsage, weeklyLimit},
		{monthlyUsage, monthlyLimit},
	} {
		if !window.limit.Valid {
			continue
		}
		remaining := window.limit.Float64 - window.usage
		if remaining < 0 {
			remaining = 0
		}
		if remaining < covered {
			covered = remaining
		}
	}
	covered = service.QuantizeUsageBillingAmount(covered)

	if _, err := tx.ExecContext(ctx, `
		UPDATE user_subscriptions
		SET daily_usage_usd = daily_usage_usd + $1,
		    weekly_usage_usd = weekly_usage_usd + $1,
		    monthly_usage_usd = monthly_usage_usd + $1,
		    updated_at = NOW()
		WHERE id = $2
	`, covered, subscriptionID); err != nil {
		return 0, err
	}
	return covered, nil
}

func isWalletOperationUserForeignKeyError(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) &&
		pqErr.Code == pq.ErrorCode("23503") &&
		pqErr.Constraint == "redstone_wallet_operations_user_id_fkey"
}

// walletUsageRequestID keeps gateway-provided request IDs within the wallet
// reference/idempotency identifier contract while remaining deterministic.
// The outer usage_billing_dedup table still retains the original request ID.
func walletUsageRequestID(requestID string, apiKeyID int64) string {
	requestID = strings.TrimSpace(requestID)
	const prefix = "usage-billing-token-"
	value := fmt.Sprintf("%d-%s", apiKeyID, requestID)
	if len(prefix)+len(value) <= 128 {
		return prefix + value
	}
	hash := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(hash[:])
}

func incrementUsageBillingAPIKeyQuota(ctx context.Context, tx *sql.Tx, apiKeyID int64, amount float64) (bool, error) {
	var exhausted bool
	err := tx.QueryRowContext(ctx, `
		UPDATE api_keys
		SET quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0
					AND status = $3
					AND quota_used < quota
					AND quota_used + $1 >= quota
				THEN $4
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING quota > 0 AND quota_used >= quota AND quota_used - $1 < quota
	`, amount, apiKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).Scan(&exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrAPIKeyNotFound
	}
	if err != nil {
		return false, err
	}
	return exhausted, nil
}

func incrementUsageBillingAPIKeyRateLimit(ctx context.Context, tx *sql.Tx, apiKeyID int64, cost float64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, cost, apiKeyID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func incrementUsageBillingAccountQuota(ctx context.Context, tx *sql.Tx, accountID int64, amount float64) (*service.AccountQuotaState, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE accounts SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN `+dailyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN `+dailyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_daily_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+dailyExpiredExpr+` AND `+nextDailyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', `+nextDailyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_weekly_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+weeklyExpiredExpr+` AND `+nextWeeklyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', `+nextWeeklyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0),
			COALESCE((extra->>'quota_daily_used')::numeric, 0),
			COALESCE((extra->>'quota_daily_limit')::numeric, 0),
			COALESCE((extra->>'quota_weekly_used')::numeric, 0),
			COALESCE((extra->>'quota_weekly_limit')::numeric, 0)`,
		amount, accountID)
	if err != nil {
		return nil, err
	}

	var state service.AccountQuotaState
	if rows.Next() {
		if err := rows.Scan(
			&state.TotalUsed, &state.TotalLimit,
			&state.DailyUsed, &state.DailyLimit,
			&state.WeeklyUsed, &state.WeeklyLimit,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
	} else {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		return nil, service.ErrAccountNotFound
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// 必须在执行下一条 SQL 前显式关闭 rows：pq 驱动在同一连接上
	// 不允许前一条查询的结果集未耗尽时启动新查询，否则会返回
	// "unexpected Parse response" 错误。
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 任意维度额度在本次递增中从"未超"跨越到"已超"时，必须刷新调度快照，
	// 否则 Redis 中缓存的 Account 仍显示旧的 used 值，后续请求会继续选中本账号，
	// 最终观察到 daily_used / weekly_used 大幅超过配置的 limit。
	// 对于日/周额度，即使本次触发了周期重置（pre=0、post=amount），
	// 判定式 (post-amount) < limit 同样成立，逻辑与总额度保持一致。
	crossedTotal := state.TotalLimit > 0 && state.TotalUsed >= state.TotalLimit && (state.TotalUsed-amount) < state.TotalLimit
	crossedDaily := state.DailyLimit > 0 && state.DailyUsed >= state.DailyLimit && (state.DailyUsed-amount) < state.DailyLimit
	crossedWeekly := state.WeeklyLimit > 0 && state.WeeklyUsed >= state.WeeklyLimit && (state.WeeklyUsed-amount) < state.WeeklyLimit
	if crossedTotal || crossedDaily || crossedWeekly {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			logger.LegacyPrintf("repository.usage_billing", "[SchedulerOutbox] enqueue quota exceeded failed: account=%d err=%v", accountID, err)
			return nil, err
		}
	}
	return &state, nil
}
