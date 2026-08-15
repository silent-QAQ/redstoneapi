//go:build unit

package config

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadMarketplaceStorageFromEnv(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MARKETPLACE_STORAGE_ENABLED", "true")
	t.Setenv("MARKETPLACE_STORAGE_BUCKET", "private-market")
	t.Setenv("MARKETPLACE_STORAGE_ACCESS_KEY_ID", "ak")
	t.Setenv("MARKETPLACE_STORAGE_SECRET_ACCESS_KEY", "sk")
	t.Setenv("MARKETPLACE_STORAGE_ENCRYPTION_KEY_VERSION", "market-v7")
	t.Setenv("MARKETPLACE_STORAGE_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("MARKETPLACE_SCANNER_ENABLED", "true")
	t.Setenv("MARKETPLACE_SCANNER_CLAMD_ADDRESS", "127.0.0.1:3310")
	t.Setenv("USER_ACCOUNT_SECRETS_ENABLED", "true")
	t.Setenv("USER_ACCOUNT_SECRETS_ENCRYPTION_KEY_VERSION", "user-account-v3")
	t.Setenv("USER_ACCOUNT_SECRETS_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.MarketplaceStorage.Active())
	require.Equal(t, "private-market", cfg.MarketplaceStorage.Bucket)
	require.Equal(t, "market-v7", cfg.MarketplaceStorage.EncryptionKeyVersion)
	require.Equal(t, "redstone-market/", cfg.MarketplaceStorage.Prefix)
	require.True(t, cfg.MarketplaceScanner.Active())
	require.True(t, cfg.MarketplaceScanner.WorkerEnabled)
	require.Equal(t, 15, cfg.MarketplaceScanner.WorkerIntervalSeconds)
	require.Equal(t, 25, cfg.MarketplaceScanner.WorkerBatchSize)
	require.True(t, cfg.UserAccountSecrets.Active())
	require.Equal(t, "user-account-v3", cfg.UserAccountSecrets.EncryptionKeyVersion)
}
