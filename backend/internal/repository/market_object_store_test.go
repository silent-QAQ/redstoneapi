package repository

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/market"
	"github.com/stretchr/testify/require"
)

func TestNewMarketDeliveryResolverDisabledWithoutCredentials(t *testing.T) {
	resolver, err := NewMarketDeliveryResolver(&config.Config{})
	require.NoError(t, err)
	require.Nil(t, resolver)
}

func TestNewMarketDeliveryResolverPreviewUsesEncryptedLocalStore(t *testing.T) {
	t.Setenv(market.PreviewInfrastructureEnv, "1")
	root := t.TempDir()
	t.Setenv("REDSTONE_MARKETPLACE_PREVIEW_DIR", root)
	content := []byte("preview-secret")
	key := make([]byte, 32)
	ciphertextKey := base64.StdEncoding.EncodeToString(key)

	resolverValue, err := NewMarketDeliveryResolver(&config.Config{MarketplaceStorage: config.MarketplaceStorageConfig{
		Prefix: "redstone-market/", EncryptionKeyVersion: "preview-v1", EncryptionKey: ciphertextKey,
	}})
	require.NoError(t, err)
	resolver, ok := resolverValue.(*market.EncryptedDeliveryResolver)
	require.True(t, ok)

	const objectKey = "redstone-market/preview-object"
	payload, err := resolver.Store(context.Background(), objectKey, content)
	require.NoError(t, err)
	size := int64(len(content))
	digest := sha256.Sum256(content)
	got, err := resolver.ResolveText(context.Background(), market.DeliveryItem{
		ProductType: "text_key", EncryptedObjectKey: objectKey, KeyVersion: "preview-v1",
		WrappedDEK: payload.WrappedDEK, ContentSHA256: hex.EncodeToString(digest[:]), ByteSize: &size,
	})
	require.NoError(t, err)
	require.Equal(t, string(content), got)

	stored, err := os.ReadFile(filepath.Join(root, "redstone-market", "preview-object"))
	require.NoError(t, err)
	require.NotContains(t, string(stored), string(content))
}

func TestMarketplaceStorageConfigRequiresDedicatedKEK(t *testing.T) {
	resolver, err := NewMarketDeliveryResolver(&config.Config{MarketplaceStorage: config.MarketplaceStorageConfig{
		Enabled:              true,
		Bucket:               "market",
		AccessKeyID:          "access",
		SecretAccessKey:      "secret",
		EncryptionKeyVersion: "v1",
		EncryptionKey:        base64.StdEncoding.EncodeToString(make([]byte, 31)),
	}})
	require.Error(t, err)
	require.Nil(t, resolver)
}

func TestNormalizeMarketObjectPrefix(t *testing.T) {
	require.Equal(t, "redstone-market/", normalizeMarketObjectPrefix(""))
	require.Equal(t, "market/private/", normalizeMarketObjectPrefix("/market/private/"))
}

func TestNormalizedMarketCiphertextLimitReservesEnvelopeOverhead(t *testing.T) {
	require.Equal(t, defaultMarketObjectBytes+marketEnvelopeOverheadBytes, normalizedMarketCiphertextLimit(0))
	require.Equal(t, int64(1024)+marketEnvelopeOverheadBytes, normalizedMarketCiphertextLimit(1024))
	require.Equal(t, maxMarketObjectCiphertextBytes, normalizedMarketCiphertextLimit(maxMarketObjectCiphertextBytes))
}
