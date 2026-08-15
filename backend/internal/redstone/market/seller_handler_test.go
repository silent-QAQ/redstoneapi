package market

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreateSellerDraftHandlerUsesAuthenticatedSellerOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &sellerRepositoryStub{}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/seller/products", withMarketSubject(81, handler.CreateSellerDraft))

	request := httptest.NewRequest(http.MethodPost, "/seller/products", strings.NewReader(`{
		"seller_user_id":999,
		"product_type":"text_key",
		"title":"  Paid key  ",
		"description":"  safe metadata  ",
		"unit_price":"12.50000000"
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Equal(t, 1, repository.createCalls)
	require.Equal(t, int64(81), repository.createRequest.SellerUserID)
	require.Equal(t, "Paid key", repository.createRequest.Title)
	require.Equal(t, "safe metadata", repository.createRequest.Description)
	require.Contains(t, recorder.Body.String(), `"seller_user_id":81`)
	require.NotContains(t, recorder.Body.String(), `"SellerUserID"`)
}

func TestCreateSellerDraftHandlerPassesAccountReferenceToOwnershipRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &sellerRepositoryStub{}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/seller/products", withMarketSubject(81, handler.CreateSellerDraft))

	request := httptest.NewRequest(http.MethodPost, "/seller/products", strings.NewReader(`{
		"product_type":"account_reference",
		"account_id":99,
		"title":"Unsafe account",
		"unit_price":"12.50000000"
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Equal(t, 1, repository.createCalls)
	require.Equal(t, int64(81), repository.createRequest.SellerUserID)
	require.NotNil(t, repository.createRequest.AccountID)
	require.Equal(t, int64(99), *repository.createRequest.AccountID)
}
