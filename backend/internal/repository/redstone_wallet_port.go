package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/shopspring/decimal"
)

// redstoneWalletPort adapts existing service transactions to the isolated
// wallet domain. It intentionally exposes commands, never a raw balance
// update, so legacy business services cannot bypass wallet receipts.
type redstoneWalletPort struct {
	wallet *wallet.Service
}

func NewRedstoneWalletPort(db *sql.DB) (service.RedstoneWalletPort, error) {
	repository, err := wallet.NewPostgresRepository(db)
	if err != nil {
		return nil, err
	}
	service, err := wallet.NewService(repository)
	if err != nil {
		return nil, err
	}
	return &redstoneWalletPort{wallet: service}, nil
}

func (p *redstoneWalletPort) Credit(ctx context.Context, command service.RedstoneWalletCreditCommand) error {
	request, err := walletCreditRequest(command)
	if err != nil {
		return err
	}
	_, err = p.wallet.Credit(ctx, request)
	return err
}

func (p *redstoneWalletPort) CreditInExecutor(ctx context.Context, executor service.RedstoneWalletExecutor, command service.RedstoneWalletCreditCommand) error {
	request, err := walletCreditRequest(command)
	if err != nil {
		return err
	}
	_, err = p.wallet.CreditInExecutor(ctx, executor, request)
	return err
}

func (p *redstoneWalletPort) AdjustNormal(ctx context.Context, command service.RedstoneWalletAdjustmentCommand) error {
	request, err := walletAdjustmentRequest(command)
	if err != nil {
		return err
	}
	_, err = p.wallet.AdjustNormal(ctx, request)
	return err
}

func (p *redstoneWalletPort) AdjustNormalInExecutor(ctx context.Context, executor service.RedstoneWalletExecutor, command service.RedstoneWalletAdjustmentCommand) error {
	request, err := walletAdjustmentRequest(command)
	if err != nil {
		return err
	}
	_, err = p.wallet.AdjustNormalInExecutor(ctx, executor, request)
	return err
}

func (p *redstoneWalletPort) DeductNormalAvailable(ctx context.Context, command service.RedstoneWalletAdjustmentCommand) (float64, error) {
	request, err := walletAvailableDeductionRequest(command)
	if err != nil {
		return 0, err
	}
	result, err := p.wallet.DeductNormalAvailable(ctx, request)
	if err != nil {
		return 0, err
	}
	deducted, _ := result.Deducted.Float64()
	return deducted, nil
}

func (p *redstoneWalletPort) DeductNormalAvailableInExecutor(ctx context.Context, executor service.RedstoneWalletExecutor, command service.RedstoneWalletAdjustmentCommand) (float64, error) {
	request, err := walletAvailableDeductionRequest(command)
	if err != nil {
		return 0, err
	}
	result, err := p.wallet.DeductNormalAvailableInExecutor(ctx, executor, request)
	if err != nil {
		return 0, err
	}
	deducted, _ := result.Deducted.Float64()
	return deducted, nil
}

func (p *redstoneWalletPort) SetNormal(ctx context.Context, command service.RedstoneWalletSetNormalCommand) error {
	request := wallet.SetNormalBalanceRequest{
		UserID:         command.UserID,
		Balance:        decimal.NewFromFloat(command.Balance).Round(wallet.MonetaryScale),
		Reference:      wallet.Reference{Type: command.ReferenceType, ID: command.ReferenceID},
		IdempotencyKey: command.IdempotencyKey,
	}
	_, err := p.wallet.SetNormal(ctx, request)
	return err
}

func (p *redstoneWalletPort) ChargeToken(ctx context.Context, command service.RedstoneWalletTokenChargeCommand) (service.RedstoneWalletTokenChargeResult, error) {
	request, err := walletTokenChargeRequest(command)
	if err != nil {
		return service.RedstoneWalletTokenChargeResult{}, err
	}
	result, err := p.wallet.ChargeToken(ctx, request)
	if err != nil {
		return service.RedstoneWalletTokenChargeResult{}, err
	}
	return walletTokenChargeResult(result), nil
}

func (p *redstoneWalletPort) ChargeTokenInExecutor(ctx context.Context, executor service.RedstoneWalletExecutor, command service.RedstoneWalletTokenChargeCommand) (service.RedstoneWalletTokenChargeResult, error) {
	request, err := walletTokenChargeRequest(command)
	if err != nil {
		return service.RedstoneWalletTokenChargeResult{}, err
	}
	result, err := p.wallet.ChargeTokenInExecutor(ctx, executor, request)
	if err != nil {
		return service.RedstoneWalletTokenChargeResult{}, err
	}
	return walletTokenChargeResult(result), nil
}

func walletCreditRequest(command service.RedstoneWalletCreditCommand) (wallet.CreditRequest, error) {
	asset := wallet.AssetType(command.Asset)
	reason := wallet.CreditReason(command.Reason)
	if !asset.Valid() || !reason.Valid() {
		return wallet.CreditRequest{}, fmt.Errorf("unsupported Redstone wallet credit asset or reason")
	}
	return wallet.CreditRequest{
		UserID:         command.UserID,
		Asset:          asset,
		Amount:         decimal.NewFromFloat(command.Amount).Round(wallet.MonetaryScale),
		Reason:         reason,
		Reference:      wallet.Reference{Type: command.ReferenceType, ID: command.ReferenceID},
		IdempotencyKey: command.IdempotencyKey,
	}, nil
}

func walletAdjustmentRequest(command service.RedstoneWalletAdjustmentCommand) (wallet.NormalAdjustmentRequest, error) {
	operation := wallet.LedgerOperation(command.Operation)
	if operation != wallet.OperationAdminGrant && operation != wallet.OperationAdminAdjustment &&
		operation != wallet.OperationRefund && operation != wallet.OperationRedeemCode &&
		operation != wallet.OperationPromoCode && operation != wallet.OperationProviderGrant &&
		operation != wallet.OperationWithdrawal {
		return wallet.NormalAdjustmentRequest{}, fmt.Errorf("unsupported Redstone wallet adjustment operation")
	}
	return wallet.NormalAdjustmentRequest{
		UserID:         command.UserID,
		Delta:          decimal.NewFromFloat(command.Delta).Round(wallet.MonetaryScale),
		Operation:      operation,
		Reference:      wallet.Reference{Type: command.ReferenceType, ID: command.ReferenceID},
		IdempotencyKey: command.IdempotencyKey,
	}, nil
}

func walletAvailableDeductionRequest(command service.RedstoneWalletAdjustmentCommand) (wallet.NormalAvailableDeductionRequest, error) {
	if command.Operation != string(wallet.OperationRefund) || command.Delta >= 0 {
		return wallet.NormalAvailableDeductionRequest{}, fmt.Errorf("normal available deduction only supports negative refund adjustments")
	}
	return wallet.NormalAvailableDeductionRequest{
		UserID:         command.UserID,
		Amount:         decimal.NewFromFloat(-command.Delta).Round(wallet.MonetaryScale),
		Operation:      wallet.OperationRefund,
		Reference:      wallet.Reference{Type: command.ReferenceType, ID: command.ReferenceID},
		IdempotencyKey: command.IdempotencyKey,
	}, nil
}

func walletTokenChargeRequest(command service.RedstoneWalletTokenChargeCommand) (wallet.TokenChargeRequest, error) {
	return wallet.TokenChargeRequest{
		UserID:         command.UserID,
		Amount:         decimal.NewFromFloat(command.Amount).Round(wallet.MonetaryScale),
		Reference:      wallet.Reference{Type: command.ReferenceType, ID: command.ReferenceID},
		IdempotencyKey: command.IdempotencyKey,
	}, nil
}

func walletTokenChargeResult(result wallet.TokenChargeResult) service.RedstoneWalletTokenChargeResult {
	boundDebited, _ := result.Allocation.Bound.Float64()
	normalDebited, _ := result.Allocation.Normal.Float64()
	normalAfter, _ := result.Allocation.NormalBalanceAfter.Float64()
	boundAfter, _ := result.Allocation.BoundBalanceAfter.Float64()
	return service.RedstoneWalletTokenChargeResult{
		BoundDebited: boundDebited, NormalDebited: normalDebited,
		NormalAfter: normalAfter, BoundAfter: boundAfter,
	}
}
