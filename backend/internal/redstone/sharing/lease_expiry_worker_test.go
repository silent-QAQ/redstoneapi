package sharing

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type leaseExpiryWorkerRunner struct {
	calls atomic.Int32
}

func (r *leaseExpiryWorkerRunner) ExpireDueLeases(context.Context, int) (LeaseExpiryBatchResult, error) {
	r.calls.Add(1)
	return LeaseExpiryBatchResult{}, nil
}

func TestLeaseExpiryWorkerStartsRunsAndStops(t *testing.T) {
	runner := &leaseExpiryWorkerRunner{}
	worker := NewLeaseExpiryWorker(runner, time.Hour, 10)
	worker.Start()
	require.Eventually(t, func() bool { return runner.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	require.True(t, worker.Running())
	worker.Stop()
	require.False(t, worker.Running())
}

func TestLeaseExpiryWorkerRejectsInvalidRuntimeOptions(t *testing.T) {
	runner := &leaseExpiryWorkerRunner{}
	for _, worker := range []*LeaseExpiryWorker{
		NewLeaseExpiryWorker(runner, 0, 10),
		NewLeaseExpiryWorker(runner, time.Second, 0),
		NewLeaseExpiryWorker(runner, time.Second, 101),
	} {
		worker.Start()
		require.False(t, worker.Running())
	}
}
