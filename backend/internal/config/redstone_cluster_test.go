package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedstoneClusterConfigValidate(t *testing.T) {
	valid := RedstoneClusterConfig{
		Enabled:                  true,
		NodeID:                   "test-node",
		HeartbeatIntervalSeconds: 10,
		NodeTimeoutSeconds:       45,
		LeaseDurationSeconds:     30,
		CacheEpochTTLSeconds:     60,
	}
	require.NoError(t, valid.Validate())

	invalidTimeout := valid
	invalidTimeout.NodeTimeoutSeconds = 20
	require.ErrorContains(t, invalidTimeout.Validate(), "node_timeout_seconds")

	invalidLease := valid
	invalidLease.LeaseDurationSeconds = 10
	require.ErrorContains(t, invalidLease.Validate(), "lease_duration_seconds")
}
