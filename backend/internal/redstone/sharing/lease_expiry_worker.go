package sharing

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/config"
)

const leaseExpiryRunTimeout = 30 * time.Second

// LeaseExpiryBatchResult describes one database-backed cleanup pass. A
// promoted membership is intentionally not granted private-group access: it
// must still pass the paid AcquireAndSettle admission path.
type LeaseExpiryBatchResult struct {
	Processed int
	Promoted  int
}

type dueLeaseExpiryRunner interface {
	ExpireDueLeases(context.Context, int) (LeaseExpiryBatchResult, error)
}

// LeaseExpiryWorker expires abandoned sharing leases outside request paths.
// PostgreSQL row locks make it safe for every application node to run one.
type LeaseExpiryWorker struct {
	runner   dueLeaseExpiryRunner
	interval time.Duration
	batch    int

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewLeaseExpiryWorker(runner dueLeaseExpiryRunner, interval time.Duration, batch int) *LeaseExpiryWorker {
	return &LeaseExpiryWorker{runner: runner, interval: interval, batch: batch}
}

// ProvideLeaseExpiryWorker owns worker startup through wire and is paired with
// Stop in the application cleanup chain.
func ProvideLeaseExpiryWorker(repository *PostgresRepository, cfg *config.Config) *LeaseExpiryWorker {
	if repository == nil || cfg == nil || !cfg.SharingLeaseCleanup.Enabled {
		return nil
	}
	worker := NewLeaseExpiryWorker(
		repository,
		time.Duration(cfg.SharingLeaseCleanup.IntervalSeconds)*time.Second,
		cfg.SharingLeaseCleanup.BatchSize,
	)
	worker.Start()
	return worker
}

func (w *LeaseExpiryWorker) Start() {
	if w == nil || w.runner == nil || w.interval <= 0 || w.batch < 1 || w.batch > 100 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.run(ctx, w.done)
}

func (w *LeaseExpiryWorker) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	w.runOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *LeaseExpiryWorker) runOnce(parent context.Context) {
	if w == nil || w.runner == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, leaseExpiryRunTimeout)
	defer cancel()
	result, err := w.runner.ExpireDueLeases(ctx, w.batch)
	if err != nil && ctx.Err() == nil {
		slog.Error("sharing_lease_cleanup_run_failed", "error", err)
		return
	}
	if result.Processed > 0 || result.Promoted > 0 {
		slog.Info("sharing_lease_cleanup_run_completed", "processed", result.Processed, "promoted", result.Promoted)
	}
}

func (w *LeaseExpiryWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (w *LeaseExpiryWorker) Running() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cancel != nil
}
