package market

import (
	"context"
	"errors"
	"net/http"
	"testing"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCreateSellerDraftRequestDefersAccountOwnershipToRepository(t *testing.T) {
	accountID := int64(42)
	err := (CreateSellerDraftRequest{
		SellerUserID: 7,
		ProductType:  "account_reference",
		Title:        "An account",
		UnitPrice:    money("10.00000000"),
		AccountID:    &accountID,
	}).Validate()
	require.NoError(t, err)
}

func TestBuildSellerDashboardUsesOnlyNormalBalanceListingRule(t *testing.T) {
	dashboard, err := buildSellerDashboard(5, money("30.00000000"), money("1.00000000"), 2)
	require.NoError(t, err)
	require.Equal(t, 3, dashboard.ListingLimit)
	require.True(t, dashboard.CanPublish)

	full, err := buildSellerDashboard(5, money("30.00000000"), money("1.00000000"), 3)
	require.NoError(t, err)
	require.False(t, full.CanPublish)

	legacyNegative, err := buildSellerDashboard(5, decimal.RequireFromString("-1"), money("1"), 0)
	require.NoError(t, err)
	require.Zero(t, legacyNegative.ListingLimit)
	require.False(t, legacyNegative.CanPublish)
}

func TestSellerServicePassesAccountReferenceToOwnershipRepository(t *testing.T) {
	repository := &sellerRepositoryStub{}
	service, err := NewService(repository)
	require.NoError(t, err)
	accountID := int64(42)

	_, err = service.CreateSellerDraft(context.Background(), CreateSellerDraftRequest{
		SellerUserID: 7,
		ProductType:  "account_reference",
		Title:        "Unsafe source",
		UnitPrice:    money("10.00000000"),
		AccountID:    &accountID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, repository.createCalls)
	require.Equal(t, &accountID, repository.createRequest.AccountID)
}

func TestCreateOfficialProductRequestDoesNotAllowAccountReference(t *testing.T) {
	valid := CreateOfficialProductRequest{
		SellerUserID: 7, ProductType: "file", Title: "Official bundle", UnitPrice: money("10.00000000"),
	}
	require.NoError(t, valid.Validate())
	valid.ProductType = "account_reference"
	require.ErrorIs(t, valid.Validate(), ErrUnsupportedSellerProductType)
}

func TestListOfficialProductsUsesOwnerScopedRepository(t *testing.T) {
	repository := &officialRepositoryStub{
		products: []Product{{ID: 9, SellerUserID: 7, SellerKind: SellerOfficial, Status: "pending_scan", RiskStatus: "pending"}},
		total:    1,
	}
	service, err := NewService(repository)
	require.NoError(t, err)

	products, total, err := service.ListOfficialProducts(context.Background(), 7, 20, 40)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, products, 1)
	require.Equal(t, int64(7), repository.sellerUserID)
	require.Equal(t, 20, repository.limit)
	require.Equal(t, 40, repository.offset)
}

func TestSellerDashboardUsesCurrentRechargeMultiplier(t *testing.T) {
	repository := &sellerRepositoryStub{}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetSellerRechargeMultiplierResolver(func(context.Context) (float64, error) {
		return 2, nil
	})

	dashboard, err := service.GetSellerDashboard(context.Background(), 7)
	require.NoError(t, err)
	require.True(t, dashboard.CNYPerBalance.Equal(decimal.RequireFromString("0.5")))
	require.Equal(t, 0, dashboard.ListingLimit)
	require.False(t, dashboard.CanPublish)
}

func TestSellerApplicationErrorMapsGovernanceFreeze(t *testing.T) {
	err := sellerApplicationError(ErrSellerFrozen)
	var appErr *infraerrors.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.EqualValues(t, http.StatusForbidden, appErr.Code)
	require.Equal(t, "MARKET_SELLER_FROZEN", appErr.Reason)
	require.ErrorIs(t, err, ErrSellerFrozen)
}

type deliveryScannerStub struct {
	result SellerScanResult
	err    error
	calls  int
}

func (s *deliveryScannerStub) Scan(_ context.Context, _ DeliveryScanInput) (SellerScanResult, error) {
	s.calls++
	return s.result, s.err
}

type deliveryUploadRepositoryStub struct {
	sellerRepositoryStub
	productType string
	inserted    []EncryptedSellerDeliveryItem
}

func (r *deliveryUploadRepositoryStub) GetSellerDeliveryProductType(context.Context, int64, int64) (string, error) {
	if r.productType == "" {
		return "", ErrDeliveryUploadProduct
	}
	return r.productType, nil
}

func (r *deliveryUploadRepositoryStub) InsertSellerDeliveryItem(_ context.Context, item EncryptedSellerDeliveryItem) (SellerProduct, error) {
	r.inserted = append(r.inserted, item)
	return SellerProduct{Product: Product{ID: item.ProductID, Status: "pending_scan"}}, nil
}

func TestUploadSellerDeliveryFailsClosedWithoutScanner(t *testing.T) {
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, testEnvelopeCipher(t))
	require.NoError(t, err)
	repository := &deliveryUploadRepositoryStub{productType: "text_key"}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(resolver)

	_, err = service.UploadSellerDelivery(context.Background(), UploadSellerDeliveryRequest{
		SellerUserID: 7, ProductID: 8, ContentType: "text/plain", Content: []byte("card-secret"),
	})
	require.Error(t, err)
	require.Empty(t, store.objects)
	require.Empty(t, repository.inserted)
}

func TestUploadSellerDeliveryQueuesCiphertextWithoutExposingPlaintextToRequestScanner(t *testing.T) {
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, testEnvelopeCipher(t))
	require.NoError(t, err)
	repository := &deliveryUploadRepositoryStub{productType: "card_key"}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(resolver)
	scanner := &deliveryScannerStub{result: SellerScanRejected}
	service.SetDeliveryScanner(scanner)

	_, err = service.UploadSellerDelivery(context.Background(), UploadSellerDeliveryRequest{
		SellerUserID: 7, ProductID: 8, ContentType: "text/plain", Content: []byte("card-secret"),
	})
	require.NoError(t, err)
	require.Zero(t, scanner.calls)
	require.Len(t, store.objects, 1)
	require.Len(t, repository.inserted, 1)
}

func TestUploadSellerDeliveryEncryptsStoresAndQueuesInventoryForScan(t *testing.T) {
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, testEnvelopeCipher(t))
	require.NoError(t, err)
	repository := &deliveryUploadRepositoryStub{productType: "text_key"}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(resolver)
	scanner := &deliveryScannerStub{result: SellerScanPassed}
	service.SetDeliveryScanner(scanner)
	content := []byte("one-time-delivery-secret")

	_, err = service.UploadSellerDelivery(context.Background(), UploadSellerDeliveryRequest{
		SellerUserID: 7, ProductID: 8, ContentType: "text/plain", Content: content,
	})
	require.NoError(t, err)
	require.Zero(t, scanner.calls)
	require.Len(t, repository.inserted, 1)
	require.Len(t, store.objects, 1)
	item := repository.inserted[0]
	require.NotEmpty(t, item.EncryptedObjectKey)
	require.NotContains(t, string(store.objects[item.EncryptedObjectKey]), "one-time-delivery-secret")
	require.NotEmpty(t, item.WrappedDEK)
	require.Empty(t, item.ContentSHA256)
	require.Equal(t, make([]byte, len(content)), content)
}

type sellerRepositoryStub struct {
	createCalls   int
	createRequest CreateSellerDraftRequest
}

type officialRepositoryStub struct {
	sellerRepositoryStub
	products     []Product
	total        int
	sellerUserID int64
	limit        int
	offset       int
}

func (r *officialRepositoryStub) ListOfficialProducts(_ context.Context, sellerUserID int64, limit, offset int) ([]Product, int, error) {
	r.sellerUserID, r.limit, r.offset = sellerUserID, limit, offset
	return r.products, r.total, nil
}

func (r *officialRepositoryStub) CreateOfficialProduct(_ context.Context, request CreateOfficialProductRequest) (Product, error) {
	return Product{ID: 1, SellerUserID: request.SellerUserID, SellerKind: SellerOfficial}, nil
}

func (r *officialRepositoryStub) GetOfficialDeliveryProductType(context.Context, int64, int64) (string, error) {
	return "text_key", nil
}

func (r *officialRepositoryStub) InsertOfficialDeliveryItem(_ context.Context, item EncryptedSellerDeliveryItem) (Product, error) {
	return Product{ID: item.ProductID, SellerUserID: item.SellerUserID, SellerKind: SellerOfficial}, nil
}

func (r *officialRepositoryStub) ApplyOfficialScanResult(context.Context, int64, SellerScanResult) (Product, error) {
	return Product{}, nil
}

func (r *sellerRepositoryStub) ListActiveProducts(context.Context, int, int) ([]Product, int, error) {
	return nil, 0, nil
}

func (r *sellerRepositoryStub) CreateOrder(context.Context, CreateOrderRequest) (CreateOrderResult, error) {
	return CreateOrderResult{}, nil
}

func (r *sellerRepositoryStub) ListOrdersByBuyer(context.Context, int64, int, int) ([]Order, int, error) {
	return nil, 0, nil
}

func (r *sellerRepositoryStub) GetSellerDashboard(_ context.Context, sellerUserID int64, cnyPerBalance decimal.Decimal) (SellerDashboard, error) {
	return buildSellerDashboard(sellerUserID, money("30"), cnyPerBalance, 0)
}

func (r *sellerRepositoryStub) ListSellerProducts(context.Context, int64, int, int) ([]SellerProduct, int, error) {
	return nil, 0, nil
}

func (r *sellerRepositoryStub) CreateSellerDraft(_ context.Context, request CreateSellerDraftRequest, _ decimal.Decimal) (SellerProduct, error) {
	r.createCalls++
	r.createRequest = request
	return SellerProduct{Product: Product{ID: 1, SellerUserID: request.SellerUserID, Title: request.Title}}, nil
}

func (r *sellerRepositoryStub) UpdateSellerDraft(context.Context, UpdateSellerDraftRequest) (SellerProduct, error) {
	return SellerProduct{}, nil
}

func (r *sellerRepositoryStub) PublishSellerProduct(context.Context, PublishSellerProductRequest, decimal.Decimal) (SellerProduct, error) {
	return SellerProduct{}, nil
}

func (r *sellerRepositoryStub) ArchiveSellerProduct(context.Context, ArchiveSellerProductRequest) (SellerProduct, error) {
	return SellerProduct{}, nil
}

func (r *sellerRepositoryStub) ApplySellerScanResult(context.Context, ApplySellerScanResultRequest, decimal.Decimal) (SellerProduct, error) {
	return SellerProduct{}, nil
}
