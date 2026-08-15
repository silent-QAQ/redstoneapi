package market

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestRetryDeliveryScanRejectsExpiredLease(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	job := deliveryScanJob{DeliveryItem: DeliveryItem{ID: 43}, LeaseToken: uuid.New()}

	mock.ExpectExec(regexp.QuoteMeta("AND lease_expires_at > NOW()")).
		WithArgs(job.ID, job.LeaseToken, int64(15)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	err = repository.RetryDeliveryScan(context.Background(), job, 15*time.Second)
	require.ErrorIs(t, err, ErrDeliveryScanUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompleteDeliveryScanRejectsExpiredLease(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	job := deliveryScanJob{DeliveryItem: DeliveryItem{ID: 43}, ProductID: 11, LeaseToken: uuid.New()}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("AND lease_expires_at > NOW()\n\t\tFOR UPDATE")).
		WithArgs(job.ID, job.LeaseToken).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	err = repository.CompleteDeliveryScan(context.Background(), job, SellerScanPassed, decimal.NewFromInt(1))
	require.ErrorIs(t, err, ErrDeliveryScanUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}
