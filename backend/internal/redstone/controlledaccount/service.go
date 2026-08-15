// Package controlledaccount owns user-controlled account metadata and
// credentials. It never writes a user credential into accounts.credentials or
// makes a user-controlled account eligible for global scheduling.
package controlledaccount

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/silent-QAQ/redstoneapi/internal/config"
)

const (
	keyBytes   = 32
	secretAADV = "redstone-user-account-secret:v1:"
)

var (
	ErrCipherKey             = errors.New("user account secret key must be 32 bytes")
	ErrCipherKeyVersion      = errors.New("user account secret key version is required")
	ErrCiphertext            = errors.New("user account secret ciphertext is invalid")
	ErrWrappedDEK            = errors.New("user account secret wrapped key is invalid")
	ErrServiceUnavailable    = errors.New("user account service is unavailable")
	ErrInvalidOwner          = errors.New("user account owner is invalid")
	ErrInvalidName           = errors.New("user account name is invalid")
	ErrInvalidProvider       = errors.New("user account provider is invalid")
	ErrInvalidAuthentication = errors.New("user account authentication type is invalid")
	ErrInvalidCredentials    = errors.New("user account credentials are invalid")
	ErrUnsupportedProvider   = errors.New("user account provider is not supported for api key upload")
	ErrNotFound              = errors.New("user-controlled account was not found")
	ErrAPIKeyOnly            = errors.New("operation is only available for api key accounts")
)

// Cipher uses a per-account random DEK encrypted by a dedicated account KEK.
// It is purposefully independent from the marketplace and TOTP ciphers.
type Cipher struct {
	keyVersion string
	kek        []byte
}

type Payload struct {
	Ciphertext []byte
	WrappedDEK []byte
}

func NewCipher(keyVersion string, kek []byte) (*Cipher, error) {
	keyVersion = strings.TrimSpace(keyVersion)
	if keyVersion == "" {
		return nil, ErrCipherKeyVersion
	}
	if len(kek) != keyBytes {
		return nil, ErrCipherKey
	}
	return &Cipher{keyVersion: keyVersion, kek: append([]byte(nil), kek...)}, nil
}

func NewCipherFromBase64(keyVersion, encodedKEK string) (*Cipher, error) {
	kek, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKEK))
	if err != nil {
		return nil, ErrCipherKey
	}
	return NewCipher(keyVersion, kek)
}

func (c *Cipher) KeyVersion() string {
	if c == nil {
		return ""
	}
	return c.keyVersion
}

func (c *Cipher) Encrypt(plaintext, aad []byte) (Payload, error) {
	if c == nil || len(c.kek) != keyBytes {
		return Payload{}, ErrCipherKey
	}
	dek := make([]byte, keyBytes)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return Payload{}, fmt.Errorf("generate account secret key: %w", err)
	}
	defer zeroBytes(dek)
	ciphertext, err := seal(dek, plaintext, aad)
	if err != nil {
		return Payload{}, err
	}
	wrappedDEK, err := seal(c.kek, dek, []byte("redstone-user-account-dek:"+c.keyVersion))
	if err != nil {
		return Payload{}, err
	}
	return Payload{Ciphertext: ciphertext, WrappedDEK: wrappedDEK}, nil
}

func (c *Cipher) Decrypt(payload Payload, aad []byte) ([]byte, error) {
	if c == nil || len(c.kek) != keyBytes {
		return nil, ErrCipherKey
	}
	dek, err := open(c.kek, payload.WrappedDEK, []byte("redstone-user-account-dek:"+c.keyVersion), ErrWrappedDEK)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(dek)
	if len(dek) != keyBytes {
		return nil, ErrWrappedDEK
	}
	return open(dek, payload.Ciphertext, aad, ErrCiphertext)
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCipherKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func open(key, encoded, aad []byte, invalid error) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCipherKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(encoded) < aead.NonceSize()+aead.Overhead() {
		return nil, invalid
	}
	plaintext, err := aead.Open(nil, encoded[:aead.NonceSize()], encoded[aead.NonceSize():], aad)
	if err != nil {
		return nil, invalid
	}
	return plaintext, nil
}

type Service struct {
	db     *sql.DB
	cipher *Cipher
}

type CreateRequest struct {
	OwnerUserID    int64
	Name           string
	Provider       string
	Authentication string
	Credentials    []byte
}

type Account struct {
	ID               int64      `json:"id"`
	OwnerUserID      int64      `json:"-"`
	Name             string     `json:"name"`
	Provider         string     `json:"provider"`
	Authentication   string     `json:"authentication"`
	Lifecycle        string     `json:"lifecycle"`
	Visibility       string     `json:"visibility"`
	HealthState      string     `json:"health_state"`
	ValidationStatus string     `json:"validation_status"`
	LastValidatedAt  *time.Time `json:"last_validated_at"`
	DisabledAt       *time.Time `json:"disabled_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	// 验真相关字段
	LastVerifiedAt  *time.Time `json:"last_verified_at,omitempty"`
	VerifyScore     *int       `json:"verify_score,omitempty"`
	VerifyVerdict   *string    `json:"verify_verdict,omitempty"`
}

func NewService(db *sql.DB, cipher *Cipher) (*Service, error) {
	if db == nil || cipher == nil || cipher.KeyVersion() == "" {
		return nil, ErrServiceUnavailable
	}
	return &Service{db: db, cipher: cipher}, nil
}

// ProvideService keeps the optional feature disabled unless a dedicated account
// KEK is configured. A missing KEK must disable this API rather than falling
// back to a shared application encryption key.
func ProvideService(db *sql.DB, cfg *config.Config) (*Service, error) {
	if cfg == nil || !cfg.UserAccountSecrets.Active() {
		return nil, nil
	}
	cipher, err := NewCipherFromBase64(cfg.UserAccountSecrets.EncryptionKeyVersion, cfg.UserAccountSecrets.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("configure user account secrets: %w", err)
	}
	return NewService(db, cipher)
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Account, error) {
	defer zeroBytes(request.Credentials)
	if err := request.validate(); err != nil {
		return Account{}, err
	}
	if s == nil || s.db == nil || s.cipher == nil {
		return Account{}, ErrServiceUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var accountID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type, credentials, extra, proxy_id, concurrency, priority, status, schedulable)
		VALUES ($1, $2, $3, '{}'::jsonb, '{}'::jsonb, NULL, 1, 50, 'disabled', FALSE)
		RETURNING id
	`, request.Name, request.Provider, request.Authentication).Scan(&accountID)
	if err != nil {
		return Account{}, err
	}
	payload, err := s.cipher.Encrypt(request.Credentials, accountAAD(accountID))
	if err != nil {
		return Account{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_user_controlled_accounts
			(account_id, owner_user_id, provider, lifecycle, visibility, health_state)
		VALUES ($1, $2, $3, 'pending', 'private', 'unknown')
	`, accountID, request.OwnerUserID, request.Provider); err != nil {
		return Account{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_user_account_secrets (account_id, ciphertext, key_version, wrapped_dek)
		VALUES ($1, $2, $3, $4)
	`, accountID, payload.Ciphertext, s.cipher.KeyVersion(), payload.WrappedDEK); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return Account{
		ID: accountID, OwnerUserID: request.OwnerUserID, Name: request.Name, Provider: request.Provider,
		Authentication: request.Authentication, Lifecycle: "pending", Visibility: "private", HealthState: "unknown",
		ValidationStatus: "unknown",
	}, nil
}

func (r CreateRequest) validate() error {
	if r.OwnerUserID <= 0 {
		return ErrInvalidOwner
	}
	if r.Name != strings.TrimSpace(r.Name) || r.Name == "" || len(r.Name) > 100 {
		return ErrInvalidName
	}
	if !validIdentifier(r.Provider, 50) {
		return ErrInvalidProvider
	}
	if r.Authentication != "api_key" && r.Authentication != "oauth" {
		return ErrInvalidAuthentication
	}
	credentials := bytes.TrimSpace(r.Credentials)
	if len(credentials) == 0 || len(credentials) > 64*1024 || credentials[0] != '{' || credentials[len(credentials)-1] != '}' || !json.Valid(credentials) {
		return ErrInvalidCredentials
	}
	if r.Authentication == "api_key" {
		if !isAPIKeyUploadProvider(r.Provider) {
			return ErrUnsupportedProvider
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(credentials, &values); err != nil {
			return ErrInvalidCredentials
		}
		var apiKey string
		if raw, ok := values["api_key"]; !ok || json.Unmarshal(raw, &apiKey) != nil || strings.TrimSpace(apiKey) == "" || len(apiKey) > 20_000 {
			return ErrInvalidCredentials
		}
	}
	return nil
}

func isAPIKeyUploadProvider(provider string) bool {
	switch provider {
	case "openai", "anthropic", "gemini", "grok":
		return true
	default:
		return false
	}
}

// ListOwned returns metadata for exactly one user's non-revoked accounts.
// It intentionally does not join the secret table or project raw credentials.
func (s *Service) ListOwned(ctx context.Context, ownerUserID int64) ([]Account, error) {
	if ownerUserID <= 0 {
		return nil, ErrInvalidOwner
	}
	if s == nil || s.db == nil {
		return nil, ErrServiceUnavailable
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, r.owner_user_id, a.name, r.provider, a.type,
			r.lifecycle, r.visibility, r.health_state, a.created_at, a.updated_at,
			r.last_verified_at, r.verify_score, r.verify_verdict
		FROM redstone_user_controlled_accounts r
		JOIN accounts a ON a.id = r.account_id
		WHERE r.owner_user_id = $1 AND r.lifecycle <> 'revoked' AND a.deleted_at IS NULL
		ORDER BY a.created_at DESC, a.id DESC
	`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]Account, 0)
	for rows.Next() {
		var account Account
		var lastVerifiedAt sql.NullTime
		var verifyScore sql.NullInt64
		var verifyVerdict sql.NullString

		if err := rows.Scan(
			&account.ID, &account.OwnerUserID, &account.Name, &account.Provider, &account.Authentication,
			&account.Lifecycle, &account.Visibility, &account.HealthState, &account.CreatedAt, &account.UpdatedAt,
			&lastVerifiedAt, &verifyScore, &verifyVerdict,
		); err != nil {
			return nil, err
		}
		account.ValidationStatus = validationStatus(account.Lifecycle)
		if account.Lifecycle == "frozen" {
			account.DisabledAt = &account.UpdatedAt
		}

		// 添加验真信息
		if lastVerifiedAt.Valid {
			account.LastVerifiedAt = &lastVerifiedAt.Time
		}
		if verifyScore.Valid {
			score := int(verifyScore.Int64)
			account.VerifyScore = &score
		}
		if verifyVerdict.Valid {
			account.VerifyVerdict = &verifyVerdict.String
		}

		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func validationStatus(lifecycle string) string {
	if lifecycle == "frozen" {
		return "disabled"
	}
	return "unknown"
}

// OwnsAccount checks if a user owns a specific account
func (s *Service) OwnsAccount(ctx context.Context, ownerUserID, accountID int64) (bool, error) {
	if ownerUserID <= 0 || accountID <= 0 {
		return false, ErrInvalidOwner
	}
	if s == nil || s.db == nil {
		return false, ErrServiceUnavailable
	}

	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM redstone_user_controlled_accounts r
			WHERE r.owner_user_id = $1 AND r.account_id = $2
				AND r.lifecycle <> 'revoked'
		)
	`, ownerUserID, accountID).Scan(&exists)

	return exists, err
}

// GetCredentialsForVerification 获取账号凭证用于验真
// 仅供内部验真服务调用，不对外暴露
func (s *Service) GetCredentialsForVerification(ctx context.Context, accountID int64) (provider, authType string, credentials []byte, err error) {
	if accountID <= 0 {
		return "", "", nil, ErrInvalidOwner
	}
	if s == nil || s.db == nil {
		return "", "", nil, ErrServiceUnavailable
	}

	var ciphertext, wrappedDEK []byte
	var keyVersion string

	err = s.db.QueryRowContext(ctx, `
		SELECT r.provider, a.type, s.key_version, s.ciphertext, s.wrapped_dek
		FROM redstone_user_controlled_accounts r
		JOIN accounts a ON a.id = r.account_id
		JOIN redstone_user_controlled_account_secrets s ON s.account_id = r.account_id
		WHERE r.account_id = $1 AND r.lifecycle <> 'revoked' AND a.deleted_at IS NULL
	`, accountID).Scan(&provider, &authType, &keyVersion, &ciphertext, &wrappedDEK)

	if err != nil {
		if err == sql.ErrNoRows {
			return "", "", nil, ErrNotFound
		}
		return "", "", nil, err
	}

	// 解密凭证
	payload := Payload{
		Ciphertext: ciphertext,
		WrappedDEK: wrappedDEK,
	}
	aad := []byte(secretAADV + keyVersion + ":" + strconv.FormatInt(accountID, 10))
	plaintext, err := s.cipher.Decrypt(payload, aad)
	if err != nil {
		return "", "", nil, err
	}

	return provider, authType, plaintext, nil
}

// Rename updates only the owner's display name. The account's provider,
// credential, proxy and scheduling fields remain immutable through this API.
func (s *Service) Rename(ctx context.Context, ownerUserID, accountID int64, name string) error {
	if ownerUserID <= 0 {
		return ErrInvalidOwner
	}
	if accountID <= 0 || name != strings.TrimSpace(name) || name == "" || len(name) > 100 {
		return ErrInvalidName
	}
	if s == nil || s.db == nil {
		return ErrServiceUnavailable
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE accounts a
		SET name = $3, updated_at = NOW()
		FROM redstone_user_controlled_accounts r
		WHERE a.id = r.account_id AND r.owner_user_id = $1 AND r.account_id = $2
			AND r.lifecycle <> 'revoked' AND a.deleted_at IS NULL
	`, ownerUserID, accountID, name)
	if err != nil {
		return err
	}
	return affectedOrNotFound(result)
}

// SetAPIKeyDisabled is the reserved owner-controlled disable switch for user
// uploaded API keys. It controls only future private sharing; the underlying
// accounts row always remains globally unschedulable.
func (s *Service) SetAPIKeyDisabled(ctx context.Context, ownerUserID, accountID int64, disabled bool) error {
	if ownerUserID <= 0 {
		return ErrInvalidOwner
	}
	if accountID <= 0 {
		return ErrNotFound
	}
	if s == nil || s.db == nil {
		return ErrServiceUnavailable
	}
	lifecycle := "active"
	if disabled {
		lifecycle = "frozen"
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE redstone_user_controlled_accounts r
		SET lifecycle = $3, updated_at = NOW()
		FROM accounts a
		WHERE r.account_id = a.id AND r.owner_user_id = $1 AND r.account_id = $2
			AND r.lifecycle <> 'revoked' AND a.type = 'api_key' AND a.deleted_at IS NULL
	`, ownerUserID, accountID, lifecycle)
	if err != nil {
		return err
	}
	if err := affectedOrNotFound(result); err != nil {
		return err
	}
	return nil
}

// Revoke destroys the encrypted credential while retaining a minimal lifecycle
// marker for future audit linkage.
func (s *Service) Revoke(ctx context.Context, ownerUserID, accountID int64) error {
	if ownerUserID <= 0 {
		return ErrInvalidOwner
	}
	if accountID <= 0 {
		return ErrNotFound
	}
	if s == nil || s.db == nil {
		return ErrServiceUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_user_controlled_accounts r
		SET lifecycle = 'revoked', updated_at = NOW()
		FROM accounts a
		WHERE r.account_id = a.id AND r.owner_user_id = $1 AND r.account_id = $2
			AND r.lifecycle <> 'revoked' AND a.deleted_at IS NULL
	`, ownerUserID, accountID)
	if err != nil {
		return err
	}
	if err := affectedOrNotFound(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM redstone_user_account_secrets WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func affectedOrNotFound(result sql.Result) error {
	if result == nil {
		return ErrNotFound
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func validIdentifier(value string, maxLen int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxLen {
		return false
	}
	for _, char := range value {
		if !(unicode.IsLower(char) || unicode.IsDigit(char) || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func accountAAD(accountID int64) []byte {
	return []byte(secretAADV + strconv.FormatInt(accountID, 10))
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
