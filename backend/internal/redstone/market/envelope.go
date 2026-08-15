package market

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	envelopeKeyBytes = 32
	envelopeNonceLen = 12
)

var (
	ErrEnvelopeKeyInvalid    = errors.New("market envelope key must be 32 bytes")
	ErrEnvelopeKeyVersion    = errors.New("market envelope key version is required")
	ErrEnvelopeCiphertext    = errors.New("market envelope ciphertext is invalid")
	ErrEnvelopeWrappedKey    = errors.New("market envelope wrapped key is invalid")
	ErrPrivateObjectNotFound = errors.New("market private object was not found")
	ErrPrivateObjectStoreNil = errors.New("market private object store is required")
	ErrEncryptedResolverNil  = errors.New("market encrypted delivery resolver is incomplete")
	ErrDeliveryKeyVersion    = errors.New("market delivery key version is unavailable")
)

// PrivateObjectStore holds ciphertext only. Implementations must never return
// a public or presigned URL: buyers receive delivery data through an
// authenticated marketplace request after the one-time audit claim succeeds.
type PrivateObjectStore interface {
	Put(context.Context, string, []byte, string) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

// PrivateObjectStoreHealthChecker is optional so focused in-memory stores do
// not need infrastructure-specific probes. Production adapters implement it
// with a short-lived random ciphertext round trip.
type PrivateObjectStoreHealthChecker interface {
	HealthCheck(context.Context) error
}

// EnvelopeCipher encrypts every delivery payload with a fresh data-encryption
// key (DEK), then encrypts that DEK with a marketplace-specific KEK. The KEK
// is deliberately independent from the TOTP and payment keys.
type EnvelopeCipher struct {
	keyVersion string
	kek        []byte
}

type EnvelopePayload struct {
	Ciphertext []byte
	WrappedDEK []byte
}

func NewEnvelopeCipher(keyVersion string, kek []byte) (*EnvelopeCipher, error) {
	if strings.TrimSpace(keyVersion) == "" {
		return nil, ErrEnvelopeKeyVersion
	}
	if len(kek) != envelopeKeyBytes {
		return nil, ErrEnvelopeKeyInvalid
	}
	return &EnvelopeCipher{keyVersion: strings.TrimSpace(keyVersion), kek: append([]byte(nil), kek...)}, nil
}

// NewEnvelopeCipherFromBase64 is intended for a KMS-exported or deployment
// secret value. The caller must keep the source value out of logs and API
// responses; this constructor never includes it in an error message.
func NewEnvelopeCipherFromBase64(keyVersion, encodedKEK string) (*EnvelopeCipher, error) {
	kek, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKEK))
	if err != nil {
		return nil, ErrEnvelopeKeyInvalid
	}
	return NewEnvelopeCipher(keyVersion, kek)
}

func (c *EnvelopeCipher) KeyVersion() string {
	if c == nil {
		return ""
	}
	return c.keyVersion
}

func (c *EnvelopeCipher) Encrypt(plaintext, aad []byte) (EnvelopePayload, error) {
	if c == nil || len(c.kek) != envelopeKeyBytes {
		return EnvelopePayload{}, ErrEnvelopeKeyInvalid
	}
	dek := make([]byte, envelopeKeyBytes)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return EnvelopePayload{}, fmt.Errorf("generate market delivery key: %w", err)
	}
	defer zeroBytes(dek)

	ciphertext, err := seal(dek, plaintext, aad)
	if err != nil {
		return EnvelopePayload{}, err
	}
	wrappedDEK, err := seal(c.kek, dek, dekAAD(c.keyVersion))
	if err != nil {
		return EnvelopePayload{}, err
	}
	return EnvelopePayload{Ciphertext: ciphertext, WrappedDEK: wrappedDEK}, nil
}

func (c *EnvelopeCipher) Decrypt(payload EnvelopePayload, aad []byte) ([]byte, error) {
	if c == nil || len(c.kek) != envelopeKeyBytes {
		return nil, ErrEnvelopeKeyInvalid
	}
	dek, err := open(c.kek, payload.WrappedDEK, dekAAD(c.keyVersion), ErrEnvelopeWrappedKey)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(dek)
	if len(dek) != envelopeKeyBytes {
		return nil, ErrEnvelopeWrappedKey
	}
	return open(dek, payload.Ciphertext, aad, ErrEnvelopeCiphertext)
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrEnvelopeKeyInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create market envelope cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate market envelope nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func open(key, encoded, aad []byte, invalid error) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrEnvelopeKeyInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, invalid
	}
	if len(encoded) < aead.NonceSize()+aead.Overhead() {
		return nil, invalid
	}
	nonce, ciphertext := encoded[:aead.NonceSize()], encoded[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, invalid
	}
	return plaintext, nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func dekAAD(keyVersion string) []byte {
	return []byte("redstone-market-dek:" + keyVersion)
}

func deliveryObjectAAD(objectKey string) []byte {
	return []byte("redstone-market-delivery-object:" + objectKey)
}

// EncryptedDeliveryResolver is the production-facing read boundary shared by
// text and card-key delivery. It performs no logging, caching, or persistence
// of the decrypted content.
type EncryptedDeliveryResolver struct {
	store        PrivateObjectStore
	cipher       *EnvelopeCipher
	previousKeys map[string]*EnvelopeCipher
}

func (r *EncryptedDeliveryResolver) HealthCheck(ctx context.Context) error {
	if r == nil || r.store == nil {
		return ErrPrivateObjectStoreNil
	}
	checker, ok := r.store.(PrivateObjectStoreHealthChecker)
	if !ok {
		return ErrRuntimeHealthCheckUnavailable
	}
	return checker.HealthCheck(ctx)
}

func NewEncryptedDeliveryResolver(store PrivateObjectStore, cipher *EnvelopeCipher) (*EncryptedDeliveryResolver, error) {
	return NewEncryptedDeliveryResolverWithPreviousKeys(store, cipher, nil)
}

// NewEncryptedDeliveryResolverWithPreviousKeys accepts only explicit legacy
// KEKs for read compatibility. Writes always use cipher, so rotation cannot
// make pending historical deliveries unreadable.
func NewEncryptedDeliveryResolverWithPreviousKeys(store PrivateObjectStore, cipher *EnvelopeCipher, previous map[string]*EnvelopeCipher) (*EncryptedDeliveryResolver, error) {
	if store == nil {
		return nil, ErrPrivateObjectStoreNil
	}
	if cipher == nil || cipher.KeyVersion() == "" {
		return nil, ErrEncryptedResolverNil
	}
	previousKeys := make(map[string]*EnvelopeCipher, len(previous))
	for version, legacy := range previous {
		version = strings.TrimSpace(version)
		if version == "" || version == cipher.KeyVersion() || legacy == nil || legacy.KeyVersion() != version {
			return nil, ErrEnvelopeKeyVersion
		}
		previousKeys[version] = legacy
	}
	return &EncryptedDeliveryResolver{store: store, cipher: cipher, previousKeys: previousKeys}, nil
}

func (r *EncryptedDeliveryResolver) ResolveText(ctx context.Context, item DeliveryItem) (string, error) {
	if item.ProductType != "text_key" && item.ProductType != "card_key" {
		return "", ErrDeliveryUnavailable
	}
	plaintext, err := r.resolve(ctx, item)
	if err != nil {
		return "", err
	}
	defer zeroBytes(plaintext)
	return string(plaintext), nil
}

// ResolveFile returns a short-lived plaintext buffer only after the caller has
// authorized the file delivery. The handler clears the buffer immediately
// after writing it to the buyer response and never exposes an object URL.
func (r *EncryptedDeliveryResolver) ResolveFile(ctx context.Context, item DeliveryItem) ([]byte, error) {
	if item.ProductType != "file" {
		return nil, ErrDeliveryUnavailable
	}
	return r.resolve(ctx, item)
}

func (r *EncryptedDeliveryResolver) resolve(ctx context.Context, item DeliveryItem) ([]byte, error) {
	if r == nil || r.store == nil || r.cipher == nil {
		return nil, ErrEncryptedResolverNil
	}
	if strings.TrimSpace(item.EncryptedObjectKey) == "" || len(item.WrappedDEK) == 0 {
		return nil, ErrDeliveryUnavailable
	}
	cipher := r.cipher
	if item.KeyVersion != cipher.KeyVersion() {
		cipher = r.previousKeys[item.KeyVersion]
	}
	if cipher == nil {
		return nil, ErrDeliveryKeyVersion
	}
	ciphertext, err := r.store.Get(ctx, item.EncryptedObjectKey)
	if err != nil {
		return nil, fmt.Errorf("load encrypted market delivery: %w", err)
	}
	plaintext, err := cipher.Decrypt(EnvelopePayload{Ciphertext: ciphertext, WrappedDEK: item.WrappedDEK}, deliveryObjectAAD(item.EncryptedObjectKey))
	if err != nil {
		return nil, err
	}
	if err := validateDeliveryPlaintext(item, plaintext); err != nil {
		zeroBytes(plaintext)
		return nil, err
	}
	return plaintext, nil
}

func validateDeliveryPlaintext(item DeliveryItem, plaintext []byte) error {
	if item.ByteSize != nil && int64(len(plaintext)) != *item.ByteSize {
		return ErrDeliveryContentIntegrity
	}
	if expected := strings.TrimSpace(item.ContentSHA256); expected != "" {
		actual := sha256.Sum256(plaintext)
		if !strings.EqualFold(expected, fmt.Sprintf("%x", actual[:])) {
			return ErrDeliveryContentIntegrity
		}
	}
	return nil
}

// Store encrypts the supplied plaintext before the object-store call. The
// caller owns the plaintext buffer and must clear it after this method returns.
func (r *EncryptedDeliveryResolver) Store(ctx context.Context, objectKey string, plaintext []byte) (EnvelopePayload, error) {
	if r == nil || r.store == nil || r.cipher == nil {
		return EnvelopePayload{}, ErrEncryptedResolverNil
	}
	payload, err := r.cipher.Encrypt(plaintext, deliveryObjectAAD(objectKey))
	if err != nil {
		return EnvelopePayload{}, err
	}
	if err := r.store.Put(ctx, objectKey, payload.Ciphertext, "application/octet-stream"); err != nil {
		return EnvelopePayload{}, fmt.Errorf("store encrypted market delivery: %w", err)
	}
	return payload, nil
}
