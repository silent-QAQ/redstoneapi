package market

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/config"
)

const marketSettlementRunTimeout = 30 * time.Second

type dueSettlementRunner interface {
	SettleDueOrders(context.Context, time.Time, int) (SettlementBatchResult, error)
}

// SettlementWorker runs the 24-hour release policy outside request handlers.
// It holds no delivery content and logs only aggregate outcome counts.
type SettlementWorker struct {
	runner   dueSettlementRunner
	interval time.Duration
	batch    int

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewSettlementWorker(runner dueSettlementRunner, interval time.Duration, batch int) *SettlementWorker {
	return &SettlementWorker{runner: runner, interval: interval, batch: batch}
}

// ProvideSettlementWorker owns runtime startup through wire and is paired with
// Stop in the server cleanup chain.
func ProvideSettlementWorker(service *Service, cfg *config.Config) *SettlementWorker {
	if service == nil || cfg == nil || !cfg.MarketplaceSettlement.Enabled {
		return nil
	}
	worker := NewSettlementWorker(
		service,
		time.Duration(cfg.MarketplaceSettlement.IntervalSeconds)*time.Second,
		cfg.MarketplaceSettlement.BatchSize,
	)
	worker.Start()
	return worker
}

func (w *SettlementWorker) Start() {
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

func (w *SettlementWorker) run(ctx context.Context, done chan struct{}) {
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

func (w *SettlementWorker) runOnce(parent context.Context) {
	if w == nil || w.runner == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, marketSettlementRunTimeout)
	defer cancel()
	result, err := w.runner.SettleDueOrders(ctx, time.Now().UTC(), w.batch)
	if err != nil && ctx.Err() == nil {
		slog.Error("market_settlement_run_failed", "error", err)
		return
	}
	if result.Processed > 0 || result.Skipped > 0 {
		slog.Info("market_settlement_run_completed", "processed", result.Processed, "skipped", result.Skipped)
	}
}

func (w *SettlementWorker) Stop() {
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

func (w *SettlementWorker) Running() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cancel != nil
}
