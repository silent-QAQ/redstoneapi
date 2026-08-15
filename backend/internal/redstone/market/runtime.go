package market

import (
	"context"
	"errors"
	"sync"
	"time"
)

const marketRuntimeProbeTimeout = 3 * time.Second

var ErrRuntimeHealthCheckUnavailable = errors.New("market runtime health check is unavailable")

type RuntimeDependencyHealth struct {
	Configured bool   `json:"configured"`
	Healthy    bool   `json:"healthy"`
	Status     string `json:"status"`
	LatencyMS  int64  `json:"latency_ms"`
}

type DeliveryScanQueueHealth struct {
	Available       bool       `json:"available"`
	Status          string     `json:"status"`
	Pending         int64      `json:"pending"`
	Processing      int64      `json:"processing"`
	RetryScheduled  int64      `json:"retry_scheduled"`
	StaleProcessing int64      `json:"stale_processing"`
	Passed          int64      `json:"passed"`
	Rejected        int64      `json:"rejected"`
	OldestPendingAt *time.Time `json:"oldest_pending_at,omitempty"`
}

type DeliveryScanWorkerHealth struct {
	Configured          bool       `json:"configured"`
	Running             bool       `json:"running"`
	IntervalSeconds     int64      `json:"interval_seconds"`
	BatchSize           int        `json:"batch_size"`
	LastStartedAt       *time.Time `json:"last_started_at,omitempty"`
	LastCompletedAt     *time.Time `json:"last_completed_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	LastFailureClass    string     `json:"last_failure_class,omitempty"`
	LastProcessed       int        `json:"last_processed"`
	LastRetried         int        `json:"last_retried"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

type MarketplaceRuntimeHealth struct {
	Healthy   bool                     `json:"healthy"`
	Scope     string                   `json:"scope"`
	CheckedAt time.Time                `json:"checked_at"`
	Storage   RuntimeDependencyHealth  `json:"object_storage"`
	Scanner   RuntimeDependencyHealth  `json:"clamav"`
	Worker    DeliveryScanWorkerHealth `json:"scan_worker"`
	Queue     DeliveryScanQueueHealth  `json:"scan_queue"`
}

type runtimeHealthChecker interface {
	HealthCheck(context.Context) error
}

type deliveryScanRuntimeRepository interface {
	DeliveryScanQueueHealth(context.Context) (DeliveryScanQueueHealth, error)
}

type deliveryScanRuntimeState struct {
	mu     sync.RWMutex
	health DeliveryScanWorkerHealth
}

func newDeliveryScanRuntimeState() *deliveryScanRuntimeState {
	return &deliveryScanRuntimeState{}
}

func (s *deliveryScanRuntimeState) configure(interval time.Duration, batch int) {
	if s == nil || interval <= 0 || batch < 1 || batch > 100 {
		return
	}
	s.mu.Lock()
	s.health.Configured = true
	s.health.IntervalSeconds = int64(interval / time.Second)
	s.health.BatchSize = batch
	s.mu.Unlock()
}

func (s *deliveryScanRuntimeState) started(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.health.Running = true
	s.health.LastStartedAt = timePointer(now)
	s.mu.Unlock()
}

func (s *deliveryScanRuntimeState) stopped() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.health.Running = false
	s.mu.Unlock()
}

func (s *deliveryScanRuntimeState) completed(now time.Time, result DeliveryScanBatchResult, failureClass string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.health.LastCompletedAt = timePointer(now)
	s.health.LastProcessed = result.Processed
	s.health.LastRetried = result.Retried
	if failureClass == "" {
		s.health.LastSuccessAt = timePointer(now)
		s.health.LastFailureClass = ""
		s.health.ConsecutiveFailures = 0
	} else {
		s.health.LastFailureAt = timePointer(now)
		s.health.LastFailureClass = failureClass
		s.health.ConsecutiveFailures++
	}
	s.mu.Unlock()
}

func (s *deliveryScanRuntimeState) snapshot() DeliveryScanWorkerHealth {
	if s == nil {
		return DeliveryScanWorkerHealth{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.health
}

func (s *Service) MarketplaceRuntimeHealth(ctx context.Context) MarketplaceRuntimeHealth {
	now := time.Now().UTC()
	result := MarketplaceRuntimeHealth{
		Scope: "process_and_shared_queue", CheckedAt: now,
		Storage: unconfiguredRuntimeDependency(), Scanner: unconfiguredRuntimeDependency(),
		Worker: s.scanRuntime.snapshot(),
		Queue:  DeliveryScanQueueHealth{Status: "unavailable"},
	}

	var storage, scanner RuntimeDependencyHealth
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		storage = probeRuntimeDependency(ctx, s.deliveryResolver)
	}()
	go func() {
		defer wg.Done()
		scanner = probeRuntimeDependency(ctx, s.deliveryScanner)
	}()

	if repository, ok := s.repository.(deliveryScanRuntimeRepository); ok {
		queue, err := repository.DeliveryScanQueueHealth(ctx)
		if err == nil {
			queue.Available = true
			queue.Status = "ok"
			result.Queue = queue
		}
	}
	wg.Wait()
	result.Storage = storage
	result.Scanner = scanner
	result.Healthy = storage.Healthy && scanner.Healthy && result.Worker.Running && result.Queue.Available && result.Queue.StaleProcessing == 0
	return result
}

func probeRuntimeDependency(parent context.Context, dependency any) RuntimeDependencyHealth {
	checker, ok := dependency.(runtimeHealthChecker)
	if !ok || checker == nil {
		return unconfiguredRuntimeDependency()
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, marketRuntimeProbeTimeout)
	defer cancel()
	err := checker.HealthCheck(ctx)
	health := RuntimeDependencyHealth{Configured: true, Healthy: err == nil, Status: "ok", LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		health.Status = "unreachable"
	}
	return health
}

func unconfiguredRuntimeDependency() RuntimeDependencyHealth {
	return RuntimeDependencyHealth{Status: "unconfigured"}
}

func timePointer(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}
