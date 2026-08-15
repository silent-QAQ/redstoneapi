package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCreateOrderHandlerUsesSubjectAndValidatesInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &marketHandlerRepository{order: testMarketOrder()}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)

	router := gin.New()
	router.POST("/products/:id/orders", withMarketSubject(81, handler.CreateOrder))

	missingKey := httptest.NewRequest(http.MethodPost, "/products/19/orders", nil)
	missingKeyResponse := httptest.NewRecorder()
	router.ServeHTTP(missingKeyResponse, missingKey)
	require.Equal(t, http.StatusBadRequest, missingKeyResponse.Code)
	require.Zero(t, repository.createCalls)

	unsafeBody := httptest.NewRequest(http.MethodPost, "/products/19/orders", strings.NewReader(`{"unit_price":"0.01"}`))
	unsafeBody.Header.Set("Idempotency-Key", "order-19")
	unsafeBodyResponse := httptest.NewRecorder()
	router.ServeHTTP(unsafeBodyResponse, unsafeBody)
	require.Equal(t, http.StatusBadRequest, unsafeBodyResponse.Code)
	require.Zero(t, repository.createCalls)

	request := httptest.NewRequest(http.MethodPost, "/products/19/orders", strings.NewReader(`{}`))
	request.Header.Set("Idempotency-Key", "order-19")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code)
	require.Equal(t, 1, repository.createCalls)
	require.Equal(t, int64(81), repository.createRequest.BuyerUserID)
	require.Equal(t, int64(19), repository.createRequest.ProductID)
	require.Equal(t, "order-19", repository.createRequest.IdempotencyKey)
}

func TestCreateOrderHandlerSignalsIdempotentReplayAndListsCurrentUsersOrders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &marketHandlerRepository{
		order:    testMarketOrder(),
		replayed: true,
		orders:   []Order{testMarketOrder()},
		total:    1,
	}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)

	router := gin.New()
	router.POST("/products/:id/orders", withMarketSubject(81, handler.CreateOrder))
	router.GET("/orders", withMarketSubject(81, handler.ListCurrentUserOrders))

	request := httptest.NewRequest(http.MethodPost, "/products/19/orders", nil)
	request.Header.Set("Idempotency-Key", "order-19")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "true", response.Header().Get("X-Idempotency-Replayed"))

	ordersRequest := httptest.NewRequest(http.MethodGet, "/orders?page=2&page_size=5", nil)
	ordersResponse := httptest.NewRecorder()
	router.ServeHTTP(ordersResponse, ordersRequest)
	require.Equal(t, http.StatusOK, ordersResponse.Code)
	require.Equal(t, int64(81), repository.ordersBuyerUserID)
	require.Equal(t, 5, repository.ordersLimit)
	require.Equal(t, 5, repository.ordersOffset)
	require.NotContains(t, ordersResponse.Body.String(), "Idempotency-Key")
}

func TestCreateAppealHandlerUsesCurrentBuyerOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &marketSettlementHandlerRepository{appeal: Appeal{ID: 9, OrderID: 51, BuyerUserID: 81, Status: "open", Reason: "Delivery did not work"}}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/orders/:id/appeals", withMarketSubject(81, handler.CreateAppeal))

	request := httptest.NewRequest(http.MethodPost, "/orders/51/appeals", strings.NewReader(`{"reason":"Delivery did not work"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code)
	require.Equal(t, int64(81), repository.appealRequest.BuyerUserID)
	require.Equal(t, int64(51), repository.appealRequest.OrderID)
	require.Equal(t, "Delivery did not work", repository.appealRequest.Reason)
}

func TestAdminMarketHandlersUseAuthenticatedAdminSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &marketSettlementHandlerRepository{result: SettlementResult{OrderID: 51, Applied: true}}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/orders/:id/refund", withMarketSubject(7, handler.AdminRefundOrder))
	router.POST("/orders/:id/appeal-resolution", withMarketSubject(7, handler.AdminResolveAppeal))

	refundRequest := httptest.NewRequest(http.MethodPost, "/orders/51/refund", nil)
	refundResponse := httptest.NewRecorder()
	router.ServeHTTP(refundResponse, refundRequest)
	require.Equal(t, http.StatusOK, refundResponse.Code)
	require.Equal(t, int64(7), repository.refundRequest.ActorUserID)
	require.Equal(t, int64(51), repository.refundRequest.OrderID)

	resolutionRequest := httptest.NewRequest(http.MethodPost, "/orders/51/appeal-resolution", strings.NewReader(`{"action":"refund","note":"Approved"}`))
	resolutionRequest.Header.Set("Content-Type", "application/json")
	resolutionResponse := httptest.NewRecorder()
	router.ServeHTTP(resolutionResponse, resolutionRequest)
	require.Equal(t, http.StatusOK, resolutionResponse.Code)
	require.Equal(t, int64(7), repository.resolveRequest.ActorUserID)
	require.Equal(t, ResolveAppealRefund, repository.resolveRequest.Action)
}

func TestAdminOfficialProductListUsesStepUpAndAuthenticatedOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &officialRepositoryStub{
		products: []Product{{ID: 19, SellerUserID: 7, SellerKind: SellerOfficial, Title: "Official key", Status: "pending_scan", RiskStatus: "pending"}},
		total:    1,
	}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Next()
	})
	admin := router.Group("/api/v1/admin")
	stepUpCalls := 0
	RegisterAdminRoutes(admin, handler, middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.Next()
	}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/market/official/products?page=2&page_size=5", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, 1, stepUpCalls)
	require.Equal(t, int64(7), repository.sellerUserID)
	require.Equal(t, 5, repository.limit)
	require.Equal(t, 5, repository.offset)
	require.Contains(t, response.Body.String(), `"status":"pending_scan"`)
}

func TestSellerAndAdminOperationalQueuesUseProtectedSubjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &marketOperationsHandlerRepository{
		marketHandlerRepository: marketHandlerRepository{order: testMarketOrder()},
		sellerOrders:            []Order{testMarketOrder()},
		adminOrders:             []AdminOrder{{Order: testMarketOrder(), HoldCount: 1}},
		appeals:                 []AdminAppeal{{Appeal: Appeal{ID: 4, OrderID: 1, BuyerUserID: 81, Status: "open", Reason: "Missing delivery"}, ProductTitle: "Test item", OrderStatus: "appealed", SellerUserID: 22}},
	}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.GET("/seller/orders", withMarketSubject(22, handler.ListCurrentSellerOrders))
	router.GET("/admin/orders", withMarketSubject(7, handler.AdminListOrders))
	router.GET("/admin/appeals", withMarketSubject(7, handler.AdminListOpenAppeals))

	sellerResponse := httptest.NewRecorder()
	router.ServeHTTP(sellerResponse, httptest.NewRequest(http.MethodGet, "/seller/orders?page=2&page_size=5", nil))
	require.Equal(t, http.StatusOK, sellerResponse.Code)
	require.Equal(t, int64(22), repository.sellerUserID)
	require.Equal(t, 5, repository.sellerLimit)
	require.Equal(t, 5, repository.sellerOffset)

	adminResponse := httptest.NewRecorder()
	router.ServeHTTP(adminResponse, httptest.NewRequest(http.MethodGet, "/admin/orders", nil))
	require.Equal(t, http.StatusOK, adminResponse.Code)
	require.Contains(t, adminResponse.Body.String(), `"hold_count":1`)

	appealsResponse := httptest.NewRecorder()
	router.ServeHTTP(appealsResponse, httptest.NewRequest(http.MethodGet, "/admin/appeals", nil))
	require.Equal(t, http.StatusOK, appealsResponse.Code)
	require.Contains(t, appealsResponse.Body.String(), "Missing delivery")
}

func TestAdminFeePolicyHandlerUsesAuthenticatedActorAndAuditReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &marketPolicyHandlerRepository{marketHandlerRepository: marketHandlerRepository{order: testMarketOrder()}}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.PUT("/settings/service-fee", withMarketSubject(7, handler.AdminUpdateFeePolicy))

	request := httptest.NewRequest(http.MethodPut, "/settings/service-fee", strings.NewReader(`{"user_service_fee_rate":"0.07500000","reason":"运营调整"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, int64(7), repository.updateRequest.ActorUserID)
	require.True(t, repository.updateRequest.UserServiceFeeRate.Equal(decimal.RequireFromString("0.07500000")))
	require.Equal(t, "运营调整", repository.updateRequest.Reason)
}

func withMarketSubject(userID int64, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		next(c)
	}
}

type marketHandlerRepository struct {
	order             Order
	replayed          bool
	orders            []Order
	total             int
	createCalls       int
	createRequest     CreateOrderRequest
	ordersBuyerUserID int64
	ordersLimit       int
	ordersOffset      int
}

type marketSettlementHandlerRepository struct {
	marketHandlerRepository
	appeal         Appeal
	result         SettlementResult
	appealRequest  CreateAppealRequest
	refundRequest  AdminOrderRequest
	resolveRequest ResolveAppealRequest
}

type marketOperationsHandlerRepository struct {
	marketHandlerRepository
	sellerOrders []Order
	adminOrders  []AdminOrder
	appeals      []AdminAppeal
	sellerUserID int64
	sellerLimit  int
	sellerOffset int
}

type marketPolicyHandlerRepository struct {
	marketHandlerRepository
	updateRequest UpdateFeePolicyRequest
}

func (r *marketSettlementHandlerRepository) CreateAppeal(_ context.Context, request CreateAppealRequest) (Appeal, error) {
	r.appealRequest = request
	return r.appeal, nil
}

func (r *marketSettlementHandlerRepository) MarkDelivered(context.Context, MarkDeliveredRequest) (Order, error) {
	return Order{}, nil
}

func (r *marketSettlementHandlerRepository) SettleOrder(_ context.Context, request AdminOrderRequest) (SettlementResult, error) {
	return r.result, nil
}

func (r *marketSettlementHandlerRepository) RefundOrder(_ context.Context, request AdminOrderRequest) (SettlementResult, error) {
	r.refundRequest = request
	return r.result, nil
}

func (r *marketSettlementHandlerRepository) ResolveAppeal(_ context.Context, request ResolveAppealRequest) (SettlementResult, error) {
	r.resolveRequest = request
	return r.result, nil
}

func (r *marketSettlementHandlerRepository) SettleDueOrders(context.Context, time.Time, int) (SettlementBatchResult, error) {
	return SettlementBatchResult{}, nil
}

func (r *marketHandlerRepository) ListActiveProducts(context.Context, int, int) ([]Product, int, error) {
	return nil, 0, nil
}

func (r *marketHandlerRepository) CreateOrder(_ context.Context, request CreateOrderRequest) (CreateOrderResult, error) {
	r.createCalls++
	r.createRequest = request
	return CreateOrderResult{Order: r.order, Replayed: r.replayed}, nil
}

func (r *marketHandlerRepository) ListOrdersByBuyer(_ context.Context, buyerUserID int64, limit, offset int) ([]Order, int, error) {
	r.ordersBuyerUserID = buyerUserID
	r.ordersLimit = limit
	r.ordersOffset = offset
	return r.orders, r.total, nil
}

func (r *marketOperationsHandlerRepository) ListOrdersBySeller(_ context.Context, sellerUserID int64, limit, offset int) ([]Order, int, error) {
	r.sellerUserID, r.sellerLimit, r.sellerOffset = sellerUserID, limit, offset
	return r.sellerOrders, len(r.sellerOrders), nil
}

func (r *marketOperationsHandlerRepository) ListAdminOrders(context.Context, int, int) ([]AdminOrder, int, error) {
	return r.adminOrders, len(r.adminOrders), nil
}

func (r *marketOperationsHandlerRepository) ListOpenAppeals(context.Context, int, int) ([]AdminAppeal, int, error) {
	return r.appeals, len(r.appeals), nil
}

func (r *marketPolicyHandlerRepository) GetFeePolicy(context.Context) (FeePolicy, error) {
	return FeePolicy{UserServiceFeeRate: decimal.RequireFromString("0.05000000"), UpdatedAt: time.Now().UTC()}, nil
}

func (r *marketPolicyHandlerRepository) UpdateFeePolicy(_ context.Context, request UpdateFeePolicyRequest) (FeePolicy, error) {
	r.updateRequest = request
	return FeePolicy{UserServiceFeeRate: request.UserServiceFeeRate, UpdatedAt: time.Now().UTC()}, nil
}

func testMarketOrder() Order {
	return Order{
		ID: 1, OrderNo: "mkt_test", BuyerUserID: 81, SellerUserID: 22, ProductID: 19,
		ProductTitle: "Test item", DeliveryItemID: 32, Status: "paid",
		UnitPrice: decimal.RequireFromString("12.50000000"), ServiceFeeRate: decimal.RequireFromString("0.05000000"),
		ServiceFeeAmount: decimal.RequireFromString("0.62500000"), SellerNetAmount: decimal.RequireFromString("11.87500000"),
		SettlementDueAt: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		CreatedAt:       time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
	}
}
