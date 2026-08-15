package controlledaccount

import (
	"context"
	"crypto/rand"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, keyBytes)
	_, err := rand.Read(key)
	require.NoError(t, err)
	cipher, err := NewCipher("user-account-test-v1", key)
	require.NoError(t, err)
	return cipher
}

func TestCipherRoundTripBindsAccountID(t *testing.T) {
	cipher := testCipher(t)
	payload, err := cipher.Encrypt([]byte(`{"api_key":"secret"}`), accountAAD(41))
	require.NoError(t, err)
	plaintext, err := cipher.Decrypt(payload, accountAAD(41))
	require.NoError(t, err)
	require.JSONEq(t, `{"api_key":"secret"}`, string(plaintext))
	zeroBytes(plaintext)

	_, err = cipher.Decrypt(payload, accountAAD(42))
	require.ErrorIs(t, err, ErrCiphertext)
}

func TestCreateStoresOnlyCiphertextAndClearsCallerBuffer(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, testCipher(t))
	require.NoError(t, err)
	credentials := []byte(`{"api_key":"secret-value"}`)

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

	account, err := service.Create(context.Background(), CreateRequest{
		OwnerUserID: 7, Name: "Private OpenAI", Provider: "openai", Authentication: "api_key", Credentials: credentials,
	})
	require.NoError(t, err)
	require.Equal(t, int64(88), account.ID)
	require.Equal(t, make([]byte, len(credentials)), credentials)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateRejectsInvalidCredentialsBeforeDatabaseAccess(t *testing.T) {
	service, err := NewService(&sql.DB{}, testCipher(t))
	require.NoError(t, err)
	_, err = service.Create(context.Background(), CreateRequest{
		OwnerUserID: 7, Name: "Private OpenAI", Provider: "openai", Authentication: "api_key", Credentials: []byte("not-json"),
	})
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestCreateRejectsUnsupportedAPIKeyProviderBeforeDatabaseAccess(t *testing.T) {
	service, err := NewService(&sql.DB{}, testCipher(t))
	require.NoError(t, err)
	_, err = service.Create(context.Background(), CreateRequest{
		OwnerUserID: 7, Name: "Private endpoint", Provider: "custom", Authentication: "api_key", Credentials: []byte(`{"api_key":"secret"}`),
	})
	require.ErrorIs(t, err, ErrUnsupportedProvider)
}

func TestListOwnedProjectsMetadataWithoutSecrets(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, testCipher(t))
	require.NoError(t, err)

	rows := sqlmock.NewRows([]string{
		"id", "owner_user_id", "name", "provider", "type", "lifecycle", "visibility", "health_state", "created_at", "updated_at",
	}).AddRow(91, 7, "Private OpenAI", "openai", "api_key", "active", "private", "healthy", time.Now(), time.Now())
	mock.ExpectQuery("SELECT a.id, r.owner_user_id").WithArgs(int64(7)).WillReturnRows(rows)

	accounts, err := service.ListOwned(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, "unknown", accounts[0].ValidationStatus)
	require.Nil(t, accounts[0].LastValidatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetAPIKeyDisabledRequiresOwnedAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, testCipher(t))
	require.NoError(t, err)

	mock.ExpectExec("UPDATE redstone_user_controlled_accounts").
		WithArgs(int64(7), int64(91), "frozen").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, service.SetAPIKeyDisabled(context.Background(), 7, 91, true))
	require.NoError(t, mock.ExpectationsWereMet())
}
