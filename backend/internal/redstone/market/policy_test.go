package market

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestUpdateFeePolicyRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		request UpdateFeePolicyRequest
		wantErr error
	}{
		{name: "accepts quantized fee", request: UpdateFeePolicyRequest{ActorUserID: 1, UserServiceFeeRate: decimal.RequireFromString("0.07500000"), Reason: "运营调整"}},
		{name: "requires actor", request: UpdateFeePolicyRequest{UserServiceFeeRate: decimal.Zero, Reason: "运营调整"}, wantErr: ErrInvalidActor},
		{name: "rejects excessive fee", request: UpdateFeePolicyRequest{ActorUserID: 1, UserServiceFeeRate: decimal.RequireFromString("1.00000001"), Reason: "运营调整"}, wantErr: ErrInvalidFeeRate},
		{name: "requires audit reason", request: UpdateFeePolicyRequest{ActorUserID: 1, UserServiceFeeRate: decimal.Zero, Reason: " "}, wantErr: ErrFeePolicyReason},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestPostgresUpdateFeePolicyWritesImmutableAuditInTheSameTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	request := UpdateFeePolicyRequest{
		ActorUserID: 9, UserServiceFeeRate: decimal.RequireFromString("0.07500000"), Reason: "运营调整",
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_service_fee_rate, updated_by_user_id, updated_at\n\t\tFROM redstone_market_fee_policy WHERE singleton = TRUE FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"user_service_fee_rate", "updated_by_user_id", "updated_at"}).AddRow("0.05000000", nil, time.Now().UTC()))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_fee_policy")).
		WithArgs(request.UserServiceFeeRate, request.ActorUserID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_market_fee_policy_audit")).
		WithArgs(decimal.RequireFromString("0.05000000"), request.UserServiceFeeRate, request.ActorUserID, request.Reason, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	policy, err := repository.UpdateFeePolicy(context.Background(), request)
	require.NoError(t, err)
	require.True(t, policy.UserServiceFeeRate.Equal(request.UserServiceFeeRate))
	require.NotNil(t, policy.UpdatedByUserID)
	require.Equal(t, request.ActorUserID, *policy.UpdatedByUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}
