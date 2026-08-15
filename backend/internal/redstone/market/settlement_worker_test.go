package market

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type settlementWorkerRunner struct {
	calls atomic.Int32
}

func (r *settlementWorkerRunner) SettleDueOrders(context.Context, time.Time, int) (SettlementBatchResult, error) {
	r.calls.Add(1)
	return SettlementBatchResult{}, nil
}

func TestSettlementWorkerStartsRunsAndStops(t *testing.T) {
	runner := &settlementWorkerRunner{}
	worker := NewSettlementWorker(runner, time.Hour, 10)
	worker.Start()
	require.Eventually(t, func() bool { return runner.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	require.True(t, worker.Running())
	worker.Stop()
	require.False(t, worker.Running())
}

func TestSettlementWorkerRejectsInvalidRuntimeOptions(t *testing.T) {
	runner := &settlementWorkerRunner{}
	for _, worker := range []*SettlementWorker{
		NewSettlementWorker(runner, 0, 10),
		NewSettlementWorker(runner, time.Second, 0),
		NewSettlementWorker(runner, time.Second, 101),
	} {
		worker.Start()
		require.False(t, worker.Running())
	}
}
