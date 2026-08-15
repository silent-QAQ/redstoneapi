//go:build unit

package service_test

import (
	"context"
	"fmt"

	"github.com/silent-QAQ/redstoneapi/internal/service"
)

// sqliteWalletPort is a deliberately small test fixture for legacy service
// tests that build an Ent SQLite client directly. Production wiring always
// injects the Postgres-backed immutable wallet port; this fixture keeps those
// focused tests concerned with their own business transition rather than a
// PostgreSQL-only ledger implementation.
type sqliteWalletPort struct{}

func (sqliteWalletPort) Credit(context.Context, service.RedstoneWalletCreditCommand) error {
	return nil
}

func (sqliteWalletPort) CreditInExecutor(ctx context.Context, executor service.RedstoneWalletExecutor, command service.RedstoneWalletCreditCommand) error {
	if command.Asset != service.RedstoneWalletAssetNormal || command.Amount == 0 {
		return nil
	}
	_, err := executor.ExecContext(ctx, "UPDATE users SET balance = balance + ? WHERE id = ?", command.Amount, command.UserID)
	return err
}

func (sqliteWalletPort) AdjustNormal(context.Context, service.RedstoneWalletAdjustmentCommand) error {
	return nil
}

func (sqliteWalletPort) AdjustNormalInExecutor(ctx context.Context, executor service.RedstoneWalletExecutor, command service.RedstoneWalletAdjustmentCommand) error {
	result, err := executor.ExecContext(ctx, "UPDATE users SET balance = balance + ? WHERE id = ? AND balance + ? >= 0", command.Delta, command.UserID, command.Delta)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("test wallet insufficient balance or user missing")
	}
	return nil
}

func (sqliteWalletPort) DeductNormalAvailable(context.Context, service.RedstoneWalletAdjustmentCommand) (float64, error) {
	return 0, nil
}

func (sqliteWalletPort) DeductNormalAvailableInExecutor(context.Context, service.RedstoneWalletExecutor, service.RedstoneWalletAdjustmentCommand) (float64, error) {
	return 0, nil
}

func (sqliteWalletPort) SetNormal(context.Context, service.RedstoneWalletSetNormalCommand) error {
	return nil
}

func (sqliteWalletPort) ChargeToken(context.Context, service.RedstoneWalletTokenChargeCommand) (service.RedstoneWalletTokenChargeResult, error) {
	return service.RedstoneWalletTokenChargeResult{}, nil
}

func (sqliteWalletPort) ChargeTokenInExecutor(context.Context, service.RedstoneWalletExecutor, service.RedstoneWalletTokenChargeCommand) (service.RedstoneWalletTokenChargeResult, error) {
	return service.RedstoneWalletTokenChargeResult{}, nil
}
