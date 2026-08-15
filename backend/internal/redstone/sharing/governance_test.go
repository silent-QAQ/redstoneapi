package sharing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type governanceRepositoryStub struct {
	fakeRepository
	policyRequest UpdateSharingPolicyRequest
	quotaRequest  UpdateQuotaPolicyRequest
}

func (r *governanceRepositoryStub) GetSharingPolicy(context.Context) (SharingPolicy, error) {
	return SharingPolicy{Version: 3, PublicRoomAllowed: true, MaxLeaseSeconds: 3600, DefaultPlatformFeeRate: decimal.RequireFromString("0.05000000")}, nil
}

func (r *governanceRepositoryStub) UpdateSharingPolicy(_ context.Context, request UpdateSharingPolicyRequest) (SharingPolicy, error) {
	r.policyRequest = request
	return SharingPolicy{Version: 4, PublicRoomAllowed: request.PublicRoomAllowed, MaxLeaseSeconds: request.MaxLeaseSeconds, DefaultPlatformFeeRate: request.DefaultPlatformFeeRate, Reason: request.Reason}, nil
}

func (r *governanceRepositoryStub) GetQuotaPolicy(_ context.Context, ownerUserID *int64) (QuotaPolicy, error) {
	return QuotaPolicy{Version: 9, Scope: QuotaScopeGlobal, OwnerUserID: ownerUserID, MaxLiveRooms: 5, MaxAccountsPerRoom: 10, MaxRoomsCreatedPerDay: 5}, nil
}

func (r *governanceRepositoryStub) UpdateQuotaPolicy(_ context.Context, request UpdateQuotaPolicyRequest) (QuotaPolicy, error) {
	r.quotaRequest = request
	return QuotaPolicy{Version: 10, Scope: request.scope(), OwnerUserID: request.OwnerUserID, MaxLiveRooms: request.MaxLiveRooms, MaxAccountsPerRoom: request.MaxAccountsPerRoom, MaxRoomsCreatedPerDay: request.MaxRoomsCreatedPerDay, Reason: request.Reason}, nil
}

func TestGovernanceServiceRejectsInvalidUpdatesBeforeRepository(t *testing.T) {
	repository := &governanceRepositoryStub{}
	service := &Service{repository: repository, wallet: &wallet.Service{}}

	_, err := service.UpdateSharingPolicy(context.Background(), UpdateSharingPolicyRequest{
		ActorUserID: 1, PublicRoomAllowed: true, MaxLeaseSeconds: 59, DefaultPlatformFeeRate: decimal.Zero, Reason: "invalid",
	})
	require.Equal(t, int32(http.StatusBadRequest), infraerrors.FromError(err).Code)
	require.Zero(t, repository.policyRequest.ActorUserID)

	_, err = service.UpdateQuotaPolicy(context.Background(), UpdateQuotaPolicyRequest{
		ActorUserID: 1, MaxLiveRooms: 5, MaxAccountsPerRoom: 31, MaxRoomsCreatedPerDay: 5, Reason: "invalid",
	})
	require.Equal(t, int32(http.StatusBadRequest), infraerrors.FromError(err).Code)
	require.Zero(t, repository.quotaRequest.ActorUserID)
}

func TestGovernanceServiceDelegatesVersionedUpdates(t *testing.T) {
	repository := &governanceRepositoryStub{}
	service := &Service{repository: repository, wallet: &wallet.Service{}}
	ownerID := int64(42)
	expiresAt := time.Now().UTC().Add(time.Hour)

	policy, err := service.UpdateSharingPolicy(context.Background(), UpdateSharingPolicyRequest{
		ActorUserID: 7, PublicRoomAllowed: false, MaxLeaseSeconds: 7200,
		DefaultPlatformFeeRate: decimal.RequireFromString("0.07500000"), Reason: "  调整共享费率  ",
	})
	require.NoError(t, err)
	require.Equal(t, int64(4), policy.Version)
	require.Equal(t, "调整共享费率", repository.policyRequest.Reason)

	quota, err := service.UpdateQuotaPolicy(context.Background(), UpdateQuotaPolicyRequest{
		ActorUserID: 7, OwnerUserID: &ownerID, MaxLiveRooms: 8, MaxAccountsPerRoom: 12,
		MaxRoomsCreatedPerDay: 4, ExpiresAt: &expiresAt, Reason: "  owner override  ",
	})
	require.NoError(t, err)
	require.Equal(t, QuotaScopeOwner, quota.Scope)
	require.Equal(t, "owner override", repository.quotaRequest.Reason)
}

func TestPostgresUpdateSharingPolicyCreatesVersionAndAuditInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	columns := []string{"id", "version", "public_room_allowed", "max_lease_seconds", "default_platform_fee_rate", "created_by_user_id", "reason", "created_at"}
	request := UpdateSharingPolicyRequest{
		ActorUserID: 7, PublicRoomAllowed: false, MaxLeaseSeconds: 7200,
		DefaultPlatformFeeRate: decimal.RequireFromString("0.07500000"), Reason: "运营调整",
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, version, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason, created_at
		FROM redstone_account_share_policies
		WHERE status = 'active'
		FOR UPDATE`)).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(11), int64(3), true, 3600, "0.05000000", nil, "旧版本", now))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE redstone_account_share_policies
		SET status = 'superseded'
		WHERE id = $1 AND status = 'active'`)).
		WithArgs(int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO redstone_account_share_policies (
			version, status, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason
		) VALUES ($1, 'active', $2, $3, $4, $5, $6)
		RETURNING id, version, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason, created_at`)).
		WithArgs(int64(4), false, 7200, decimal.RequireFromString("0.07500000"), int64(7), "运营调整").
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(12), int64(4), false, 7200, "0.07500000", int64(7), "运营调整", now))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO redstone_account_share_audits (room_id, actor_user_id, action, detail)
		VALUES (NULL, $1, $2, $3)`)).
		WithArgs(int64(7), "policy_version_created", "policy_version=4; reason=运营调整").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	policy, err := repository.UpdateSharingPolicy(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, int64(4), policy.Version)
	require.False(t, policy.PublicRoomAllowed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresGovernanceReadsCurrentPolicyAndOwnerQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, version, public_room_allowed, max_lease_seconds, default_platform_fee_rate,
			created_by_user_id, reason, created_at
		FROM redstone_account_share_policies
		WHERE status = 'active'
		LIMIT 1`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "public_room_allowed", "max_lease_seconds", "default_platform_fee_rate", "created_by_user_id", "reason", "created_at"}).
			AddRow(int64(12), int64(4), false, 7200, "0.07500000", int64(7), "运营调整", now))
	policy, err := repository.GetSharingPolicy(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(4), policy.Version)
	require.False(t, policy.PublicRoomAllowed)

	ownerID := int64(23)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, scope, owner_user_id, max_live_rooms, max_accounts_per_room,
				max_rooms_created_per_day, expires_at, reason, created_by_user_id, created_at
			FROM redstone_account_share_quota_policies
			WHERE scope = 'owner' AND owner_user_id = $1 AND active
				AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY id DESC
			LIMIT 1`)).
		WithArgs(ownerID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "scope", "owner_user_id", "max_live_rooms", "max_accounts_per_room", "max_rooms_created_per_day", "expires_at", "reason", "created_by_user_id", "created_at"}).
			AddRow(int64(101), "owner", ownerID, 8, 12, 4, nil, "owner override", int64(7), now))
	quota, err := repository.GetQuotaPolicy(context.Background(), &ownerID)
	require.NoError(t, err)
	require.Equal(t, QuotaScopeOwner, quota.Scope)
	require.Equal(t, ownerID, *quota.OwnerUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresUpdateOwnerQuotaCreatesVersionAndAuditInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)
	ownerID := int64(23)
	request := UpdateQuotaPolicyRequest{
		ActorUserID: 7, OwnerUserID: &ownerID, MaxLiveRooms: 8, MaxAccountsPerRoom: 12,
		MaxRoomsCreatedPerDay: 4, ExpiresAt: &expiresAt, Reason: "owner override",
	}
	columns := []string{"id", "scope", "owner_user_id", "max_live_rooms", "max_accounts_per_room", "max_rooms_created_per_day", "expires_at", "reason", "created_by_user_id", "created_at"}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE redstone_account_share_quota_policies IN SHARE ROW EXCLUSIVE MODE`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE redstone_account_share_quota_policies
		SET active = FALSE
		WHERE scope = $1 AND active AND owner_user_id IS NOT DISTINCT FROM $2`)).
		WithArgs(string(QuotaScopeOwner), ownerID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO redstone_account_share_quota_policies (
			scope, owner_user_id, max_live_rooms, max_accounts_per_room,
			max_rooms_created_per_day, active, expires_at, reason, created_by_user_id
		) VALUES ($1, $2, $3, $4, $5, TRUE, $6, $7, $8)
		RETURNING id, scope, owner_user_id, max_live_rooms, max_accounts_per_room,
			max_rooms_created_per_day, expires_at, reason, created_by_user_id, created_at`)).
		WithArgs(string(QuotaScopeOwner), ownerID, 8, 12, 4, sqlmock.AnyArg(), "owner override", int64(7)).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(101), "owner", ownerID, 8, 12, 4, expiresAt, "owner override", int64(7), now))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO redstone_account_share_audits (room_id, actor_user_id, action, detail)
		VALUES (NULL, $1, $2, $3)`)).
		WithArgs(int64(7), "quota_version_created", "owner_version=101; reason=owner override").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	quota, err := repository.UpdateQuotaPolicy(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, int64(101), quota.Version)
	require.Equal(t, QuotaScopeOwner, quota.Scope)
	require.Equal(t, ownerID, *quota.OwnerUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharingGovernanceRoutesRequireStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &governanceRepositoryStub{}
	handler := NewHandler(&Service{repository: repository, wallet: &wallet.Service{}})
	router := gin.New()
	stepUpCalls := 0
	RegisterAdminRoutes(router.Group("/admin"), handler, middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusForbidden)
	}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/account-share/settings/policy", nil))
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Equal(t, 1, stepUpCalls)
}
