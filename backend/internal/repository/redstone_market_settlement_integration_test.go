//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/market"
	"github.com/stretchr/testify/require"
)

func TestRedstoneMarketSettlementWorkerUsesPostgresCompatibleDueQuery(t *testing.T) {
	repository, err := market.NewPostgresRepository(integrationDB)
	require.NoError(t, err)

	settlementRepository, ok := repository.(market.SettlementRepository)
	require.True(t, ok, "market PostgreSQL repository must implement settlement operations")

	result, err := settlementRepository.SettleDueOrders(context.Background(), time.Unix(0, 0).UTC(), 1)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, result.Skipped)
}
