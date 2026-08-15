package cluster

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDisabledClusterIsReadyWithoutInfrastructure(t *testing.T) {
	manager := NewManager(config.RedstoneClusterConfig{}, nil, nil)
	require.True(t, manager.Ready())
	require.Equal(t, StateActive, manager.State())
	require.NoError(t, manager.Heartbeat(context.Background()))
}

func TestAcquireTaskLeaseUsesFencedToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	manager := NewManager(config.RedstoneClusterConfig{
		Enabled:              true,
		NodeID:               "node-a",
		LeaseDurationSeconds: 30,
	}, db, nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM redstone_cluster_nodes WHERE node_id = $1")).
		WithArgs("node-a").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(StateActive))
	mock.ExpectQuery("INSERT INTO redstone_cluster_task_leases").
		WithArgs("channel-monitor-v2", "node-a", sqlmock.AnyArg(), 30).
		WillReturnRows(sqlmock.NewRows([]string{"fencing_token", "lease_until", "updated_at"}).
			AddRow(int64(8), time.Now().UTC().Add(30*time.Second), time.Now().UTC()))

	lease, err := manager.AcquireTaskLease(context.Background(), "channel-monitor-v2")
	require.NoError(t, err)
	require.Equal(t, "node-a", lease.HolderNodeID)
	require.Equal(t, int64(8), lease.FencingToken)
	require.NotEqual(t, uuid.Nil, lease.LeaseToken)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRenewTaskLeaseRefusesStaleHolder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	manager := NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, db, nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM redstone_cluster_nodes WHERE node_id = $1")).
		WithArgs("node-a").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(StateActive))
	mock.ExpectExec("UPDATE redstone_cluster_task_leases").
		WithArgs("rollup", "node-a", sqlmock.AnyArg(), 20).
		WillReturnResult(driver.RowsAffected(0))

	err = manager.RenewTaskLease(context.Background(), &TaskLease{TaskName: "rollup", LeaseToken: uuid.New()})
	require.ErrorIs(t, err, ErrLeaseNotHeld)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCacheEpochReturnsOneWhenNamespaceWasNotInvalidated(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	manager := NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, db, nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT epoch FROM redstone_cluster_cache_epochs WHERE cache_name = $1")).
		WithArgs("scheduler").
		WillReturnError(sql.ErrNoRows)

	epoch, err := manager.CacheEpoch(context.Background(), "scheduler")
	require.NoError(t, err)
	require.Equal(t, int64(1), epoch)
	require.NoError(t, mock.ExpectationsWereMet())
}
