package service

import (
	"context"
	"database/sql"
)

// RedstoneWalletExecutor is the narrow common denominator implemented by both
// *sql.Tx and ent.Tx.Client(). It keeps existing sub2 business transactions
// atomic without coupling the service layer to the Redstone wallet package.
type RedstoneWalletExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type RedstoneWalletAsset string

const (
	RedstoneWalletAssetNormal RedstoneWalletAsset = "normal"
	RedstoneWalletAssetBound  RedstoneWalletAsset = "bound"
)

type RedstoneWalletCreditCommand struct {
	UserID         int64
	Asset          RedstoneWalletAsset
	Amount         float64
	Reason         string
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
}

type RedstoneWalletAdjustmentCommand struct {
	UserID         int64
	Delta          float64
	Operation      string
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
}

type RedstoneWalletSetNormalCommand struct {
	UserID         int64
	Balance        float64
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
}

// RedstoneWalletTokenChargeCommand represents a metered API debit after any
// subscription allowance has been consumed. The wallet chooses bound then
// normal balance; callers cannot select an asset or bypass that policy.
type RedstoneWalletTokenChargeCommand struct {
	UserID         int64
	Amount         float64
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
}

type RedstoneWalletTokenChargeResult struct {
	BoundDebited  float64
	NormalDebited float64
	NormalAfter   float64
	BoundAfter    float64
}

// RedstoneWalletPort is implemented at the repository/infrastructure edge.
// Only explicit ledger commands may mutate a Redstone wallet; callers do not
// receive direct balance setters.
type RedstoneWalletPort interface {
	Credit(ctx context.Context, command RedstoneWalletCreditCommand) error
	CreditInExecutor(ctx context.Context, executor RedstoneWalletExecutor, command RedstoneWalletCreditCommand) error
	AdjustNormal(ctx context.Context, command RedstoneWalletAdjustmentCommand) error
	AdjustNormalInExecutor(ctx context.Context, executor RedstoneWalletExecutor, command RedstoneWalletAdjustmentCommand) error
	DeductNormalAvailable(ctx context.Context, command RedstoneWalletAdjustmentCommand) (float64, error)
	DeductNormalAvailableInExecutor(ctx context.Context, executor RedstoneWalletExecutor, command RedstoneWalletAdjustmentCommand) (float64, error)
	SetNormal(ctx context.Context, command RedstoneWalletSetNormalCommand) error
	ChargeToken(ctx context.Context, command RedstoneWalletTokenChargeCommand) (RedstoneWalletTokenChargeResult, error)
	ChargeTokenInExecutor(ctx context.Context, executor RedstoneWalletExecutor, command RedstoneWalletTokenChargeCommand) (RedstoneWalletTokenChargeResult, error)
}
