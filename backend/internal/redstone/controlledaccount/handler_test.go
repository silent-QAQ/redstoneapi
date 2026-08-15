package controlledaccount

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreateHandlerDoesNotReturnAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, testCipher(t))
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO accounts").
		WithArgs("Private OpenAI", "openai", "api_key").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(88))
	mock.ExpectExec("INSERT INTO redstone_user_controlled_accounts").
		WithArgs(int64(88), int64(7), "openai").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO redstone_user_account_secrets").
		WithArgs(int64(88), sqlmock.AnyArg(), "user-account-test-v1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	handler := NewHandler(service)
	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		handler.Create(c)
	})
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"Private OpenAI","provider":"openai","api_key":"secret-value"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code)
	require.NotContains(t, response.Body.String(), "secret-value")
	require.NotContains(t, response.Body.String(), "credentials")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestVerifyHandlerIsReservedButInactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, err := NewService(&sql.DB{}, testCipher(t))
	require.NoError(t, err)
	handler := NewHandler(service)
	router := gin.New()
	router.POST("/:id/verify", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		handler.Verify(c)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/91/verify", nil))

	require.Equal(t, http.StatusNotImplemented, response.Code)
}

func TestOAuthBindingRejectsCrossUserWithoutConsumingOwnerSession(t *testing.T) {
	store := newOAuthBindingStore()
	store.Bind("session-1", "state-1", 17)

	err := store.Consume("session-1", "state-1", 29)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not belong")

	require.NoError(t, store.Consume("session-1", "state-1", 17))
	err = store.Consume("session-1", "state-1", 17)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already used")
}

func TestOAuthBindingRejectsStateMismatchWithoutConsumingOwnerSession(t *testing.T) {
	store := newOAuthBindingStore()
	store.Bind("session-2", "state-2", 17)

	err := store.Consume("session-2", "wrong-state", 17)
	require.Error(t, err)
	require.Contains(t, err.Error(), "state does not match")
	require.NoError(t, store.Consume("session-2", "state-2", 17))
}

func TestOAuthBindingRejectsExpiredSession(t *testing.T) {
	store := newOAuthBindingStore()
	store.entries["expired"] = oauthBindingEntry{
		UserID:    17,
		ExpiresAt: time.Now().Add(-time.Second),
	}

	err := store.Consume("expired", "", 17)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
}
