package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAccountSharingLeaseCleanupDefaults(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.SharingLeaseCleanup.Enabled)
	require.Equal(t, 30, cfg.SharingLeaseCleanup.IntervalSeconds)
	require.Equal(t, 50, cfg.SharingLeaseCleanup.BatchSize)
}

func TestValidateAccountSharingLeaseCleanupBounds(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)

	cfg.SharingLeaseCleanup.IntervalSeconds = 4
	require.ErrorContains(t, cfg.Validate(), "account_sharing_lease_cleanup.interval_seconds")

	cfg.SharingLeaseCleanup.IntervalSeconds = 30
	cfg.SharingLeaseCleanup.BatchSize = 101
	require.ErrorContains(t, cfg.Validate(), "account_sharing_lease_cleanup.batch_size")
}
