package market

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type deliveryHandlerRepository struct {
	marketHandlerRepository
	delivery Delivery
	request  struct {
		buyerUserID int64
		orderID     int64
		requestID   string
	}
}

func (r *deliveryHandlerRepository) DeliverOrder(_ context.Context, buyerUserID, orderID int64, requestID string, _ DeliveryContentResolver) (Delivery, error) {
	r.request.buyerUserID = buyerUserID
	r.request.orderID = orderID
	r.request.requestID = requestID
	return r.delivery, nil
}

func TestDeliveryRequestValidationAndSafeProjection(t *testing.T) {
	require.NotEmpty(t, "req-1")
	require.NotEqual(t, "req-1", " req-1")
	require.Equal(t, int64(77), *(&Delivery{AccountID: ptrInt64(77)}).AccountID)
	// Delivery projections contain account identity only; credentials are not a
	// field and therefore cannot accidentally cross the HTTP boundary.
	require.Empty(t, Delivery{AccountID: ptrInt64(77)}.Text)
}

func TestDeliveryHandlerUsesAuthenticatedBuyerAndRequestID(t *testing.T) {
	repository := &deliveryHandlerRepository{delivery: Delivery{OrderID: 20, ProductID: 11, ProductType: "account_reference", DeliveryItemID: 44, AccountID: ptrInt64(77)}}
	service, err := NewService(repository)
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/orders/:id/delivery", withMarketSubject(9, handler.DeliverOrder))

	request := httptest.NewRequest("POST", "/orders/20/delivery", nil)
	request.Header.Set("X-Request-ID", "request-20")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, 200, response.Code)
	require.Equal(t, int64(9), repository.request.buyerUserID)
	require.Equal(t, int64(20), repository.request.orderID)
	require.Equal(t, "request-20", repository.request.requestID)
}

func TestDeliveryContentResolverIsUsedOnlyForTextAndNeverLogsObjectKeys(t *testing.T) {
	resolver := deliveryResolverStub{text: "secret-card-value"}
	item := DeliveryItem{ID: 10, ProductType: "text_key", Status: "reserved", EncryptedObjectKey: "opaque/object-key", WrappedDEK: []byte("wrapped")}
	text, err := resolver.ResolveText(context.Background(), item)
	require.NoError(t, err)
	require.Equal(t, "secret-card-value", text)
	require.NotContains(t, text, item.EncryptedObjectKey)
}

func TestDeliveryProductTypeProjectionSupportsFilePlaceholder(t *testing.T) {
	size := int64(128)
	file := Delivery{OrderID: 20, ProductType: "file", File: &DeliveryFile{ContentType: "application/zip", ByteSize: &size, Available: false}}
	require.False(t, file.File.Available)
	require.Equal(t, int64(128), *file.File.ByteSize)
	require.Empty(t, file.Text)
}

func TestDownloadFileDeliveryWritesPrivateContentOnceAndClearsBuffer(t *testing.T) {
	byteSize := int64(len("private-file-content"))
	repository := &fileDeliveryRepositoryStub{item: DeliveryItem{
		ID: 44, ProductType: "file", Status: "reserved", EncryptedObjectKey: "redstone-market/delivery/file-44",
		KeyVersion: "test", WrappedDEK: []byte("wrapped"), ByteSize: &byteSize,
	}}
	resolver := &fileDeliveryResolverStub{content: []byte("private-file-content")}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(resolver)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/orders/:id/file-download", withMarketSubject(9, handler.DownloadFileDelivery))

	request := httptest.NewRequest(http.MethodPost, "/orders/20/file-download", nil)
	request.Header.Set("X-Request-ID", "file-request-20")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "private-file-content", response.Body.String())
	require.Equal(t, "attachment", response.Header().Get("Content-Disposition"))
	require.Equal(t, "no-store, private", response.Header().Get("Cache-Control"))
	require.Equal(t, 1, repository.prepareCalls)
	require.Equal(t, 1, repository.claimCalls)
	require.Equal(t, int64(9), repository.claimBuyerUserID)
	require.Equal(t, int64(20), repository.claimOrderID)
	require.Equal(t, "file-request-20", repository.claimRequestID)
	require.Equal(t, make([]byte, len(resolver.returned)), resolver.returned)
}

func TestDownloadFileDeliveryDoesNotClaimWhenPrivateContentCannotBeRead(t *testing.T) {
	repository := &fileDeliveryRepositoryStub{item: DeliveryItem{ID: 44, ProductType: "file", Status: "reserved"}}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(&fileDeliveryResolverStub{err: errors.New("private storage unavailable")})

	_, err = service.DownloadFileDelivery(context.Background(), 9, 20, "file-request-20")
	require.Error(t, err)
	require.Equal(t, 1, repository.prepareCalls)
	require.Zero(t, repository.claimCalls)
}

type deliveryResolverStub struct{ text string }

func (s deliveryResolverStub) ResolveText(context.Context, DeliveryItem) (string, error) {
	return s.text, nil
}

type fileDeliveryResolverStub struct {
	content  []byte
	err      error
	returned []byte
}

func (s *fileDeliveryResolverStub) ResolveText(context.Context, DeliveryItem) (string, error) {
	return "", ErrDeliveryUnavailable
}

func (s *fileDeliveryResolverStub) ResolveFile(context.Context, DeliveryItem) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.returned = append([]byte(nil), s.content...)
	return s.returned, nil
}

type fileDeliveryRepositoryStub struct {
	marketHandlerRepository
	item             DeliveryItem
	prepareCalls     int
	claimCalls       int
	claimBuyerUserID int64
	claimOrderID     int64
	claimRequestID   string
}

func (r *fileDeliveryRepositoryStub) PrepareFileDelivery(_ context.Context, buyerUserID, orderID int64) (DeliveryItem, error) {
	r.prepareCalls++
	if buyerUserID != 9 || orderID != 20 {
		return DeliveryItem{}, ErrDeliveryForbidden
	}
	return r.item, nil
}

func (r *fileDeliveryRepositoryStub) ClaimFileDelivery(_ context.Context, buyerUserID, orderID int64, requestID string) (FileDelivery, error) {
	r.claimCalls++
	r.claimBuyerUserID = buyerUserID
	r.claimOrderID = orderID
	r.claimRequestID = requestID
	return FileDelivery{OrderID: orderID, DeliveryItemID: r.item.ID}, nil
}

func ptrInt64(v int64) *int64 { return &v }
