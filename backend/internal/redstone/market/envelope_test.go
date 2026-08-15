package market

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type memoryPrivateObjectStore struct {
	objects map[string][]byte
}

func (s *memoryPrivateObjectStore) Put(_ context.Context, key string, payload []byte, _ string) error {
	if s.objects == nil {
		s.objects = make(map[string][]byte)
	}
	s.objects[key] = append([]byte(nil), payload...)
	return nil
}

func (s *memoryPrivateObjectStore) Get(_ context.Context, key string) ([]byte, error) {
	payload, ok := s.objects[key]
	if !ok {
		return nil, ErrPrivateObjectNotFound
	}
	return append([]byte(nil), payload...), nil
}

func (s *memoryPrivateObjectStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func testEnvelopeCipher(t *testing.T) *EnvelopeCipher {
	t.Helper()
	kek := make([]byte, envelopeKeyBytes)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	cipher, err := NewEnvelopeCipher("test-kek-v1", kek)
	require.NoError(t, err)
	return cipher
}

func TestEnvelopeCipherRoundTripUsesDistinctCiphertexts(t *testing.T) {
	cipher := testEnvelopeCipher(t)
	plaintext := []byte("delivery secret must not appear in storage metadata")
	aad := deliveryObjectAAD("redstone-market/item-a")

	first, err := cipher.Encrypt(plaintext, aad)
	require.NoError(t, err)
	second, err := cipher.Encrypt(plaintext, aad)
	require.NoError(t, err)
	require.NotEqual(t, first.Ciphertext, second.Ciphertext)
	require.NotEqual(t, first.WrappedDEK, second.WrappedDEK)

	decrypted, err := cipher.Decrypt(first, aad)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)
}

func TestEnvelopeCipherRejectsWrongAADAndTampering(t *testing.T) {
	cipher := testEnvelopeCipher(t)
	payload, err := cipher.Encrypt([]byte("card-key-value"), deliveryObjectAAD("object-a"))
	require.NoError(t, err)

	_, err = cipher.Decrypt(payload, deliveryObjectAAD("object-b"))
	require.ErrorIs(t, err, ErrEnvelopeCiphertext)

	payload.WrappedDEK[len(payload.WrappedDEK)-1] ^= 1
	_, err = cipher.Decrypt(payload, deliveryObjectAAD("object-a"))
	require.ErrorIs(t, err, ErrEnvelopeWrappedKey)
}

func TestEncryptedDeliveryResolverReturnsPlaintextOnlyAfterPrivateRead(t *testing.T) {
	cipher := testEnvelopeCipher(t)
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, cipher)
	require.NoError(t, err)

	objectKey := "redstone-market/delivery/test-object"
	secret := []byte("only-the-buyer-sees-this")
	payload, err := cipher.Encrypt(secret, deliveryObjectAAD(objectKey))
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), objectKey, payload.Ciphertext, "application/octet-stream"))

	text, err := resolver.ResolveText(context.Background(), DeliveryItem{
		ProductType:        "text_key",
		EncryptedObjectKey: objectKey,
		KeyVersion:         cipher.KeyVersion(),
		WrappedDEK:         payload.WrappedDEK,
	})
	require.NoError(t, err)
	require.Equal(t, string(secret), text)

	_, err = resolver.ResolveText(context.Background(), DeliveryItem{
		ProductType:        "text_key",
		EncryptedObjectKey: objectKey,
		KeyVersion:         "retired-key",
		WrappedDEK:         payload.WrappedDEK,
	})
	require.ErrorIs(t, err, ErrDeliveryKeyVersion)
	require.NotContains(t, err.Error(), string(secret))
}

func TestEncryptedDeliveryResolverDoesNotMapMissingObjectToPlaintext(t *testing.T) {
	resolver, err := NewEncryptedDeliveryResolver(&memoryPrivateObjectStore{}, testEnvelopeCipher(t))
	require.NoError(t, err)
	_, err = resolver.ResolveText(context.Background(), DeliveryItem{
		ProductType:        "card_key",
		EncryptedObjectKey: "missing",
		KeyVersion:         resolver.cipher.KeyVersion(),
		WrappedDEK:         []byte("not-a-real-wrapped-key"),
	})
	require.True(t, errors.Is(err, ErrPrivateObjectNotFound))
}

func TestEncryptedDeliveryResolverReadsExplicitPreviousKEK(t *testing.T) {
	legacy := testEnvelopeCipher(t)
	currentKey := make([]byte, envelopeKeyBytes)
	_, err := rand.Read(currentKey)
	require.NoError(t, err)
	current, err := NewEnvelopeCipher("test-kek-v2", currentKey)
	require.NoError(t, err)
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolverWithPreviousKeys(store, current, map[string]*EnvelopeCipher{
		legacy.KeyVersion(): legacy,
	})
	require.NoError(t, err)
	key := "redstone-market/delivery/legacy-object"
	payload, err := legacy.Encrypt([]byte("legacy card"), deliveryObjectAAD(key))
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), key, payload.Ciphertext, "application/octet-stream"))

	text, err := resolver.ResolveText(context.Background(), DeliveryItem{
		ProductType: "card_key", EncryptedObjectKey: key, KeyVersion: legacy.KeyVersion(), WrappedDEK: payload.WrappedDEK,
	})
	require.NoError(t, err)
	require.Equal(t, "legacy card", text)
}

func TestEncryptedDeliveryResolverRejectsFileWithUnexpectedPlaintextDigest(t *testing.T) {
	cipher := testEnvelopeCipher(t)
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, cipher)
	require.NoError(t, err)

	objectKey := "redstone-market/delivery/file-object"
	plaintext := []byte("safe-file-content")
	payload, err := cipher.Encrypt(plaintext, deliveryObjectAAD(objectKey))
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), objectKey, payload.Ciphertext, "application/octet-stream"))
	byteSize := int64(len(plaintext))
	writtenDigest := sha256.Sum256([]byte("different-content"))

	_, err = resolver.ResolveFile(context.Background(), DeliveryItem{
		ProductType: "file", EncryptedObjectKey: objectKey, KeyVersion: cipher.KeyVersion(), WrappedDEK: payload.WrappedDEK,
		ContentSHA256: fmt.Sprintf("%x", writtenDigest[:]), ByteSize: &byteSize,
	})
	require.ErrorIs(t, err, ErrDeliveryContentIntegrity)
}
