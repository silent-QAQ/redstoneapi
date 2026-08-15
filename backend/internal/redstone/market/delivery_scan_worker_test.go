package market

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type deliveryScanRepositoryStub struct {
	sellerRepositoryStub
	jobs      []deliveryScanJob
	completed []SellerScanResult
	retried   int
}

func (r *deliveryScanRepositoryStub) ClaimDeliveryScan(_ context.Context, _ time.Duration) (deliveryScanJob, bool, error) {
	if len(r.jobs) == 0 {
		return deliveryScanJob{}, false, nil
	}
	job := r.jobs[0]
	r.jobs = r.jobs[1:]
	return job, true, nil
}

func (r *deliveryScanRepositoryStub) CompleteDeliveryScan(_ context.Context, job deliveryScanJob, verdict SellerScanResult, _ decimal.Decimal) error {
	if job.LeaseToken == uuid.Nil {
		return ErrDeliveryScanUnavailable
	}
	r.completed = append(r.completed, verdict)
	return nil
}

func (r *deliveryScanRepositoryStub) RetryDeliveryScan(_ context.Context, job deliveryScanJob, _ time.Duration) error {
	if job.LeaseToken == uuid.Nil {
		return ErrDeliveryScanUnavailable
	}
	r.retried++
	return nil
}

func TestProcessPendingDeliveryScansDecryptsOnlyForLocalScanner(t *testing.T) {
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, testEnvelopeCipher(t))
	require.NoError(t, err)
	objectKey := "redstone-market/delivery/scan-job"
	payload, err := resolver.Store(context.Background(), objectKey, []byte("one-time-scan-content"))
	require.NoError(t, err)
	repository := &deliveryScanRepositoryStub{jobs: []deliveryScanJob{{
		DeliveryItem: DeliveryItem{
			ID:                 44,
			ProductType:        "text_key",
			Status:             "available",
			EncryptedObjectKey: objectKey,
			KeyVersion:         resolver.cipher.KeyVersion(),
			WrappedDEK:         payload.WrappedDEK,
			ContentType:        "text/plain",
		},
		ProductID:  19,
		LeaseToken: uuid.New(),
	}}}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(resolver)
	scanner := &deliveryScannerStub{result: SellerScanPassed}
	service.SetDeliveryScanner(scanner)

	result, err := service.ProcessPendingDeliveryScans(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed)
	require.Zero(t, result.Retried)
	require.Equal(t, 1, scanner.calls)
	require.Equal(t, []SellerScanResult{SellerScanPassed}, repository.completed)
	require.NotContains(t, string(store.objects[objectKey]), "one-time-scan-content")
}

func TestProcessPendingDeliveryScansRetriesScannerFailureWithoutVerdict(t *testing.T) {
	store := &memoryPrivateObjectStore{}
	resolver, err := NewEncryptedDeliveryResolver(store, testEnvelopeCipher(t))
	require.NoError(t, err)
	objectKey := "redstone-market/delivery/retry-job"
	payload, err := resolver.Store(context.Background(), objectKey, []byte("retry-content"))
	require.NoError(t, err)
	repository := &deliveryScanRepositoryStub{jobs: []deliveryScanJob{{
		DeliveryItem: DeliveryItem{ID: 45, ProductType: "file", Status: "available", EncryptedObjectKey: objectKey, KeyVersion: resolver.cipher.KeyVersion(), WrappedDEK: payload.WrappedDEK, ContentType: "application/octet-stream"},
		ProductID:    20, LeaseToken: uuid.New(),
	}}}
	service, err := NewService(repository)
	require.NoError(t, err)
	service.SetDeliveryContentResolver(resolver)
	service.SetDeliveryScanner(&deliveryScannerStub{err: errors.New("scanner offline")})

	result, err := service.ProcessPendingDeliveryScans(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Equal(t, 1, result.Retried)
	require.Empty(t, repository.completed)
	require.Equal(t, 1, repository.retried)
}
