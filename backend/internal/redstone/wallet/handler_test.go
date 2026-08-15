package wallet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestWalletRoutesExposeOnlyCurrentUsersSnapshotAndLedger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &walletReadRepository{
		snapshot: Snapshot{UserID: 71, Balances: Balances{
			Normal: decimal.RequireFromString("12.50000000"),
			Bound:  decimal.RequireFromString("3.25000000"),
		}},
		ledger: LedgerPage{
			Total: 1,
			Entries: []LedgerEntry{{
				ID:           101,
				UserID:       71,
				Asset:        AssetBound,
				Operation:    OperationTokenCharge,
				Delta:        decimal.RequireFromString("-0.50000000"),
				BalanceAfter: decimal.RequireFromString("2.75000000"),
				Reference:    Reference{Type: "usage_log", ID: "usage_11"},
				// This field is intentionally not part of the HTTP response.
				IdempotencyKey: "internal-idempotency-key",
				CreatedAt:      time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
			}},
		},
	}
	service, err := NewService(repository)
	require.NoError(t, err)

	router := gin.New()
	v1 := router.Group("/api/v1")
	jwtAuth := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 71})
		c.Next()
	})
	RegisterRoutes(v1, NewHandler(service), jwtAuth, nil, nil)

	snapshotRequest := httptest.NewRequest(http.MethodGet, "/api/v1/wallet", nil)
	snapshotResponse := httptest.NewRecorder()
	router.ServeHTTP(snapshotResponse, snapshotRequest)
	require.Equal(t, http.StatusOK, snapshotResponse.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {"normal_balance": "12.5", "bound_balance": "3.25"}
	}`, snapshotResponse.Body.String())
	require.Equal(t, int64(71), repository.snapshotUserID)

	ledgerRequest := httptest.NewRequest(http.MethodGet, "/api/v1/wallet/ledger?page=2&page_size=25", nil)
	ledgerResponse := httptest.NewRecorder()
	router.ServeHTTP(ledgerResponse, ledgerRequest)
	require.Equal(t, http.StatusOK, ledgerResponse.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"items": [{
				"id": 101,
				"asset": "bound",
				"operation": "token_charge",
				"delta": "-0.5",
				"balance_after": "2.75",
				"reference": {"type": "usage_log", "id": "usage_11"},
				"created_at": "2026-08-13T10:00:00Z"
			}],
			"total": 1,
			"page": 2,
			"page_size": 25,
			"pages": 1
		}
	}`, ledgerResponse.Body.String())
	require.NotContains(t, ledgerResponse.Body.String(), "internal-idempotency-key")
	require.Equal(t, int64(71), repository.ledgerUserID)
	require.Equal(t, 25, repository.ledgerLimit)
	require.Equal(t, 25, repository.ledgerOffset)
}

func TestWalletRoutesRejectUnauthenticatedAndOversizedLedgerPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &walletReadRepository{}
	service, err := NewService(repository)
	require.NoError(t, err)

	unauthenticated := gin.New()
	unauthenticated.GET("/wallet", NewHandler(service).GetSnapshot)
	request := httptest.NewRequest(http.MethodGet, "/wallet", nil)
	response := httptest.NewRecorder()
	unauthenticated.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Zero(t, repository.snapshotCalls)

	overPageSize := gin.New()
	overPageSize.GET("/wallet/ledger", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 71})
		NewHandler(service).ListLedger(c)
	})
	request = httptest.NewRequest(http.MethodGet, "/wallet/ledger?page_size=201", nil)
	response = httptest.NewRecorder()
	overPageSize.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, repository.ledgerCalls)
}

type walletReadRepository struct {
	snapshot       Snapshot
	ledger         LedgerPage
	snapshotCalls  int
	snapshotUserID int64
	ledgerCalls    int
	ledgerUserID   int64
	ledgerLimit    int
	ledgerOffset   int
}

func (r *walletReadRepository) GetSnapshot(_ context.Context, userID int64) (Snapshot, error) {
	r.snapshotCalls++
	r.snapshotUserID = userID
	return r.snapshot, nil
}

func (r *walletReadRepository) ListLedger(_ context.Context, userID int64, limit, offset int) (LedgerPage, error) {
	r.ledgerCalls++
	r.ledgerUserID = userID
	r.ledgerLimit = limit
	r.ledgerOffset = offset
	return r.ledger, nil
}

func (r *walletReadRepository) Credit(context.Context, CreditRequest) (CreditResult, error) {
	return CreditResult{}, nil
}

func (r *walletReadRepository) ChargeToken(context.Context, TokenChargeRequest) (TokenChargeResult, error) {
	return TokenChargeResult{}, nil
}

func (r *walletReadRepository) ReserveTokenHold(context.Context, TokenHoldRequest) (TokenHoldResult, error) {
	return TokenHoldResult{}, nil
}

func (r *walletReadRepository) CaptureTokenHold(context.Context, TokenHoldCaptureRequest) (TokenHoldResult, error) {
	return TokenHoldResult{}, nil
}

func (r *walletReadRepository) ReleaseTokenHold(context.Context, TokenHoldReleaseRequest) (TokenHoldResult, error) {
	return TokenHoldResult{}, nil
}

func (r *walletReadRepository) DebitMarketplace(context.Context, MarketplaceDebitRequest) (CreditResult, error) {
	return CreditResult{}, nil
}
