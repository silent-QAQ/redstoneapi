package market

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	marketDeliveryScanLease      = 2 * time.Minute
	marketDeliveryScanRetryDelay = 15 * time.Second
	marketDeliveryScanRunTimeout = 45 * time.Second
)

var ErrDeliveryScanUnavailable = errors.New("market delivery scan is unavailable")

type deliveryScanJob struct {
	DeliveryItem
	ProductID  int64
	LeaseToken uuid.UUID
}

type deliveryScanRepository interface {
	ClaimDeliveryScan(context.Context, time.Duration) (deliveryScanJob, bool, error)
	CompleteDeliveryScan(context.Context, deliveryScanJob, SellerScanResult, decimal.Decimal) error
	RetryDeliveryScan(context.Context, deliveryScanJob, time.Duration) error
}

// DeliveryScanBatchResult contains no content, object keys, scanner response,
// or account details. It is safe for aggregate worker logging.
type DeliveryScanBatchResult struct {
	Processed          int `json:"processed"`
	Retried            int `json:"retried"`
	ObjectStoreRetries int `json:"object_store_retries"`
	ScannerRetries     int `json:"scanner_retries"`
}

// ProcessPendingDeliveryScans claims one encrypted item at a time. The lease
// is committed before object I/O so another node can recover abandoned work;
// the lease token prevents a stale worker from writing a verdict afterwards.
func (s *Service) ProcessPendingDeliveryScans(ctx context.Context, limit int) (DeliveryScanBatchResult, error) {
	if limit <= 0 || limit > 100 {
		return DeliveryScanBatchResult{}, ErrInvalidPagination
	}
	if s == nil || s.deliveryScanner == nil {
		return DeliveryScanBatchResult{}, ErrDeliveryScanUnavailable
	}
	resolver, ok := s.deliveryResolver.(*EncryptedDeliveryResolver)
	if !ok || resolver == nil {
		return DeliveryScanBatchResult{}, ErrDeliveryScanUnavailable
	}
	repository, ok := s.repository.(deliveryScanRepository)
	if !ok {
		return DeliveryScanBatchResult{}, ErrDeliveryScanUnavailable
	}
	cnyPerBalance, err := s.sellerCNYPerBalance(ctx)
	if err != nil {
		return DeliveryScanBatchResult{}, err
	}
	result := DeliveryScanBatchResult{}
	for range limit {
		job, claimed, err := repository.ClaimDeliveryScan(ctx, marketDeliveryScanLease)
		if err != nil {
			return result, err
		}
		if !claimed {
			return result, nil
		}
		retryClass, err := s.processClaimedDeliveryScan(ctx, resolver, repository, job, cnyPerBalance)
		if err != nil {
			return result, err
		}
		if retryClass != "" {
			result.Retried++
			if retryClass == "object_store" {
				result.ObjectStoreRetries++
			} else if retryClass == "scanner" {
				result.ScannerRetries++
			}
		} else {
			result.Processed++
		}
	}
	return result, nil
}

func (s *Service) processClaimedDeliveryScan(ctx context.Context, resolver *EncryptedDeliveryResolver, repository deliveryScanRepository, job deliveryScanJob, cnyPerBalance decimal.Decimal) (string, error) {
	plaintext, err := resolver.resolve(ctx, job.DeliveryItem)
	if err != nil {
		return "object_store", repository.RetryDeliveryScan(ctx, job, marketDeliveryScanRetryDelay)
	}
	defer zeroBytes(plaintext)
	verdict, err := s.deliveryScanner.Scan(ctx, DeliveryScanInput{
		ProductType: job.ProductType,
		ContentType: job.ContentType,
		Content:     plaintext,
	})
	if err != nil || (verdict != SellerScanPassed && verdict != SellerScanRejected && verdict != SellerScanFlagged) {
		return "scanner", repository.RetryDeliveryScan(ctx, job, marketDeliveryScanRetryDelay)
	}
	return "", repository.CompleteDeliveryScan(ctx, job, verdict, cnyPerBalance)
}

func (r *sqlRepository) DeliveryScanQueueHealth(ctx context.Context) (DeliveryScanQueueHealth, error) {
	var result DeliveryScanQueueHealth
	err := r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE state = 'pending'),
			COUNT(*) FILTER (WHERE state = 'processing'),
			COUNT(*) FILTER (WHERE state = 'pending' AND available_at > NOW()),
			COUNT(*) FILTER (WHERE state = 'processing' AND lease_expires_at < NOW()),
			COUNT(*) FILTER (WHERE state = 'passed'),
			COUNT(*) FILTER (WHERE state = 'rejected'),
			MIN(available_at) FILTER (WHERE state = 'pending')
		FROM redstone_market_delivery_scan_jobs
	`).Scan(
		&result.Pending, &result.Processing, &result.RetryScheduled,
		&result.StaleProcessing, &result.Passed, &result.Rejected,
		&result.OldestPendingAt,
	)
	return result, err
}

func (r *sqlRepository) ClaimDeliveryScan(ctx context.Context, lease time.Duration) (_ deliveryScanJob, claimed bool, err error) {
	if lease <= 0 {
		return deliveryScanJob{}, false, ErrDeliveryScanUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deliveryScanJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var job deliveryScanJob
	err = tx.QueryRowContext(ctx, `
		SELECT j.delivery_item_id, j.product_id,
			p.product_type, di.status, di.account_id, di.encrypted_object_key,
			di.key_version, di.wrapped_dek, COALESCE(di.content_sha256, ''),
			di.content_type, di.byte_size
		FROM redstone_market_delivery_scan_jobs j
		JOIN redstone_market_delivery_items di ON di.id = j.delivery_item_id
		JOIN redstone_market_products p ON p.id = j.product_id
		WHERE (j.state = 'pending' AND j.available_at <= NOW())
		   OR (j.state = 'processing' AND j.lease_expires_at < NOW())
		ORDER BY j.available_at, j.delivery_item_id
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 1
	`).Scan(
		&job.ID, &job.ProductID,
		&job.ProductType, &job.Status, &job.AccountID, &job.EncryptedObjectKey,
		&job.KeyVersion, &job.WrappedDEK, &job.ContentSHA256, &job.ContentType, &job.ByteSize,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return deliveryScanJob{}, false, err
		}
		return deliveryScanJob{}, false, nil
	}
	if err != nil {
		return deliveryScanJob{}, false, err
	}
	job.LeaseToken = uuid.New()
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_delivery_scan_jobs
		SET state = 'processing', attempts = attempts + 1, lease_token = $2,
			lease_expires_at = NOW() + make_interval(secs => $3), updated_at = NOW()
		WHERE delivery_item_id = $1
	`, job.ID, job.LeaseToken, int64(lease.Seconds()))
	if err != nil {
		return deliveryScanJob{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return deliveryScanJob{}, false, err
		}
		return deliveryScanJob{}, false, ErrDeliveryScanUnavailable
	}
	if err := tx.Commit(); err != nil {
		return deliveryScanJob{}, false, err
	}
	return job, true, nil
}

func (r *sqlRepository) RetryDeliveryScan(ctx context.Context, job deliveryScanJob, delay time.Duration) error {
	if job.ID <= 0 || job.LeaseToken == uuid.Nil {
		return ErrDeliveryScanUnavailable
	}
	if delay <= 0 {
		delay = marketDeliveryScanRetryDelay
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE redstone_market_delivery_scan_jobs
		SET state = 'pending', available_at = NOW() + make_interval(secs => $3),
			lease_token = NULL, lease_expires_at = NULL, updated_at = NOW()
		WHERE delivery_item_id = $1 AND state = 'processing' AND lease_token = $2
			AND lease_expires_at > NOW()
	`, job.ID, job.LeaseToken, int64(delay.Seconds()))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrDeliveryScanUnavailable
	}
	return nil
}

func (r *sqlRepository) CompleteDeliveryScan(ctx context.Context, job deliveryScanJob, verdict SellerScanResult, cnyPerBalance decimal.Decimal) error {
	if job.ID <= 0 || job.LeaseToken == uuid.Nil || (verdict != SellerScanPassed && verdict != SellerScanRejected && verdict != SellerScanFlagged) {
		return ErrDeliveryScanUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var productID int64
	var state string
	err = tx.QueryRowContext(ctx, `
		SELECT product_id, state FROM redstone_market_delivery_scan_jobs
		WHERE delivery_item_id = $1 AND lease_token = $2 AND state = 'processing'
			AND lease_expires_at > NOW()
		FOR UPDATE
	`, job.ID, job.LeaseToken).Scan(&productID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeliveryScanUnavailable
	}
	if err != nil {
		return err
	}
	if state != "processing" || productID != job.ProductID {
		return ErrDeliveryScanUnavailable
	}
	jobState := "passed"
	if verdict != SellerScanPassed {
		jobState = "rejected"
	}
	update, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_delivery_scan_jobs
		SET state = $2, lease_token = NULL, lease_expires_at = NULL, completed_at = NOW(), updated_at = NOW()
		WHERE delivery_item_id = $1 AND state = 'processing' AND lease_token = $3
			AND lease_expires_at > NOW()
	`, job.ID, jobState, job.LeaseToken)
	if err != nil {
		return err
	}
	if affected, err := update.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrDeliveryScanUnavailable
	}
	var sellerUserID int64
	var sellerKind, status string
	var inventoryTotal, inventoryReserved int
	if err := tx.QueryRowContext(ctx, `
		SELECT seller_user_id, seller_kind, status, inventory_total, inventory_reserved
		FROM redstone_market_products WHERE id = $1 FOR UPDATE
	`, productID).Scan(&sellerUserID, &sellerKind, &status, &inventoryTotal, &inventoryReserved); err != nil {
		return err
	}
	if status != "pending_scan" {
		return tx.Commit()
	}
	if verdict != SellerScanPassed {
		failure := "scanner_rejected"
		if verdict == SellerScanFlagged {
			failure = "scanner_flagged"
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE redstone_market_products
			SET status = 'suspended', risk_status = $2, scan_completed_at = NOW(),
				scan_failure_reason = $3, updated_at = NOW()
			WHERE id = $1
		`, productID, verdict, failure)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	var pendingContentReview bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM redstone_market_content_reviews
			WHERE product_id = $1 AND review_state = 'open'
		)
	`, productID).Scan(&pendingContentReview); err != nil {
		return err
	}
	if pendingContentReview {
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_products
			SET status = 'suspended', risk_status = 'flagged', scan_completed_at = NOW(),
				scan_failure_reason = 'content_review_pending', updated_at = NOW()
			WHERE id = $1
		`, productID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var remaining bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM redstone_market_delivery_scan_jobs
			WHERE product_id = $1 AND state IN ('pending', 'processing')
		)
	`, productID).Scan(&remaining); err != nil {
		return err
	}
	if remaining {
		return tx.Commit()
	}
	nextStatus := "draft"
	if inventoryTotal > inventoryReserved {
		if sellerKind == "official" {
			nextStatus = "active"
		} else {
			dashboard, err := r.lockSellerDashboard(ctx, tx, sellerUserID, cnyPerBalance, productID)
			if err != nil && !errors.Is(err, ErrSellerFrozen) {
				return err
			}
			if err == nil && dashboard.ListingLimit > 0 && dashboard.ActiveListings < dashboard.ListingLimit {
				nextStatus = "active"
			} else if errors.Is(err, ErrSellerFrozen) {
				nextStatus = "suspended"
			}
		}
	}
	publishNow := nextStatus == "active"
	_, err = tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = $2, risk_status = 'passed', scan_completed_at = NOW(), scan_failure_reason = '',
			published_at = CASE WHEN $3 THEN COALESCE(published_at, NOW()) ELSE published_at END,
			updated_at = NOW()
		WHERE id = $1
	`, productID, nextStatus, publishNow)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// DeliveryScanWorker is intentionally separate from settlement. Scanning
// performs object I/O, while settlement only changes wallet-ledger state.
type DeliveryScanWorker struct {
	service  *Service
	interval time.Duration
	batch    int

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewDeliveryScanWorker(service *Service, interval time.Duration, batch int) *DeliveryScanWorker {
	worker := &DeliveryScanWorker{service: service, interval: interval, batch: batch}
	if service != nil && service.scanRuntime != nil {
		service.scanRuntime.configure(interval, batch)
	}
	return worker
}

func ProvideDeliveryScanWorker(service *Service, cfg *config.Config) *DeliveryScanWorker {
	if service == nil || cfg == nil || (!cfg.MarketplaceScanner.Active() && !PreviewInfrastructureEnabled()) || !cfg.MarketplaceScanner.WorkerEnabled {
		return nil
	}
	worker := NewDeliveryScanWorker(service, time.Duration(cfg.MarketplaceScanner.WorkerIntervalSeconds)*time.Second, cfg.MarketplaceScanner.WorkerBatchSize)
	worker.Start()
	return worker
}

func (w *DeliveryScanWorker) Start() {
	if w == nil || w.service == nil || w.interval <= 0 || w.batch < 1 || w.batch > 100 {
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
	if w.service.scanRuntime != nil {
		w.service.scanRuntime.started(time.Now().UTC())
	}
	go w.run(ctx, w.done)
}

func (w *DeliveryScanWorker) run(ctx context.Context, done chan struct{}) {
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

func (w *DeliveryScanWorker) runOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, marketDeliveryScanRunTimeout)
	defer cancel()
	result, err := w.service.ProcessPendingDeliveryScans(ctx, w.batch)
	failureClass := ""
	if err != nil && ctx.Err() == nil {
		failureClass = deliveryScanFailureClass(err)
		slog.Error("market_delivery_scan_run_failed", "failure_class", failureClass)
	} else if ctx.Err() != nil && parent.Err() == nil {
		failureClass = "timeout"
		slog.Error("market_delivery_scan_run_failed", "failure_class", failureClass)
	}
	if w.service.scanRuntime != nil {
		w.service.scanRuntime.completed(time.Now().UTC(), result, failureClass)
	}
	if failureClass == "" && (result.Processed > 0 || result.Retried > 0) {
		slog.Info("market_delivery_scan_run_completed", "processed", result.Processed, "retried", result.Retried,
			"object_store_retries", result.ObjectStoreRetries, "scanner_retries", result.ScannerRetries)
	}
}

func deliveryScanFailureClass(err error) string {
	switch {
	case errors.Is(err, ErrDeliveryScanUnavailable):
		return "runtime_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "repository"
	}
}

func (w *DeliveryScanWorker) Stop() {
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
	if w.service != nil && w.service.scanRuntime != nil {
		w.service.scanRuntime.stopped()
	}
}

func (w *DeliveryScanWorker) Running() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cancel != nil
}
