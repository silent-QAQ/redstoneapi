// Package market implements Redstone's user-to-user marketplace policy.
// It deliberately references existing sub2 accounts by ID and never handles
// account credentials, OAuth tokens, proxies, or account uploads.
package market

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
)

const (
	DefaultUserServiceFeeRate = "0.05000000"
	OfficialServiceFeeRate    = "0.00000000"
)

var (
	ErrInvalidRechargeRate    = errors.New("market recharge rate must be positive")
	ErrInvalidBalance         = errors.New("market balance must be nonnegative")
	ErrInvalidSellerKind      = errors.New("market seller kind is invalid")
	ErrMarketplaceUnavailable = errors.New("market wallet is unavailable")
)

type SellerKind string

const (
	SellerUser     SellerKind = "user"
	SellerOfficial SellerKind = "official"
)

func (s SellerKind) Valid() bool {
	return s == SellerUser || s == SellerOfficial
}

// SellerListingLimit calculates the maximum active listings from the user's
// current normal balance converted at the configured CNY-per-balance rate.
// Existing listings remain untouched when the balance later falls.
func SellerListingLimit(normalBalance, cnyPerBalance decimal.Decimal) (int, error) {
	if normalBalance.IsNegative() {
		return 0, ErrInvalidBalance
	}
	if !cnyPerBalance.IsPositive() {
		return 0, ErrInvalidRechargeRate
	}
	cnyValue := normalBalance.Mul(cnyPerBalance)
	minimum := decimal.NewFromInt(30)
	if cnyValue.LessThan(minimum) {
		return 0, nil
	}
	extra := cnyValue.Sub(minimum).Div(decimal.NewFromInt(20)).Floor()
	extraInt := int(extra.IntPart())
	if extraInt > math.MaxInt-3 {
		return math.MaxInt, nil
	}
	return 3 + extraInt, nil
}

type OrderAmounts struct {
	Price      decimal.Decimal
	FeeRate    decimal.Decimal
	FeeAmount  decimal.Decimal
	SellerNet  decimal.Decimal
	SellerKind SellerKind
}

func CalculateOrderAmounts(price decimal.Decimal, sellerKind SellerKind, configuredUserFeeRate decimal.Decimal) (OrderAmounts, error) {
	if !price.IsPositive() || !price.Equal(price.Round(8)) {
		return OrderAmounts{}, errors.New("market price must be positive and quantized to 8 decimal places")
	}
	if !sellerKind.Valid() {
		return OrderAmounts{}, ErrInvalidSellerKind
	}
	feeRate := decimal.Zero
	if sellerKind == SellerUser {
		feeRate = configuredUserFeeRate
	}
	if feeRate.IsNegative() || feeRate.GreaterThan(decimal.NewFromInt(1)) || !feeRate.Equal(feeRate.Round(8)) {
		return OrderAmounts{}, errors.New("market fee rate must be between zero and one")
	}
	fee := price.Mul(feeRate).Round(8)
	return OrderAmounts{
		Price: price, FeeRate: feeRate, FeeAmount: fee, SellerNet: price.Sub(fee), SellerKind: sellerKind,
	}, nil
}

type Product struct {
	ID                int64           `json:"id"`
	SellerUserID      int64           `json:"seller_user_id"`
	SellerKind        SellerKind      `json:"seller_kind"`
	ProductType       string          `json:"product_type"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	InventoryTotal    int             `json:"inventory_total"`
	InventoryReserved int             `json:"inventory_reserved"`
	Status            string          `json:"status"`
	RiskStatus        string          `json:"risk_status"`
	AccountID         *int64          `json:"account_id,omitempty"`
}

// Repository must reserve one delivery item and create an order atomically.
// Its implementation must debit only normal balance via the wallet ledger;
// bound balance is never an eligible funding source for marketplace orders.
type Repository interface {
	ListActiveProducts(ctx context.Context, limit, offset int) ([]Product, int, error)
	CreateOrder(ctx context.Context, request CreateOrderRequest) (CreateOrderResult, error)
	ListOrdersByBuyer(ctx context.Context, buyerUserID int64, limit, offset int) ([]Order, int, error)
}

type sqlRepository struct {
	db     *sql.DB
	wallet *wallet.Service
}

// NewPostgresRepository constructs the marketplace read/write adapter. Order
// payment writes use the repository's own shared SQL transaction so inventory,
// order, and wallet ledger state commit or roll back together.
func NewPostgresRepository(db *sql.DB) (Repository, error) {
	if db == nil {
		return nil, errors.New("market postgres repository db is nil")
	}
	walletRepository, err := wallet.NewPostgresRepository(db)
	if err != nil {
		return nil, err
	}
	walletService, err := wallet.NewService(walletRepository)
	if err != nil {
		return nil, err
	}
	return &sqlRepository{db: db, wallet: walletService}, nil
}

func (r *sqlRepository) ListActiveProducts(ctx context.Context, limit, offset int) ([]Product, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM redstone_market_products
		WHERE status = 'active' AND risk_status = 'passed'
			AND inventory_total > inventory_reserved
	`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, seller_user_id, seller_kind, product_type, title, description,
			unit_price, inventory_total, inventory_reserved, status, risk_status, account_id
		FROM redstone_market_products
		WHERE status = 'active' AND risk_status = 'passed'
			AND inventory_total > inventory_reserved
		ORDER BY published_at DESC NULLS LAST, id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	products := make([]Product, 0)
	for rows.Next() {
		var product Product
		if err := rows.Scan(
			&product.ID, &product.SellerUserID, &product.SellerKind, &product.ProductType, &product.Title, &product.Description,
			&product.UnitPrice, &product.InventoryTotal, &product.InventoryReserved, &product.Status, &product.RiskStatus, &product.AccountID,
		); err != nil {
			return nil, 0, err
		}
		products = append(products, product)
	}
	return products, total, rows.Err()
}

type Service struct {
	repository                       Repository
	deliveryResolver                 DeliveryContentResolver
	deliveryScanner                  DeliveryScanner
	contentScanner                   ContentModerationScanner
	sellerRechargeMultiplierResolver SellerRechargeMultiplierResolver
	scanRuntime                      *deliveryScanRuntimeState
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("market repository is required")
	}
	return &Service{
		repository:     repository,
		contentScanner: NewDeterministicContentModerationScanner(),
		scanRuntime:    newDeliveryScanRuntimeState(),
	}, nil
}

// ProvideService wires seller eligibility to the existing payment recharge
// multiplier while retaining NewService for focused domain tests.
func ProvideService(repository Repository, provider SellerRechargeMultiplierProvider, resolver DeliveryContentResolver, scanner DeliveryScanner) (*Service, error) {
	service, err := NewService(repository)
	if err != nil {
		return nil, err
	}
	if provider != nil {
		service.SetSellerRechargeMultiplierResolver(provider.GetBalanceRechargeMultiplier)
	}
	service.SetDeliveryContentResolver(resolver)
	service.SetDeliveryScanner(scanner)
	return service, nil
}

// SetDeliveryContentResolver supplies the encrypted object-store adapter for
// text/card deliveries. Account references never use this resolver and return
// only the existing accounts.id projection.
func (s *Service) SetDeliveryContentResolver(resolver DeliveryContentResolver) {
	if s != nil {
		s.deliveryResolver = resolver
	}
}

// SetSellerRechargeMultiplierResolver connects seller eligibility to the
// existing payment configuration without coupling marketplace checkout to it.
func (s *Service) SetSellerRechargeMultiplierResolver(resolver SellerRechargeMultiplierResolver) {
	if s != nil {
		s.sellerRechargeMultiplierResolver = resolver
	}
}

func (s *Service) ListActiveProducts(ctx context.Context, limit, offset int) ([]Product, int, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, fmt.Errorf("invalid marketplace pagination")
	}
	return s.repository.ListActiveProducts(ctx, limit, offset)
}

// CreateOrder purchases one delivery item from an active marketplace product.
// The repository owns the single SQL transaction containing the inventory
// reservation, ordinary-balance debit, immutable wallet receipt and order.
func (s *Service) CreateOrder(ctx context.Context, request CreateOrderRequest) (CreateOrderResult, error) {
	if err := request.Validate(); err != nil {
		return CreateOrderResult{}, marketApplicationError(err)
	}
	result, err := s.repository.CreateOrder(ctx, request)
	if err != nil {
		return CreateOrderResult{}, marketApplicationError(err)
	}
	return result, nil
}

// ListOrdersByBuyer returns only the authenticated buyer's own orders. The
// caller supplies its subject user ID rather than accepting a client-selected
// user ID, which prevents cross-user order disclosure.
func (s *Service) ListOrdersByBuyer(ctx context.Context, buyerUserID int64, limit, offset int) ([]Order, int, error) {
	if buyerUserID <= 0 {
		return nil, 0, marketApplicationError(ErrInvalidBuyer)
	}
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, marketApplicationError(ErrInvalidPagination)
	}
	orders, total, err := s.repository.ListOrdersByBuyer(ctx, buyerUserID, limit, offset)
	if err != nil {
		return nil, 0, marketApplicationError(err)
	}
	return orders, total, nil
}
