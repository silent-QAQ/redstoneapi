// Package wallet contains the Redstone wallet domain contract.
//
// It deliberately has no dependency on Ent, HTTP, or an existing sub2api
// service. Adapters own database transactions; this package owns the wallet
// invariants that every adapter must preserve.
package wallet

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const MonetaryScale int32 = 8

var (
	ErrRepositoryRequired        = errors.New("wallet repository is required")
	ErrInvalidUserID             = errors.New("wallet user id must be positive")
	ErrInvalidAmount             = errors.New("wallet amount must be positive and quantized to 8 decimal places")
	ErrInvalidBalance            = errors.New("wallet balance cannot be negative")
	ErrInvalidAsset              = errors.New("wallet asset is invalid")
	ErrInvalidReference          = errors.New("wallet reference type and id are required")
	ErrIdempotencyKeyRequired    = errors.New("wallet idempotency key is required")
	ErrInsufficientFunds         = errors.New("wallet insufficient funds")
	ErrIdempotencyConflict       = errors.New("wallet idempotency key conflicts with an existing request")
	ErrOperationHasNoLedgerEntry = errors.New("wallet operation has no ledger entry")
	ErrTransactionalRepository   = errors.New("wallet repository does not support shared transactions")
	ErrBoundBalanceCreditSource  = errors.New("bound balance may only be credited by an administrator grant or redeem code")
	ErrBoundBalanceSpend         = errors.New("bound balance may only be used for token charges")
	ErrInvalidLedgerEntry        = errors.New("wallet ledger entry is invalid")
	ErrTokenHoldNotFound         = errors.New("wallet token hold was not found")
	ErrTokenHoldSettled          = errors.New("wallet token hold has already been settled")
	ErrTokenHoldAmountExceeded   = errors.New("wallet token hold capture exceeds the held amount")
)

// AssetType identifies a separately accounted wallet balance.
type AssetType string

const (
	AssetNormal AssetType = "normal"
	AssetBound  AssetType = "bound"
)

func (a AssetType) Valid() bool {
	return a == AssetNormal || a == AssetBound
}

// SpendPurpose is intentionally distinct from a ledger operation. It is used
// at a policy boundary before a debit request reaches persistence.
type SpendPurpose string

const (
	SpendToken       SpendPurpose = "token"
	SpendWithdrawal  SpendPurpose = "withdrawal"
	SpendMarketplace SpendPurpose = "marketplace"
)

// CreditReason becomes the immutable ledger operation for a credit entry.
type CreditReason string

const (
	CreditAdminGrant     CreditReason = "admin_grant"
	CreditRedeemCode     CreditReason = "redeem_code"
	CreditPayment        CreditReason = "payment"
	CreditSettlement     CreditReason = "settlement"
	CreditRefund         CreditReason = "refund"
	CreditPromoCode      CreditReason = "promo_code"
	CreditProviderGrant  CreditReason = "provider_grant"
	CreditReferralReward CreditReason = "referral_reward"
	CreditActivityReward CreditReason = "activity_reward"
	CreditOpeningBalance CreditReason = "opening_balance"
)

func (r CreditReason) Valid() bool {
	switch r {
	case CreditAdminGrant, CreditRedeemCode, CreditPayment, CreditSettlement, CreditRefund,
		CreditPromoCode, CreditProviderGrant, CreditReferralReward, CreditActivityReward,
		CreditOpeningBalance:
		return true
	default:
		return false
	}
}

// LedgerOperation describes why a ledger entry exists. Ledger rows are append
// only; corrections must be represented by a compensating new row.
type LedgerOperation string

const (
	OperationAdminGrant       LedgerOperation = LedgerOperation(CreditAdminGrant)
	OperationRedeemCode       LedgerOperation = LedgerOperation(CreditRedeemCode)
	OperationPayment          LedgerOperation = LedgerOperation(CreditPayment)
	OperationSettlement       LedgerOperation = LedgerOperation(CreditSettlement)
	OperationRefund           LedgerOperation = LedgerOperation(CreditRefund)
	OperationPromoCode        LedgerOperation = LedgerOperation(CreditPromoCode)
	OperationProviderGrant    LedgerOperation = LedgerOperation(CreditProviderGrant)
	OperationReferralReward   LedgerOperation = LedgerOperation(CreditReferralReward)
	OperationActivityReward   LedgerOperation = LedgerOperation(CreditActivityReward)
	OperationOpeningBalance   LedgerOperation = LedgerOperation(CreditOpeningBalance)
	OperationAdminAdjustment  LedgerOperation = "admin_adjustment"
	OperationTokenCharge      LedgerOperation = "token_charge"
	OperationTokenHold        LedgerOperation = "token_hold"
	OperationTokenRelease     LedgerOperation = "token_release"
	OperationMarketplaceDebit LedgerOperation = "marketplace_debit"
	OperationWithdrawal       LedgerOperation = "withdrawal"
)

func (o LedgerOperation) Valid() bool {
	switch o {
	case OperationAdminGrant, OperationRedeemCode, OperationPayment, OperationSettlement,
		OperationRefund, OperationPromoCode, OperationProviderGrant, OperationReferralReward,
		OperationActivityReward, OperationOpeningBalance, OperationAdminAdjustment,
		OperationTokenCharge, OperationTokenHold, OperationTokenRelease, OperationMarketplaceDebit,
		OperationWithdrawal:
		return true
	default:
		return false
	}
}

// Reference links a wallet mutation to its source business record. It is part
// of the repository's uniqueness contract, not user-display metadata.
type Reference struct {
	Type string
	ID   string
}

func (r Reference) Validate() error {
	if !validIdentifier(r.Type, 64) || !validIdentifier(r.ID, 128) {
		return ErrInvalidReference
	}
	return nil
}

// LedgerEntry is the portable representation of an immutable ledger row.
// BalanceAfter is the post-mutation balance of Asset, enabling reconciliation
// without reading a mutable user row at a later point in time.
type LedgerEntry struct {
	ID             int64
	UserID         int64
	Asset          AssetType
	Operation      LedgerOperation
	Delta          decimal.Decimal
	BalanceAfter   decimal.Decimal
	Reference      Reference
	IdempotencyKey string
	CreatedAt      time.Time
}

func (e LedgerEntry) Validate() error {
	if e.UserID <= 0 || !e.Asset.Valid() || !e.Operation.Valid() ||
		!isQuantizedNonZero(e.Delta) || !isQuantized(e.BalanceAfter) ||
		e.Reference.Validate() != nil || !validIdentifier(e.IdempotencyKey, 128) || e.CreatedAt.IsZero() {
		return ErrInvalidLedgerEntry
	}
	// Existing sub2 deployments may carry a negative legacy normal balance.
	// It is accepted only as an opening snapshot; all new wallet debits reject
	// insufficient funds and therefore cannot create a negative post-balance.
	if e.BalanceAfter.IsNegative() && !(e.Asset == AssetNormal && e.Operation == OperationOpeningBalance) {
		return ErrInvalidLedgerEntry
	}
	if e.Asset == AssetBound && e.Operation != OperationAdminGrant && e.Operation != OperationRedeemCode &&
		e.Operation != OperationTokenCharge && e.Operation != OperationTokenHold && e.Operation != OperationTokenRelease {
		return ErrInvalidLedgerEntry
	}
	if e.Operation == OperationTokenHold && !e.Delta.IsNegative() {
		return ErrInvalidLedgerEntry
	}
	if e.Operation == OperationTokenRelease && !e.Delta.IsPositive() {
		return ErrInvalidLedgerEntry
	}
	if e.Operation == OperationOpeningBalance && e.Asset != AssetNormal {
		return ErrInvalidLedgerEntry
	}
	if e.Operation == OperationMarketplaceDebit && (e.Asset != AssetNormal || !e.Delta.IsNegative()) {
		return ErrInvalidLedgerEntry
	}
	if e.Operation == OperationWithdrawal && (e.Asset != AssetNormal || !e.Delta.IsNegative()) {
		return ErrInvalidLedgerEntry
	}
	if (e.Operation == OperationReferralReward || e.Operation == OperationActivityReward) &&
		(e.Asset != AssetNormal || !e.Delta.IsPositive()) {
		return ErrInvalidLedgerEntry
	}
	return nil
}

// CanSpend exposes the enforced asset policy for callers such as withdrawals
// and the marketplace. Only token charging may consume bound balance.
func CanSpend(asset AssetType, purpose SpendPurpose) bool {
	switch asset {
	case AssetNormal:
		return purpose == SpendToken || purpose == SpendWithdrawal || purpose == SpendMarketplace
	case AssetBound:
		return purpose == SpendToken
	default:
		return false
	}
}

func ValidateSpend(asset AssetType, purpose SpendPurpose) error {
	if !asset.Valid() {
		return ErrInvalidAsset
	}
	if !CanSpend(asset, purpose) {
		return ErrBoundBalanceSpend
	}
	return nil
}

// ValidateCreditPolicy enforces the non-transferable nature of bound balance.
func ValidateCreditPolicy(asset AssetType, reason CreditReason) error {
	if !asset.Valid() || !reason.Valid() {
		return ErrInvalidAsset
	}
	if asset == AssetBound && reason != CreditAdminGrant && reason != CreditRedeemCode {
		return ErrBoundBalanceCreditSource
	}
	return nil
}

// Balances is a wallet snapshot obtained under the repository's row lock.
type Balances struct {
	Normal decimal.Decimal
	Bound  decimal.Decimal
}

type Snapshot struct {
	UserID   int64
	Balances Balances
}

func (b Balances) Validate() error {
	if !isQuantized(b.Normal) || !isQuantized(b.Bound) || b.Normal.IsNegative() || b.Bound.IsNegative() {
		return ErrInvalidBalance
	}
	return nil
}

// DebitAllocation contains the fixed post-subscription token debit order.
// Subscription quota is deliberately outside this package: callers invoke this
// wallet path only after subscription quota has been exhausted or is absent.
type DebitAllocation struct {
	Bound              decimal.Decimal
	Normal             decimal.Decimal
	BoundBalanceAfter  decimal.Decimal
	NormalBalanceAfter decimal.Decimal
}

// AllocateTokenDebit allocates a token charge in the mandated bound -> normal
// order. It is deterministic and must be called by repository adapters while
// the user's wallet row is locked.
func AllocateTokenDebit(balances Balances, amount decimal.Decimal) (DebitAllocation, error) {
	if err := balances.Validate(); err != nil {
		return DebitAllocation{}, err
	}
	if !isQuantizedPositive(amount) {
		return DebitAllocation{}, ErrInvalidAmount
	}
	if balances.Bound.Add(balances.Normal).LessThan(amount) {
		return DebitAllocation{}, ErrInsufficientFunds
	}

	bound := decimal.Min(balances.Bound, amount)
	normal := amount.Sub(bound)
	return DebitAllocation{
		Bound:              bound,
		Normal:             normal,
		BoundBalanceAfter:  balances.Bound.Sub(bound),
		NormalBalanceAfter: balances.Normal.Sub(normal),
	}, nil
}

// CreditRequest represents one explicit asset credit. Normal credits support
// all known credit reasons; bound credits only accept admin_grant/redeem_code.
type CreditRequest struct {
	UserID         int64
	Asset          AssetType
	Amount         decimal.Decimal
	Reason         CreditReason
	Reference      Reference
	IdempotencyKey string
}

func (r CreditRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !isQuantizedPositive(r.Amount) {
		return ErrInvalidAmount
	}
	if err := ValidateCreditPolicy(r.Asset, r.Reason); err != nil {
		return err
	}
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r CreditRequest) Fingerprint() string {
	return fingerprint(
		"credit", fmt.Sprint(r.UserID), string(r.Asset), amountFingerprintValue(r.Amount),
		string(r.Reason), r.Reference.Type, r.Reference.ID,
	)
}

// TokenChargeRequest represents the wallet portion of an API token charge.
// It is always split by AllocateTokenDebit, never by the caller.
type TokenChargeRequest struct {
	UserID         int64
	Amount         decimal.Decimal
	Reference      Reference
	IdempotencyKey string
}

func (r TokenChargeRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !isQuantizedPositive(r.Amount) {
		return ErrInvalidAmount
	}
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r TokenChargeRequest) Fingerprint() string {
	return fingerprint(
		"token_charge", fmt.Sprint(r.UserID), amountFingerprintValue(r.Amount), r.Reference.Type, r.Reference.ID,
	)
}

// TokenHoldRequest reserves a token charge before an asynchronous API task is
// dispatched. The allocation is persisted by asset so a technical failure can
// return funds to the exact source bucket. A hold is a token-only operation;
// it is never used for user refunds, withdrawals, or marketplace payments.
type TokenHoldRequest struct {
	UserID             int64
	Amount             decimal.Decimal
	Reference          Reference
	IdempotencyKey     string
	RequestFingerprint string
}

func (r TokenHoldRequest) Validate() error {
	return TokenChargeRequest{
		UserID:         r.UserID,
		Amount:         r.Amount,
		Reference:      r.Reference,
		IdempotencyKey: r.IdempotencyKey,
	}.Validate()
}

func (r TokenHoldRequest) Fingerprint() string {
	if r.RequestFingerprint != "" {
		return r.RequestFingerprint
	}
	return fingerprint(
		"token_hold", fmt.Sprint(r.UserID), amountFingerprintValue(r.Amount), r.Reference.Type, r.Reference.ID,
	)
}

// TokenHoldCaptureRequest settles one existing hold after the upstream task
// succeeds. ActualAmount cannot exceed the amount that was held. Any unused
// bound and normal portions are released independently.
type TokenHoldCaptureRequest struct {
	UserID             int64
	HoldKey            string
	ActualAmount       decimal.Decimal
	IdempotencyKey     string
	RequestFingerprint string
}

func (r TokenHoldCaptureRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !validIdentifier(r.HoldKey, 128) || !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	if r.ActualAmount.IsNegative() || !isQuantized(r.ActualAmount) {
		return ErrInvalidAmount
	}
	return nil
}

func (r TokenHoldCaptureRequest) Fingerprint() string {
	if r.RequestFingerprint != "" {
		return r.RequestFingerprint
	}
	return fingerprint(
		"token_hold_capture", fmt.Sprint(r.UserID), r.HoldKey, amountFingerprintValue(r.ActualAmount),
	)
}

// TokenHoldReleaseRequest compensates a pre-dispatch technical failure. It
// restores the original per-asset allocation and cannot be used after capture.
type TokenHoldReleaseRequest struct {
	UserID             int64
	HoldKey            string
	IdempotencyKey     string
	RequestFingerprint string
}

// NormalAdjustmentRequest is a normal-balance-only compensating entry for
// administrator corrections and payment-refund recovery. Bound balance is
// intentionally absent: it cannot be withdrawn, refunded, or adjusted into a
// transferable asset.
type NormalAdjustmentRequest struct {
	UserID         int64
	Delta          decimal.Decimal
	Operation      LedgerOperation
	Reference      Reference
	IdempotencyKey string
}

// NormalAvailableDeductionRequest debits up to Amount from ordinary balance.
// It is used by forced payment refunds, whose historic contract permits a
// partial normal-balance clawback but must never inspect bound balance.
type NormalAvailableDeductionRequest struct {
	UserID         int64
	Amount         decimal.Decimal
	Operation      LedgerOperation
	Reference      Reference
	IdempotencyKey string
}

// SetNormalBalanceRequest preserves the legacy administrator "set" command
// without performing an absolute UPDATE. The adapter locks the row, derives a
// compensating ledger delta, and rejects any non-ordinary asset target.
type SetNormalBalanceRequest struct {
	UserID         int64
	Balance        decimal.Decimal
	Reference      Reference
	IdempotencyKey string
}

func (r SetNormalBalanceRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if r.Balance.IsNegative() || !isQuantized(r.Balance) {
		return ErrInvalidAmount
	}
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r SetNormalBalanceRequest) Fingerprint() string {
	return fingerprint("normal_set", fmt.Sprint(r.UserID), amountFingerprintValue(r.Balance), r.Reference.Type, r.Reference.ID)
}

func (r NormalAdjustmentRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !isQuantizedNonZero(r.Delta) {
		return ErrInvalidAmount
	}
	if r.Operation != OperationAdminGrant && r.Operation != OperationAdminAdjustment &&
		r.Operation != OperationRefund && r.Operation != OperationRedeemCode &&
		r.Operation != OperationPromoCode && r.Operation != OperationProviderGrant &&
		r.Operation != OperationMarketplaceDebit && r.Operation != OperationWithdrawal {
		return ErrInvalidLedgerEntry
	}
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r NormalAdjustmentRequest) Fingerprint() string {
	return fingerprint(
		"normal_adjustment", fmt.Sprint(r.UserID), amountFingerprintValue(r.Delta), string(r.Operation),
		r.Reference.Type, r.Reference.ID,
	)
}

func (r NormalAvailableDeductionRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !isQuantizedPositive(r.Amount) {
		return ErrInvalidAmount
	}
	if r.Operation != OperationRefund {
		return ErrInvalidLedgerEntry
	}
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r NormalAvailableDeductionRequest) Fingerprint() string {
	return fingerprint(
		"normal_available_deduction", fmt.Sprint(r.UserID), amountFingerprintValue(r.Amount), string(r.Operation),
		r.Reference.Type, r.Reference.ID,
	)
}

func (r TokenHoldReleaseRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !validIdentifier(r.HoldKey, 128) || !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r TokenHoldReleaseRequest) Fingerprint() string {
	if r.RequestFingerprint != "" {
		return r.RequestFingerprint
	}
	return fingerprint("token_hold_release", fmt.Sprint(r.UserID), r.HoldKey)
}

// MarketplaceDebitRequest is an ordinary-balance-only debit. It exists so
// marketplace adapters cannot accidentally call token charging and consume a
// user's bound balance.
type MarketplaceDebitRequest struct {
	UserID         int64
	Amount         decimal.Decimal
	Reference      Reference
	IdempotencyKey string
}

func (r MarketplaceDebitRequest) Validate() error {
	if r.UserID <= 0 {
		return ErrInvalidUserID
	}
	if !isQuantizedPositive(r.Amount) {
		return ErrInvalidAmount
	}
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey, 128) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r MarketplaceDebitRequest) Fingerprint() string {
	return fingerprint(
		"marketplace_debit", fmt.Sprint(r.UserID), amountFingerprintValue(r.Amount), r.Reference.Type, r.Reference.ID,
	)
}

// CreditResult and TokenChargeResult return persisted post-transaction state.
// Applied is false only for a successful idempotent replay.
type CreditResult struct {
	Applied      bool
	BalanceAfter decimal.Decimal
	Entry        LedgerEntry
}

type NormalAvailableDeductionResult struct {
	Applied      bool
	Deducted     decimal.Decimal
	BalanceAfter decimal.Decimal
	Entry        LedgerEntry
}

type TokenChargeResult struct {
	Applied    bool
	Allocation DebitAllocation
	Entries    []LedgerEntry
}

// TokenHoldState is the immutable settlement state of a pre-charge. The row
// itself is stateful solely to prevent a second capture/release; every money
// movement remains represented by append-only ledger entries.
type TokenHoldState string

const (
	TokenHoldStateHeld     TokenHoldState = "held"
	TokenHoldStateCaptured TokenHoldState = "captured"
	TokenHoldStateReleased TokenHoldState = "released"
)

type TokenHoldResult struct {
	Applied  bool
	HoldKey  string
	State    TokenHoldState
	Held     DebitAllocation
	Captured DebitAllocation
	Released DebitAllocation
	Balances Balances
}

// WalletOperation is the immutable idempotency receipt for one persisted
// wallet command. A single TokenCharge may reference two ledger entries.
type WalletOperation struct {
	ID             int64
	UserID         int64
	Operation      LedgerOperation
	IdempotencyKey string
	Fingerprint    string
	CreatedAt      time.Time
}

// LedgerPage is a read-only projection. The repository must never expose an
// update/delete operation for ledger entries.
type LedgerPage struct {
	Entries []LedgerEntry
	Total   int
}

// Repository is the wallet persistence boundary.
//
// Credit and ChargeToken must each execute in one database transaction. The
// implementation must: (1) lock or create an immutable wallet operation
// receipt, unique by (user_id, idempotency_key), and return its original result
// or ErrIdempotencyConflict for a fingerprint mismatch, (2) lock the target
// users row (SELECT ... FOR UPDATE or equivalent), (3) update users.balance
// and/or users.bound_balance, and (4) append ledger entries with balance-after
// snapshots before committing. The operation receipt must be acquired before
// the user row so concurrent equal requests do not deadlock. A receipt can own
// two ledger rows for one token charge. Ledger entries are immutable:
// corrections are compensating entries only.
type Repository interface {
	GetSnapshot(ctx context.Context, userID int64) (Snapshot, error)
	Credit(ctx context.Context, request CreditRequest) (CreditResult, error)
	ChargeToken(ctx context.Context, request TokenChargeRequest) (TokenChargeResult, error)
	ReserveTokenHold(ctx context.Context, request TokenHoldRequest) (TokenHoldResult, error)
	CaptureTokenHold(ctx context.Context, request TokenHoldCaptureRequest) (TokenHoldResult, error)
	ReleaseTokenHold(ctx context.Context, request TokenHoldReleaseRequest) (TokenHoldResult, error)
	DebitMarketplace(ctx context.Context, request MarketplaceDebitRequest) (CreditResult, error)
	ListLedger(ctx context.Context, userID int64, limit, offset int) (LedgerPage, error)
}

// SQLExecutor is deliberately compatible with both database/sql transactions
// and ent.Tx.Client(). It lets a caller keep its business transition and the
// wallet operation receipt in one physical transaction without importing Ent
// into the wallet domain.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ExecutorRepository is optional because only SQL-backed adapters can join a
// caller-owned transaction. It is intentionally separate from Repository to
// keep in-memory read-model test doubles small.
type ExecutorRepository interface {
	CreditInExecutor(ctx context.Context, executor SQLExecutor, request CreditRequest) (CreditResult, error)
	ChargeTokenInExecutor(ctx context.Context, executor SQLExecutor, request TokenChargeRequest) (TokenChargeResult, error)
	AdjustNormalInExecutor(ctx context.Context, executor SQLExecutor, request NormalAdjustmentRequest) (CreditResult, error)
	SetNormalInExecutor(ctx context.Context, executor SQLExecutor, request SetNormalBalanceRequest) (CreditResult, error)
	DebitMarketplaceInExecutor(ctx context.Context, executor SQLExecutor, request MarketplaceDebitRequest) (CreditResult, error)
}

// TransactionalRepository is implemented by persistence adapters that can
// apply a token charge inside a caller-owned SQL transaction. Gateway billing
// uses this port to keep subscription/quota updates and wallet ledger writes
// atomic without nesting database transactions.
type TransactionalRepository interface {
	ChargeTokenTx(ctx context.Context, tx *sql.Tx, request TokenChargeRequest) (TokenChargeResult, error)
}

type normalAvailableDeductionRepository interface {
	DeductNormalAvailable(ctx context.Context, request NormalAvailableDeductionRequest) (NormalAvailableDeductionResult, error)
	DeductNormalAvailableInExecutor(ctx context.Context, executor SQLExecutor, request NormalAvailableDeductionRequest) (NormalAvailableDeductionResult, error)
}

// Service is a thin policy boundary over the transactional repository port.
type Service struct {
	repository Repository
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, ErrRepositoryRequired
	}
	return &Service{repository: repository}, nil
}

func (s *Service) Credit(ctx context.Context, request CreditRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	return s.repository.Credit(ctx, request)
}

func (s *Service) CreditInExecutor(ctx context.Context, executor SQLExecutor, request CreditRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(ExecutorRepository)
	if !ok {
		return CreditResult{}, ErrTransactionalRepository
	}
	return repository.CreditInExecutor(ctx, executor, request)
}

// DebitMarketplaceInExecutor keeps a marketplace order, its inventory
// reservation, and the normal-balance-only wallet debit in one transaction.
func (s *Service) DebitMarketplaceInExecutor(ctx context.Context, executor SQLExecutor, request MarketplaceDebitRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(ExecutorRepository)
	if !ok {
		return CreditResult{}, ErrTransactionalRepository
	}
	return repository.DebitMarketplaceInExecutor(ctx, executor, request)
}

func (s *Service) AdjustNormal(ctx context.Context, request NormalAdjustmentRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	return s.adjustNormal(ctx, request)
}

func (s *Service) AdjustNormalInExecutor(ctx context.Context, executor SQLExecutor, request NormalAdjustmentRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(ExecutorRepository)
	if !ok {
		return CreditResult{}, ErrTransactionalRepository
	}
	return repository.AdjustNormalInExecutor(ctx, executor, request)
}

func (s *Service) DeductNormalAvailable(ctx context.Context, request NormalAvailableDeductionRequest) (NormalAvailableDeductionResult, error) {
	if err := request.Validate(); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	repository, ok := s.repository.(normalAvailableDeductionRepository)
	if !ok {
		return NormalAvailableDeductionResult{}, ErrTransactionalRepository
	}
	return repository.DeductNormalAvailable(ctx, request)
}

func (s *Service) DeductNormalAvailableInExecutor(ctx context.Context, executor SQLExecutor, request NormalAvailableDeductionRequest) (NormalAvailableDeductionResult, error) {
	if err := request.Validate(); err != nil {
		return NormalAvailableDeductionResult{}, err
	}
	if executor == nil {
		return NormalAvailableDeductionResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(normalAvailableDeductionRepository)
	if !ok {
		return NormalAvailableDeductionResult{}, ErrTransactionalRepository
	}
	return repository.DeductNormalAvailableInExecutor(ctx, executor, request)
}

func (s *Service) SetNormal(ctx context.Context, request SetNormalBalanceRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	repository, ok := s.repository.(*PostgresRepository)
	if !ok {
		return CreditResult{}, ErrTransactionalRepository
	}
	return repository.SetNormal(ctx, request)
}

func (s *Service) SetNormalInExecutor(ctx context.Context, executor SQLExecutor, request SetNormalBalanceRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	if executor == nil {
		return CreditResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(ExecutorRepository)
	if !ok {
		return CreditResult{}, ErrTransactionalRepository
	}
	return repository.SetNormalInExecutor(ctx, executor, request)
}

func (s *Service) adjustNormal(ctx context.Context, request NormalAdjustmentRequest) (CreditResult, error) {
	repository, ok := s.repository.(*PostgresRepository)
	if !ok {
		return CreditResult{}, ErrTransactionalRepository
	}
	return repository.AdjustNormal(ctx, request)
}

// GrantBound is the constrained convenience API for the two valid bound
// balance sources. It prevents handlers from constructing an unrestricted
// bound credit on their own.
func (s *Service) GrantBound(ctx context.Context, userID int64, amount decimal.Decimal, reason CreditReason, reference Reference, idempotencyKey string) (CreditResult, error) {
	return s.Credit(ctx, CreditRequest{
		UserID:         userID,
		Asset:          AssetBound,
		Amount:         amount,
		Reason:         reason,
		Reference:      reference,
		IdempotencyKey: idempotencyKey,
	})
}

// ChargeToken applies the wallet portion of a token charge. Subscription quota
// selection happens before this method; within the wallet the order is always
// bound balance followed by normal balance.
func (s *Service) ChargeToken(ctx context.Context, request TokenChargeRequest) (TokenChargeResult, error) {
	if err := request.Validate(); err != nil {
		return TokenChargeResult{}, err
	}
	return s.repository.ChargeToken(ctx, request)
}

// ChargeTokenInExecutor joins a caller-owned transaction while preserving the
// subscription-exhausted bound-then-normal debit policy. It exists for legacy
// business records that still use Ent transactions but must not mutate a user
// balance directly.
func (s *Service) ChargeTokenInExecutor(ctx context.Context, executor SQLExecutor, request TokenChargeRequest) (TokenChargeResult, error) {
	if err := request.Validate(); err != nil {
		return TokenChargeResult{}, err
	}
	if executor == nil {
		return TokenChargeResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(ExecutorRepository)
	if !ok {
		return TokenChargeResult{}, ErrTransactionalRepository
	}
	return repository.ChargeTokenInExecutor(ctx, executor, request)
}

func (s *Service) ReserveTokenHold(ctx context.Context, request TokenHoldRequest) (TokenHoldResult, error) {
	if err := request.Validate(); err != nil {
		return TokenHoldResult{}, err
	}
	return s.repository.ReserveTokenHold(ctx, request)
}

func (s *Service) CaptureTokenHold(ctx context.Context, request TokenHoldCaptureRequest) (TokenHoldResult, error) {
	if err := request.Validate(); err != nil {
		return TokenHoldResult{}, err
	}
	return s.repository.CaptureTokenHold(ctx, request)
}

func (s *Service) ReleaseTokenHold(ctx context.Context, request TokenHoldReleaseRequest) (TokenHoldResult, error) {
	if err := request.Validate(); err != nil {
		return TokenHoldResult{}, err
	}
	return s.repository.ReleaseTokenHold(ctx, request)
}

// ChargeTokenInTransaction applies a token charge using a transaction owned
// by the caller. It preserves the same validation and bound-then-normal
// allocation policy as ChargeToken.
func (s *Service) ChargeTokenInTransaction(ctx context.Context, tx *sql.Tx, request TokenChargeRequest) (TokenChargeResult, error) {
	if err := request.Validate(); err != nil {
		return TokenChargeResult{}, err
	}
	if tx == nil {
		return TokenChargeResult{}, ErrTransactionalRepository
	}
	repository, ok := s.repository.(TransactionalRepository)
	if !ok {
		return TokenChargeResult{}, ErrTransactionalRepository
	}
	return repository.ChargeTokenTx(ctx, tx, request)
}

func (s *Service) DebitMarketplace(ctx context.Context, request MarketplaceDebitRequest) (CreditResult, error) {
	if err := request.Validate(); err != nil {
		return CreditResult{}, err
	}
	return s.repository.DebitMarketplace(ctx, request)
}

// GetSnapshot returns the current user's dual-wallet balances. It is a
// read-side view only and must not be used by callers to authorize a later
// mutation; each debit remains responsible for locking its own balances.
func (s *Service) GetSnapshot(ctx context.Context, userID int64) (Snapshot, error) {
	if userID <= 0 {
		return Snapshot{}, ErrInvalidUserID
	}
	return s.repository.GetSnapshot(ctx, userID)
}

// ListLedger returns the user's append-only wallet history. The persistence
// adapter owns the final pagination bound because it is part of its query
// contract.
func (s *Service) ListLedger(ctx context.Context, userID int64, limit, offset int) (LedgerPage, error) {
	if userID <= 0 {
		return LedgerPage{}, ErrInvalidUserID
	}
	return s.repository.ListLedger(ctx, userID, limit, offset)
}

func validIdentifier(value string, maxLength int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxLength
}

func isQuantized(value decimal.Decimal) bool {
	return value.Equal(value.Round(MonetaryScale))
}

func isQuantizedPositive(value decimal.Decimal) bool {
	return value.IsPositive() && isQuantized(value)
}

func isQuantizedNonZero(value decimal.Decimal) bool {
	return !value.IsZero() && isQuantized(value)
}

func amountFingerprintValue(value decimal.Decimal) string {
	if !isQuantized(value) {
		return "invalid:" + value.String()
	}
	return value.StringFixed(MonetaryScale)
}

func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}
