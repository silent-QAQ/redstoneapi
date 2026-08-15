package wallet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// PostgresRepository applies wallet changes in one SQL transaction. It owns
// the row lock, idempotency lookup, balance update and append-only ledger
// write so callers cannot observe a partially applied money mutation.
type PostgresRepository struct {
	db *sql.DB
}

func NewPostgresRepository(db *sql.DB) (*PostgresRepository, error) {
	if db == nil {
		return nil, ErrRepositoryRequired
	}
	return &PostgresRepository{db: db}, nil
}

// GetSnapshot returns the current dual-wallet balances without acquiring a
// write lock. Mutating operations still use lockBalances inside their own
// transaction, so callers must not use this snapshot to pre-authorize a debit.
func (r *PostgresRepository) GetSnapshot(ctx context.Context, userID int64) (Snapshot, error) {
	if userID <= 0 {
		return Snapshot{}, ErrInvalidUserID
	}
	var normal, bound decimal.Decimal
	err := r.db.QueryRowContext(ctx, `
		SELECT balance, bound_balance
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`, userID).Scan(&normal, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrInvalidUserID
	}
	if err != nil {
		return Snapshot{}, err
	}
	balances := Balances{Normal: normal, Bound: bound}
	if err := balances.Validate(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{UserID: userID, Balances: balances}, nil
}

func (r *PostgresRepository) Credit(ctx context.Context, request CreditRequest) (_ CreditResult, err error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreditResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	found, err := acquireOperation(ctx, tx, request.UserID, LedgerOperation(request.Reason), request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return CreditResult{}, err
	}
	if found {
		entry, err := findSingleEntry(ctx, tx, request.UserID, request.IdempotencyKey)
		if err != nil {
			return CreditResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CreditResult{}, err
		}
		return CreditResult{Applied: false, BalanceAfter: entry.BalanceAfter, Entry: entry}, nil
	}

	balances, err := lockBalances(ctx, tx, request.UserID)
	if err != nil {
		return CreditResult{}, err
	}
	newBalance := balances.Normal.Add(request.Amount)
	if request.Asset == AssetBound {
		newBalance = balances.Bound.Add(request.Amount)
	}
	if err := updateBalance(ctx, tx, request.UserID, request.Asset, newBalance); err != nil {
		return CreditResult{}, err
	}

	entry := LedgerEntry{
		UserID: request.UserID, Asset: request.Asset, Operation: LedgerOperation(request.Reason),
		Delta: request.Amount, BalanceAfter: newBalance, Reference: request.Reference,
		IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
	}
	if err := insertEntry(ctx, tx, entry, request.Fingerprint()); err != nil {
		return CreditResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditResult{}, err
	}
	return CreditResult{Applied: true, BalanceAfter: newBalance, Entry: entry}, nil
}

// CreditInExecutor joins an Ent-owned (or database/sql-owned) transaction.
// Redeem codes and payment state transitions use it so their business record,
// mutable balance snapshot, immutable operation receipt and ledger entry are
// committed together.
func (r *PostgresRepository) CreditInExecutor(ctx context.Context, executor SQLExecutor, request CreditRequest) (CreditResult, error) {
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	found, err := acquireOperationExecutor(ctx, executor, request.UserID, LedgerOperation(request.Reason), request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return CreditResult{}, err
	}
	if found {
		entry, err := findSingleEntryExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
		if err != nil {
			return CreditResult{}, err
		}
		return CreditResult{Applied: false, BalanceAfter: entry.BalanceAfter, Entry: entry}, nil
	}

	balances, err := lockBalancesExecutor(ctx, executor, request.UserID)
	if err != nil {
		return CreditResult{}, err
	}
	newBalance := balances.Normal.Add(request.Amount)
	if request.Asset == AssetBound {
		newBalance = balances.Bound.Add(request.Amount)
	}
	if err := updateBalanceExecutor(ctx, executor, request.UserID, request.Asset, newBalance); err != nil {
		return CreditResult{}, err
	}
	entry := LedgerEntry{
		UserID: request.UserID, Asset: request.Asset, Operation: LedgerOperation(request.Reason),
		Delta: request.Amount, BalanceAfter: newBalance, Reference: request.Reference,
		IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
	}
	if err := insertEntryExecutor(ctx, executor, entry, request.Fingerprint()); err != nil {
		return CreditResult{}, err
	}
	return CreditResult{Applied: true, BalanceAfter: newBalance, Entry: entry}, nil
}

func (r *PostgresRepository) AdjustNormal(ctx context.Context, request NormalAdjustmentRequest) (_ CreditResult, err error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreditResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.AdjustNormalInExecutor(ctx, tx, request)
	if err != nil {
		return CreditResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditResult{}, err
	}
	return result, nil
}

// AdjustNormalInExecutor is limited to compensation-style ordinary balance
// changes. The row lock computes the delta from the latest balance and rejects
// overdrafts, so an administrator "set" is represented as one auditable
// compensation rather than a stale absolute write.
func (r *PostgresRepository) AdjustNormalInExecutor(ctx context.Context, executor SQLExecutor, request NormalAdjustmentRequest) (CreditResult, error) {
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	found, err := acquireOperationExecutor(ctx, executor, request.UserID, request.Operation, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return CreditResult{}, err
	}
	if found {
		entry, err := findSingleEntryExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
		if err != nil {
			return CreditResult{}, err
		}
		return CreditResult{Applied: false, BalanceAfter: entry.BalanceAfter, Entry: entry}, nil
	}
	balances, err := lockBalancesExecutor(ctx, executor, request.UserID)
	if err != nil {
		return CreditResult{}, err
	}
	newBalance := balances.Normal.Add(request.Delta)
	if newBalance.IsNegative() {
		return CreditResult{}, ErrInsufficientFunds
	}
	if err := updateBalanceExecutor(ctx, executor, request.UserID, AssetNormal, newBalance); err != nil {
		return CreditResult{}, err
	}
	entry := LedgerEntry{
		UserID: request.UserID, Asset: AssetNormal, Operation: request.Operation,
		Delta: request.Delta, BalanceAfter: newBalance, Reference: request.Reference,
		IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
	}
	if err := insertEntryExecutor(ctx, executor, entry, request.Fingerprint()); err != nil {
		return CreditResult{}, err
	}
	return CreditResult{Applied: true, BalanceAfter: newBalance, Entry: entry}, nil
}

func (r *PostgresRepository) DeductNormalAvailable(ctx context.Context, request NormalAvailableDeductionRequest) (_ NormalAvailableDeductionResult, err error) {
	if err := request.Validate(); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.DeductNormalAvailableInExecutor(ctx, tx, request)
	if err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	return result, nil
}

// DeductNormalAvailable is the normal-only partial debit required by forced
// payment refunds. The row lock chooses min(request.Amount, normal balance),
// so a bound balance can neither affect the result nor be converted by it.
func (r *PostgresRepository) DeductNormalAvailableInExecutor(ctx context.Context, executor SQLExecutor, request NormalAvailableDeductionRequest) (NormalAvailableDeductionResult, error) {
	if executor == nil {
		return NormalAvailableDeductionResult{}, ErrTransactionalRepository
	}
	if err := request.Validate(); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	found, err := acquireOperationExecutor(ctx, executor, request.UserID, request.Operation, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	if found {
		entry, err := findSingleEntryExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
		if errors.Is(err, ErrOperationHasNoLedgerEntry) {
			balances, recorded, snapshotErr := operationSnapshotExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
			if snapshotErr != nil {
				return NormalAvailableDeductionResult{}, snapshotErr
			}
			if recorded {
				return NormalAvailableDeductionResult{Applied: false, BalanceAfter: balances.Normal}, nil
			}
			balances, balanceErr := lockBalancesExecutor(ctx, executor, request.UserID)
			if balanceErr != nil {
				return NormalAvailableDeductionResult{}, balanceErr
			}
			return NormalAvailableDeductionResult{Applied: false, BalanceAfter: balances.Normal}, nil
		}
		if err != nil {
			return NormalAvailableDeductionResult{}, err
		}
		return NormalAvailableDeductionResult{Applied: false, Deducted: entry.Delta.Neg(), BalanceAfter: entry.BalanceAfter, Entry: entry}, nil
	}

	balances, err := lockBalancesExecutor(ctx, executor, request.UserID)
	if err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	deducted := decimal.Min(balances.Normal, request.Amount)
	if deducted.IsZero() {
		if err := persistOperationSnapshotExecutor(ctx, executor, request.UserID, request.IdempotencyKey, balances); err != nil {
			return NormalAvailableDeductionResult{}, err
		}
		return NormalAvailableDeductionResult{Applied: true, BalanceAfter: balances.Normal}, nil
	}
	newBalance := balances.Normal.Sub(deducted)
	if err := updateBalanceExecutor(ctx, executor, request.UserID, AssetNormal, newBalance); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	entry := LedgerEntry{
		UserID: request.UserID, Asset: AssetNormal, Operation: request.Operation,
		Delta: deducted.Neg(), BalanceAfter: newBalance, Reference: request.Reference,
		IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
	}
	if err := insertEntryExecutor(ctx, executor, entry, request.Fingerprint()); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	if err := persistOperationSnapshotExecutor(ctx, executor, request.UserID, request.IdempotencyKey, Balances{Normal: newBalance, Bound: balances.Bound}); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	return NormalAvailableDeductionResult{Applied: true, Deducted: deducted, BalanceAfter: newBalance, Entry: entry}, nil
}

func (r *PostgresRepository) SetNormal(ctx context.Context, request SetNormalBalanceRequest) (_ CreditResult, err error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreditResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.SetNormalInExecutor(ctx, tx, request)
	if err != nil {
		return CreditResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditResult{}, err
	}
	return result, nil
}

func (r *PostgresRepository) SetNormalInExecutor(ctx context.Context, executor SQLExecutor, request SetNormalBalanceRequest) (CreditResult, error) {
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	found, err := acquireOperationExecutor(ctx, executor, request.UserID, OperationAdminAdjustment, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return CreditResult{}, err
	}
	if found {
		entry, err := findSingleEntryExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
		if errors.Is(err, ErrOperationHasNoLedgerEntry) {
			balances, recorded, snapshotErr := operationSnapshotExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
			if snapshotErr != nil {
				return CreditResult{}, snapshotErr
			}
			if recorded {
				return CreditResult{Applied: false, BalanceAfter: balances.Normal}, nil
			}
			// Backward compatibility for receipts created before result snapshots
			// were persisted. New no-op commands always return their first snapshot.
			balances, balanceErr := lockBalancesExecutor(ctx, executor, request.UserID)
			if balanceErr != nil {
				return CreditResult{}, balanceErr
			}
			return CreditResult{Applied: false, BalanceAfter: balances.Normal}, nil
		}
		if err != nil {
			return CreditResult{}, err
		}
		return CreditResult{Applied: false, BalanceAfter: entry.BalanceAfter, Entry: entry}, nil
	}
	balances, err := lockBalancesExecutor(ctx, executor, request.UserID)
	if err != nil {
		return CreditResult{}, err
	}
	delta := request.Balance.Sub(balances.Normal)
	if delta.IsZero() {
		// The operation receipt remains so concurrent equal commands serialize,
		// but no ledger row is written because ledger deltas cannot be zero. Its
		// result snapshot makes later idempotent retries stable.
		if err := persistOperationSnapshotExecutor(ctx, executor, request.UserID, request.IdempotencyKey, balances); err != nil {
			return CreditResult{}, err
		}
		return CreditResult{Applied: false, BalanceAfter: balances.Normal}, nil
	}
	if err := updateBalanceExecutor(ctx, executor, request.UserID, AssetNormal, request.Balance); err != nil {
		return CreditResult{}, err
	}
	entry := LedgerEntry{
		UserID: request.UserID, Asset: AssetNormal, Operation: OperationAdminAdjustment,
		Delta: delta, BalanceAfter: request.Balance, Reference: request.Reference,
		IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
	}
	if err := insertEntryExecutor(ctx, executor, entry, request.Fingerprint()); err != nil {
		return CreditResult{}, err
	}
	if err := persistOperationSnapshotExecutor(ctx, executor, request.UserID, request.IdempotencyKey, Balances{Normal: request.Balance, Bound: balances.Bound}); err != nil {
		return CreditResult{}, err
	}
	return CreditResult{Applied: true, BalanceAfter: request.Balance, Entry: entry}, nil
}

func (r *PostgresRepository) ChargeToken(ctx context.Context, request TokenChargeRequest) (_ TokenChargeResult, err error) {
	if err := request.Validate(); err != nil {
		return TokenChargeResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenChargeResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := r.ChargeTokenInExecutor(ctx, tx, request)
	if err != nil {
		return TokenChargeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TokenChargeResult{}, err
	}
	return result, nil
}

// ReserveTokenHold reserves a token-only pre-charge for asynchronous work.
// The ledger records the hold debit immediately; CaptureTokenHold records only
// the compensating release for unused funds, so the net ledger amount is the
// actual charge. This makes failure recovery an auditable compensation rather
// than a mutable balance repair.
func (r *PostgresRepository) ReserveTokenHold(ctx context.Context, request TokenHoldRequest) (_ TokenHoldResult, err error) {
	if err := request.Validate(); err != nil {
		return TokenHoldResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenHoldResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	found, err := acquireOperation(ctx, tx, request.UserID, OperationTokenHold, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return TokenHoldResult{}, err
	}
	if found {
		result, err := replayTokenHold(ctx, tx, request.UserID, request.IdempotencyKey)
		if err != nil {
			return TokenHoldResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return TokenHoldResult{}, err
		}
		return result, nil
	}

	balances, err := lockBalances(ctx, tx, request.UserID)
	if err != nil {
		return TokenHoldResult{}, err
	}
	allocation, err := AllocateTokenDebit(balances, request.Amount)
	if err != nil {
		return TokenHoldResult{}, err
	}
	updated, err := updateHoldBalances(ctx, tx, request.UserID, allocation.NormalBalanceAfter, allocation.BoundBalanceAfter, allocation.Normal)
	if err != nil {
		return TokenHoldResult{}, err
	}

	for _, debit := range []struct {
		asset  AssetType
		amount decimal.Decimal
		after  decimal.Decimal
	}{
		{AssetBound, allocation.Bound, allocation.BoundBalanceAfter},
		{AssetNormal, allocation.Normal, allocation.NormalBalanceAfter},
	} {
		if debit.amount.IsZero() {
			continue
		}
		entry := LedgerEntry{
			UserID: request.UserID, Asset: debit.asset, Operation: OperationTokenHold,
			Delta: debit.amount.Neg(), BalanceAfter: debit.after, Reference: request.Reference,
			IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
		}
		if err := insertEntry(ctx, tx, entry, request.Fingerprint()); err != nil {
			return TokenHoldResult{}, err
		}
	}
	if err := insertTokenHold(ctx, tx, request, allocation, updated); err != nil {
		return TokenHoldResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TokenHoldResult{}, err
	}
	return TokenHoldResult{
		Applied: true, HoldKey: request.IdempotencyKey, State: TokenHoldStateHeld,
		Held: allocation, Balances: updated,
	}, nil
}

// CaptureTokenHold finalizes a successful pre-charge. It releases the unused
// normal portion first because the original hold allocated bound balance first.
// Thus a partial capture preserves the required bound-then-normal spending
// order while a technical release restores each source bucket exactly.
func (r *PostgresRepository) CaptureTokenHold(ctx context.Context, request TokenHoldCaptureRequest) (_ TokenHoldResult, err error) {
	if err := request.Validate(); err != nil {
		return TokenHoldResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenHoldResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	found, err := acquireOperation(ctx, tx, request.UserID, OperationTokenCharge, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return TokenHoldResult{}, err
	}
	if found {
		result, err := replayTokenHold(ctx, tx, request.UserID, request.HoldKey)
		if err != nil {
			return TokenHoldResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return TokenHoldResult{}, err
		}
		return result, nil
	}

	hold, err := lockTokenHold(ctx, tx, request.UserID, request.HoldKey)
	if err != nil {
		return TokenHoldResult{}, err
	}
	if hold.State != TokenHoldStateHeld {
		return TokenHoldResult{}, ErrTokenHoldSettled
	}
	heldAmount := hold.BoundAmount.Add(hold.NormalAmount)
	if request.ActualAmount.GreaterThan(heldAmount) {
		return TokenHoldResult{}, ErrTokenHoldAmountExceeded
	}
	capturedBound := decimal.Min(hold.BoundAmount, request.ActualAmount)
	capturedNormal := request.ActualAmount.Sub(capturedBound)
	releasedBound := hold.BoundAmount.Sub(capturedBound)
	releasedNormal := hold.NormalAmount.Sub(capturedNormal)

	balances, err := lockBalances(ctx, tx, request.UserID)
	if err != nil {
		return TokenHoldResult{}, err
	}
	updated, err := updateHoldBalances(
		ctx,
		tx,
		request.UserID,
		balances.Normal.Add(releasedNormal),
		balances.Bound.Add(releasedBound),
		hold.NormalAmount.Neg(),
	)
	if err != nil {
		return TokenHoldResult{}, err
	}
	if err := insertTokenReleaseEntries(ctx, tx, request.UserID, hold, releasedBound, releasedNormal, updated, request.IdempotencyKey, request.Fingerprint()); err != nil {
		return TokenHoldResult{}, err
	}
	if err := markTokenHoldCaptured(ctx, tx, hold.ID, capturedBound, capturedNormal, updated); err != nil {
		return TokenHoldResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TokenHoldResult{}, err
	}
	return tokenHoldResult(hold, true, TokenHoldStateCaptured, capturedBound, capturedNormal, releasedBound, releasedNormal, updated), nil
}

// ReleaseTokenHold compensates a technical failure before the held request was
// charged. User refunds and withdrawals do not call this method and therefore
// cannot convert or return bound balance.
func (r *PostgresRepository) ReleaseTokenHold(ctx context.Context, request TokenHoldReleaseRequest) (_ TokenHoldResult, err error) {
	if err := request.Validate(); err != nil {
		return TokenHoldResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenHoldResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	found, err := acquireOperation(ctx, tx, request.UserID, OperationTokenRelease, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return TokenHoldResult{}, err
	}
	if found {
		result, err := replayTokenHold(ctx, tx, request.UserID, request.HoldKey)
		if err != nil {
			return TokenHoldResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return TokenHoldResult{}, err
		}
		return result, nil
	}

	hold, err := lockTokenHold(ctx, tx, request.UserID, request.HoldKey)
	if err != nil {
		return TokenHoldResult{}, err
	}
	if hold.State != TokenHoldStateHeld {
		return TokenHoldResult{}, ErrTokenHoldSettled
	}
	balances, err := lockBalances(ctx, tx, request.UserID)
	if err != nil {
		return TokenHoldResult{}, err
	}
	updated, err := updateHoldBalances(
		ctx,
		tx,
		request.UserID,
		balances.Normal.Add(hold.NormalAmount),
		balances.Bound.Add(hold.BoundAmount),
		hold.NormalAmount.Neg(),
	)
	if err != nil {
		return TokenHoldResult{}, err
	}
	if err := insertTokenReleaseEntries(ctx, tx, request.UserID, hold, hold.BoundAmount, hold.NormalAmount, updated, request.IdempotencyKey, request.Fingerprint()); err != nil {
		return TokenHoldResult{}, err
	}
	if err := markTokenHoldReleased(ctx, tx, hold.ID, updated); err != nil {
		return TokenHoldResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TokenHoldResult{}, err
	}
	return tokenHoldResult(hold, true, TokenHoldStateReleased, decimal.Zero, decimal.Zero, hold.BoundAmount, hold.NormalAmount, updated), nil
}

// ChargeTokenTx applies the wallet portion of a token charge in a caller-
// owned transaction. The operation receipt is acquired before the user row
// lock, matching the standalone ChargeToken deadlock/idempotency contract.
func (r *PostgresRepository) ChargeTokenTx(ctx context.Context, tx *sql.Tx, request TokenChargeRequest) (_ TokenChargeResult, err error) {
	if r == nil || tx == nil {
		return TokenChargeResult{}, ErrTransactionalRepository
	}
	return r.ChargeTokenInExecutor(ctx, tx, request)
}

// ChargeTokenInExecutor is the generic transaction-sharing counterpart used
// by Ent-owned transactions. Its SQL only requires the narrow SQLExecutor
// contract, so a usage log and its wallet receipt commit together.
func (r *PostgresRepository) ChargeTokenInExecutor(ctx context.Context, executor SQLExecutor, request TokenChargeRequest) (_ TokenChargeResult, err error) {
	if r == nil || executor == nil {
		return TokenChargeResult{}, ErrTransactionalRepository
	}
	if err := request.Validate(); err != nil {
		return TokenChargeResult{}, err
	}

	found, err := acquireOperationExecutor(ctx, executor, request.UserID, OperationTokenCharge, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return TokenChargeResult{}, err
	}
	if found {
		result, err := replayTokenChargeExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
		if err != nil {
			return TokenChargeResult{}, err
		}
		return result, nil
	}

	balances, err := lockBalancesExecutor(ctx, executor, request.UserID)
	if err != nil {
		return TokenChargeResult{}, err
	}
	allocation, err := AllocateTokenDebit(balances, request.Amount)
	if err != nil {
		return TokenChargeResult{}, err
	}
	if err := updateBothBalancesExecutor(ctx, executor, request.UserID, allocation.NormalBalanceAfter, allocation.BoundBalanceAfter); err != nil {
		return TokenChargeResult{}, err
	}

	entries := make([]LedgerEntry, 0, 2)
	for _, debit := range []struct {
		asset  AssetType
		amount decimal.Decimal
		after  decimal.Decimal
	}{
		{AssetBound, allocation.Bound, allocation.BoundBalanceAfter},
		{AssetNormal, allocation.Normal, allocation.NormalBalanceAfter},
	} {
		if debit.amount.IsZero() {
			continue
		}
		entry := LedgerEntry{
			UserID: request.UserID, Asset: debit.asset, Operation: OperationTokenCharge,
			Delta: debit.amount.Neg(), BalanceAfter: debit.after, Reference: request.Reference,
			IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
		}
		if err := insertEntryExecutor(ctx, executor, entry, request.Fingerprint()); err != nil {
			return TokenChargeResult{}, err
		}
		entries = append(entries, entry)
	}
	return TokenChargeResult{Applied: true, Allocation: allocation, Entries: entries}, nil
}

func (r *PostgresRepository) DebitMarketplace(ctx context.Context, request MarketplaceDebitRequest) (_ CreditResult, err error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreditResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.DebitMarketplaceInExecutor(ctx, tx, request)
	if err != nil {
		return CreditResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditResult{}, err
	}
	return result, nil
}

func (r *PostgresRepository) DebitMarketplaceInExecutor(ctx context.Context, executor SQLExecutor, request MarketplaceDebitRequest) (CreditResult, error) {
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}

	found, err := acquireOperationExecutor(ctx, executor, request.UserID, OperationMarketplaceDebit, request.IdempotencyKey, request.Fingerprint())
	if err != nil {
		return CreditResult{}, err
	}
	if found {
		entry, err := findSingleEntryExecutor(ctx, executor, request.UserID, request.IdempotencyKey)
		if err != nil {
			return CreditResult{}, err
		}
		return CreditResult{Applied: false, BalanceAfter: entry.BalanceAfter, Entry: entry}, nil
	}

	balances, err := lockBalancesExecutor(ctx, executor, request.UserID)
	if err != nil {
		return CreditResult{}, err
	}
	if balances.Normal.LessThan(request.Amount) {
		return CreditResult{}, ErrInsufficientFunds
	}
	newBalance := balances.Normal.Sub(request.Amount)
	if err := updateBalanceExecutor(ctx, executor, request.UserID, AssetNormal, newBalance); err != nil {
		return CreditResult{}, err
	}
	entry := LedgerEntry{
		UserID: request.UserID, Asset: AssetNormal, Operation: OperationMarketplaceDebit,
		Delta: request.Amount.Neg(), BalanceAfter: newBalance, Reference: request.Reference,
		IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(),
	}
	if err := insertEntryExecutor(ctx, executor, entry, request.Fingerprint()); err != nil {
		return CreditResult{}, err
	}
	return CreditResult{Applied: true, BalanceAfter: newBalance, Entry: entry}, nil
}

func (r *PostgresRepository) ListLedger(ctx context.Context, userID int64, limit, offset int) (LedgerPage, error) {
	if userID <= 0 {
		return LedgerPage{}, ErrInvalidUserID
	}
	if limit <= 0 || limit > 200 || offset < 0 {
		return LedgerPage{}, fmt.Errorf("invalid ledger pagination")
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_wallet_ledger WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return LedgerPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, asset_type, operation, delta, balance_after, reference_type, reference_id, idempotency_key, created_at
		FROM redstone_wallet_ledger
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return LedgerPage{}, err
	}
	defer rows.Close()
	page := LedgerPage{Entries: make([]LedgerEntry, 0), Total: total}
	for rows.Next() {
		var entry LedgerEntry
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.Asset, &entry.Operation, &entry.Delta, &entry.BalanceAfter, &entry.Reference.Type, &entry.Reference.ID, &entry.IdempotencyKey, &entry.CreatedAt); err != nil {
			return LedgerPage{}, err
		}
		if err := entry.Validate(); err != nil {
			return LedgerPage{}, err
		}
		page.Entries = append(page.Entries, entry)
	}
	return page, rows.Err()
}

type tokenHoldRecord struct {
	ID             int64
	UserID         int64
	HoldKey        string
	Reference      Reference
	BoundAmount    decimal.Decimal
	NormalAmount   decimal.Decimal
	CapturedBound  decimal.Decimal
	CapturedNormal decimal.Decimal
	State          TokenHoldState
	Balances       Balances
	HasSnapshot    bool
}

func insertTokenHold(ctx context.Context, tx *sql.Tx, request TokenHoldRequest, allocation DebitAllocation, balances Balances) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_wallet_token_holds (
			user_id, hold_key, reference_type, reference_id,
			bound_amount, normal_amount, normal_balance_after, bound_balance_after, state
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, request.UserID, request.IdempotencyKey, request.Reference.Type, request.Reference.ID,
		allocation.Bound, allocation.Normal, balances.Normal, balances.Bound, TokenHoldStateHeld)
	return err
}

func lockTokenHold(ctx context.Context, tx *sql.Tx, userID int64, holdKey string) (tokenHoldRecord, error) {
	return queryTokenHold(ctx, tx, userID, holdKey, true)
}

func queryTokenHold(ctx context.Context, tx *sql.Tx, userID int64, holdKey string, forUpdate bool) (tokenHoldRecord, error) {
	query := `
		SELECT id, user_id, hold_key, reference_type, reference_id,
			bound_amount, normal_amount, captured_bound_amount, captured_normal_amount,
			normal_balance_after::TEXT, bound_balance_after::TEXT, state
		FROM redstone_wallet_token_holds
		WHERE user_id = $1 AND hold_key = $2
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var hold tokenHoldRecord
	var normalAfter, boundAfter sql.NullString
	err := tx.QueryRowContext(ctx, query, userID, holdKey).Scan(
		&hold.ID, &hold.UserID, &hold.HoldKey, &hold.Reference.Type, &hold.Reference.ID,
		&hold.BoundAmount, &hold.NormalAmount, &hold.CapturedBound, &hold.CapturedNormal,
		&normalAfter, &boundAfter, &hold.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return tokenHoldRecord{}, ErrTokenHoldNotFound
	}
	if err != nil {
		return tokenHoldRecord{}, err
	}
	if normalAfter.Valid && boundAfter.Valid {
		normal, parseErr := decimal.NewFromString(normalAfter.String)
		if parseErr != nil {
			return tokenHoldRecord{}, parseErr
		}
		bound, parseErr := decimal.NewFromString(boundAfter.String)
		if parseErr != nil {
			return tokenHoldRecord{}, parseErr
		}
		hold.Balances = Balances{Normal: normal, Bound: bound}
		hold.HasSnapshot = true
	}
	return hold, nil
}

func replayTokenHold(ctx context.Context, tx *sql.Tx, userID int64, holdKey string) (TokenHoldResult, error) {
	hold, err := queryTokenHold(ctx, tx, userID, holdKey, false)
	if err != nil {
		return TokenHoldResult{}, err
	}
	balances := hold.Balances
	if !hold.HasSnapshot {
		// Holds created before result snapshots were introduced retain the
		// previous replay behavior; newly-created holds never take this path.
		var err error
		balances, err = lockBalances(ctx, tx, userID)
		if err != nil {
			return TokenHoldResult{}, err
		}
	}
	releasedBound := decimal.Zero
	releasedNormal := decimal.Zero
	if hold.State == TokenHoldStateReleased {
		releasedBound = hold.BoundAmount
		releasedNormal = hold.NormalAmount
	} else if hold.State == TokenHoldStateCaptured {
		releasedBound = hold.BoundAmount.Sub(hold.CapturedBound)
		releasedNormal = hold.NormalAmount.Sub(hold.CapturedNormal)
	}
	return tokenHoldResult(hold, false, hold.State, hold.CapturedBound, hold.CapturedNormal, releasedBound, releasedNormal, balances), nil
}

func tokenHoldResult(
	hold tokenHoldRecord,
	applied bool,
	state TokenHoldState,
	capturedBound, capturedNormal, releasedBound, releasedNormal decimal.Decimal,
	balances Balances,
) TokenHoldResult {
	return TokenHoldResult{
		Applied: applied,
		HoldKey: hold.HoldKey,
		State:   state,
		Held: DebitAllocation{
			Bound: hold.BoundAmount, Normal: hold.NormalAmount,
		},
		Captured: DebitAllocation{Bound: capturedBound, Normal: capturedNormal},
		Released: DebitAllocation{Bound: releasedBound, Normal: releasedNormal},
		Balances: balances,
	}
}

func markTokenHoldCaptured(ctx context.Context, tx *sql.Tx, holdID int64, bound, normal decimal.Decimal, balances Balances) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_wallet_token_holds
		SET state = $1, captured_bound_amount = $2, captured_normal_amount = $3,
			normal_balance_after = $4, bound_balance_after = $5, settled_at = NOW()
		WHERE id = $6 AND state = $7
	`, TokenHoldStateCaptured, bound, normal, balances.Normal, balances.Bound, holdID, TokenHoldStateHeld)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTokenHoldSettled
	}
	return nil
}

func markTokenHoldReleased(ctx context.Context, tx *sql.Tx, holdID int64, balances Balances) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_wallet_token_holds
		SET state = $1, normal_balance_after = $2, bound_balance_after = $3, settled_at = NOW()
		WHERE id = $4 AND state = $5
	`, TokenHoldStateReleased, balances.Normal, balances.Bound, holdID, TokenHoldStateHeld)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTokenHoldSettled
	}
	return nil
}

func insertTokenReleaseEntries(
	ctx context.Context,
	tx *sql.Tx,
	userID int64,
	hold tokenHoldRecord,
	bound, normal decimal.Decimal,
	balances Balances,
	idempotencyKey, fingerprint string,
) error {
	for _, credit := range []struct {
		asset  AssetType
		amount decimal.Decimal
		after  decimal.Decimal
	}{
		{AssetBound, bound, balances.Bound},
		{AssetNormal, normal, balances.Normal},
	} {
		if credit.amount.IsZero() {
			continue
		}
		if err := insertEntry(ctx, tx, LedgerEntry{
			UserID: userID, Asset: credit.asset, Operation: OperationTokenRelease,
			Delta: credit.amount, BalanceAfter: credit.after, Reference: hold.Reference,
			IdempotencyKey: idempotencyKey, CreatedAt: time.Now().UTC(),
		}, fingerprint); err != nil {
			return err
		}
	}
	return nil
}

func lockBalances(ctx context.Context, tx *sql.Tx, userID int64) (Balances, error) {
	var normal, bound decimal.Decimal
	err := tx.QueryRowContext(ctx, `
		SELECT balance, bound_balance
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, userID).Scan(&normal, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return Balances{}, ErrInvalidUserID
	}
	if err != nil {
		return Balances{}, err
	}
	return Balances{Normal: normal, Bound: bound}, nil
}

func updateBalance(ctx context.Context, tx *sql.Tx, userID int64, asset AssetType, amount decimal.Decimal) error {
	column := "balance"
	if asset == AssetBound {
		column = "bound_balance"
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET `+column+` = $1, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL`, amount, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInvalidUserID
	}
	return nil
}

func updateBothBalances(ctx context.Context, tx *sql.Tx, userID int64, normal, bound decimal.Decimal) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE users
		SET balance = $1, bound_balance = $2, updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL
	`, normal, bound, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInvalidUserID
	}
	return nil
}

// updateHoldBalances changes available balance and the legacy normal frozen
// counter together. Bound holds deliberately have no transferable frozen
// bucket: their exact allocation is retained in redstone_wallet_token_holds.
func updateHoldBalances(ctx context.Context, tx *sql.Tx, userID int64, normal, bound, frozenDelta decimal.Decimal) (Balances, error) {
	var updated Balances
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = $1,
			bound_balance = $2,
			frozen_balance = COALESCE(frozen_balance, 0) + $3,
			updated_at = NOW()
		WHERE id = $4
			AND deleted_at IS NULL
			AND COALESCE(frozen_balance, 0) + $3 >= 0
		RETURNING balance, bound_balance
	`, normal, bound, frozenDelta, userID).Scan(&updated.Normal, &updated.Bound)
	if errors.Is(err, sql.ErrNoRows) {
		return Balances{}, ErrInvalidUserID
	}
	if err != nil {
		return Balances{}, err
	}
	return updated, nil
}

func insertEntry(ctx context.Context, tx *sql.Tx, entry LedgerEntry, fingerprint string) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_wallet_ledger
			(user_id, asset_type, operation, delta, balance_after, reference_type, reference_id, idempotency_key, request_fingerprint, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, entry.UserID, entry.Asset, entry.Operation, entry.Delta, entry.BalanceAfter, entry.Reference.Type, entry.Reference.ID, entry.IdempotencyKey, fingerprint, entry.CreatedAt)
	return err
}

// acquireOperation serializes equal idempotency requests before a user-row lock.
// The INSERT ... ON CONFLICT path waits for a concurrent uncommitted receipt;
// FOR UPDATE then makes the retry inspect a stable completed command. The
// caller must roll back on any failure so an incomplete receipt is removed.
func acquireOperation(ctx context.Context, tx *sql.Tx, userID int64, operation LedgerOperation, idempotencyKey, fingerprint string) (bool, error) {
	inserted := false
	err := tx.QueryRowContext(ctx, `
		INSERT INTO redstone_wallet_operations (user_id, operation, idempotency_key, request_fingerprint)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING true
	`, userID, operation, idempotencyKey, fingerprint).Scan(&inserted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		inserted = false
	}

	var existingFingerprint string
	var existingOperation LedgerOperation
	if err := tx.QueryRowContext(ctx, `
		SELECT operation, request_fingerprint
		FROM redstone_wallet_operations
		WHERE user_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, userID, idempotencyKey).Scan(&existingOperation, &existingFingerprint); err != nil {
		return false, err
	}
	if existingOperation != operation || existingFingerprint != fingerprint {
		return false, ErrIdempotencyConflict
	}
	if inserted {
		return false, nil
	}

	return true, nil
}

func acquireOperationExecutor(ctx context.Context, executor SQLExecutor, userID int64, operation LedgerOperation, idempotencyKey, fingerprint string) (bool, error) {
	inserted := false
	found, err := scanExecutorRow(ctx, executor, `
		INSERT INTO redstone_wallet_operations (user_id, operation, idempotency_key, request_fingerprint)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING true
	`, []any{userID, operation, idempotencyKey, fingerprint}, &inserted)
	if err != nil {
		return false, err
	}
	if found {
		var existingOperation LedgerOperation
		var existingFingerprint string
		found, err = scanExecutorRow(ctx, executor, `
			SELECT operation, request_fingerprint
			FROM redstone_wallet_operations
			WHERE user_id = $1 AND idempotency_key = $2
			FOR UPDATE
		`, []any{userID, idempotencyKey}, &existingOperation, &existingFingerprint)
		if err != nil {
			return false, err
		}
		if !found {
			return false, ErrIdempotencyConflict
		}
		if existingOperation != operation || existingFingerprint != fingerprint {
			return false, ErrIdempotencyConflict
		}
		return false, nil
	}

	var existingOperation LedgerOperation
	var existingFingerprint string
	found, err = scanExecutorRow(ctx, executor, `
		SELECT operation, request_fingerprint
		FROM redstone_wallet_operations
		WHERE user_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, []any{userID, idempotencyKey}, &existingOperation, &existingFingerprint)
	if err != nil {
		return false, err
	}
	if !found || existingOperation != operation || existingFingerprint != fingerprint {
		return false, ErrIdempotencyConflict
	}
	return true, nil
}

func scanExecutorRow(ctx context.Context, executor SQLExecutor, query string, args []any, dest ...any) (bool, error) {
	rows, err := executor.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := rows.Scan(dest...); err != nil {
		return false, err
	}
	return true, rows.Err()
}

func lockBalancesExecutor(ctx context.Context, executor SQLExecutor, userID int64) (Balances, error) {
	var balances Balances
	found, err := scanExecutorRow(ctx, executor, `
		SELECT balance, bound_balance
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, []any{userID}, &balances.Normal, &balances.Bound)
	if err != nil {
		return Balances{}, err
	}
	if !found {
		return Balances{}, ErrInvalidUserID
	}
	return balances, nil
}

func updateBalanceExecutor(ctx context.Context, executor SQLExecutor, userID int64, asset AssetType, amount decimal.Decimal) error {
	column := "balance"
	if asset == AssetBound {
		column = "bound_balance"
	}
	result, err := executor.ExecContext(ctx, `UPDATE users SET `+column+` = $1, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL`, amount, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInvalidUserID
	}
	return nil
}

func updateBothBalancesExecutor(ctx context.Context, executor SQLExecutor, userID int64, normal, bound decimal.Decimal) error {
	result, err := executor.ExecContext(ctx, `
		UPDATE users
		SET balance = $1, bound_balance = $2, updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL
	`, normal, bound, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInvalidUserID
	}
	return nil
}

func insertEntryExecutor(ctx context.Context, executor SQLExecutor, entry LedgerEntry, fingerprint string) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	_, err := executor.ExecContext(ctx, `
		INSERT INTO redstone_wallet_ledger
			(user_id, asset_type, operation, delta, balance_after, reference_type, reference_id, idempotency_key, request_fingerprint, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, entry.UserID, entry.Asset, entry.Operation, entry.Delta, entry.BalanceAfter, entry.Reference.Type, entry.Reference.ID, entry.IdempotencyKey, fingerprint, entry.CreatedAt)
	return err
}

// persistOperationSnapshotExecutor records the durable result for commands
// that may not emit a ledger row (for example a zero available-balance debit).
// It is also written for non-zero commands so every new receipt has one stable
// replay snapshot independent of later wallet activity.
func persistOperationSnapshotExecutor(ctx context.Context, executor SQLExecutor, userID int64, idempotencyKey string, balances Balances) error {
	if err := balances.Validate(); err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
		UPDATE redstone_wallet_operations
		SET result_normal_after = $1, result_bound_after = $2
		WHERE user_id = $3 AND idempotency_key = $4
			AND result_normal_after IS NULL AND result_bound_after IS NULL
	`, balances.Normal, balances.Bound, userID, idempotencyKey)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrIdempotencyConflict
	}
	return nil
}

func operationSnapshotExecutor(ctx context.Context, executor SQLExecutor, userID int64, idempotencyKey string) (Balances, bool, error) {
	var normalAfter, boundAfter sql.NullString
	found, err := scanExecutorRow(ctx, executor, `
		SELECT result_normal_after::TEXT, result_bound_after::TEXT
		FROM redstone_wallet_operations
		WHERE user_id = $1 AND idempotency_key = $2
	`, []any{userID, idempotencyKey}, &normalAfter, &boundAfter)
	if err != nil || !found || !normalAfter.Valid || !boundAfter.Valid {
		return Balances{}, false, err
	}
	normal, err := decimal.NewFromString(normalAfter.String)
	if err != nil {
		return Balances{}, false, err
	}
	bound, err := decimal.NewFromString(boundAfter.String)
	if err != nil {
		return Balances{}, false, err
	}
	balances := Balances{Normal: normal, Bound: bound}
	if err := balances.Validate(); err != nil {
		return Balances{}, false, err
	}
	return balances, true, nil
}

func findSingleEntryExecutor(ctx context.Context, executor SQLExecutor, userID int64, idempotencyKey string) (LedgerEntry, error) {
	var entry LedgerEntry
	found, err := scanExecutorRow(ctx, executor, `
		SELECT id, user_id, asset_type, operation, delta, balance_after, reference_type, reference_id, idempotency_key, created_at
		FROM redstone_wallet_ledger
		WHERE user_id = $1 AND idempotency_key = $2
		ORDER BY id
		LIMIT 1
	`, []any{userID, idempotencyKey}, &entry.ID, &entry.UserID, &entry.Asset, &entry.Operation, &entry.Delta, &entry.BalanceAfter, &entry.Reference.Type, &entry.Reference.ID, &entry.IdempotencyKey, &entry.CreatedAt)
	if err != nil {
		return LedgerEntry{}, err
	}
	if !found {
		return LedgerEntry{}, ErrOperationHasNoLedgerEntry
	}
	if err := entry.Validate(); err != nil {
		return LedgerEntry{}, err
	}
	return entry, nil
}

func findSingleEntry(ctx context.Context, tx *sql.Tx, userID int64, idempotencyKey string) (LedgerEntry, error) {
	var entry LedgerEntry
	err := tx.QueryRowContext(ctx, `
		SELECT id, user_id, asset_type, operation, delta, balance_after, reference_type, reference_id, idempotency_key, created_at
		FROM redstone_wallet_ledger
		WHERE user_id = $1 AND idempotency_key = $2
		ORDER BY id
		LIMIT 1
	`, userID, idempotencyKey).Scan(&entry.ID, &entry.UserID, &entry.Asset, &entry.Operation, &entry.Delta, &entry.BalanceAfter, &entry.Reference.Type, &entry.Reference.ID, &entry.IdempotencyKey, &entry.CreatedAt)
	if err != nil {
		return LedgerEntry{}, err
	}
	if err := entry.Validate(); err != nil {
		return LedgerEntry{}, err
	}
	return entry, nil
}

func replayTokenCharge(ctx context.Context, tx *sql.Tx, userID int64, idempotencyKey string) (TokenChargeResult, error) {
	return replayTokenChargeExecutor(ctx, tx, userID, idempotencyKey)
}

func replayTokenChargeExecutor(ctx context.Context, executor SQLExecutor, userID int64, idempotencyKey string) (TokenChargeResult, error) {
	rows, err := executor.QueryContext(ctx, `
		SELECT id, user_id, asset_type, operation, delta, balance_after, reference_type, reference_id, idempotency_key, created_at
		FROM redstone_wallet_ledger
		WHERE user_id = $1 AND idempotency_key = $2
		ORDER BY id
	`, userID, idempotencyKey)
	if err != nil {
		return TokenChargeResult{}, err
	}
	defer rows.Close()

	result := TokenChargeResult{Applied: false, Entries: make([]LedgerEntry, 0, 2)}
	for rows.Next() {
		var entry LedgerEntry
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.Asset, &entry.Operation, &entry.Delta, &entry.BalanceAfter, &entry.Reference.Type, &entry.Reference.ID, &entry.IdempotencyKey, &entry.CreatedAt); err != nil {
			return TokenChargeResult{}, err
		}
		if err := entry.Validate(); err != nil {
			return TokenChargeResult{}, err
		}
		if entry.Operation != OperationTokenCharge {
			return TokenChargeResult{}, ErrIdempotencyConflict
		}
		result.Entries = append(result.Entries, entry)
		switch entry.Asset {
		case AssetBound:
			result.Allocation.Bound = entry.Delta.Neg()
			result.Allocation.BoundBalanceAfter = entry.BalanceAfter
		case AssetNormal:
			result.Allocation.Normal = entry.Delta.Neg()
			result.Allocation.NormalBalanceAfter = entry.BalanceAfter
		default:
			return TokenChargeResult{}, ErrIdempotencyConflict
		}
	}
	if err := rows.Err(); err != nil {
		return TokenChargeResult{}, err
	}
	if len(result.Entries) == 0 {
		return TokenChargeResult{}, fmt.Errorf("wallet idempotency receipt missing ledger entries")
	}
	return result, nil
}
