package sharing

import (
	"context"
	"testing"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct{}

func (fakeRepository) ListPublicRooms(context.Context, int, int) ([]Room, int, error) {
	return nil, 0, nil
}
func (fakeRepository) ListOwnerRooms(context.Context, int64, int, int) ([]Room, int, error) {
	return nil, 0, nil
}
func (fakeRepository) ListUserMemberships(context.Context, int64, int, int) ([]Membership, int, error) {
	return nil, 0, nil
}
func (fakeRepository) CreateRoom(context.Context, CreateRoomRequest) (Room, error) {
	return Room{}, nil
}
func (fakeRepository) BindAccount(context.Context, BindAccountRequest) error { return nil }
func (fakeRepository) CreateInvite(context.Context, InviteRequest) error     { return nil }
func (fakeRepository) JoinRoom(context.Context, JoinRoomRequest) (JoinResult, error) {
	return JoinResult{}, nil
}
func (fakeRepository) AcquireLease(context.Context, int64, int64, string) (JoinResult, error) {
	return JoinResult{}, nil
}
func (fakeRepository) HeartbeatLease(context.Context, int64, int64) (Lease, error) {
	return Lease{}, nil
}
func (fakeRepository) ReleaseLease(context.Context, int64, int64, string) error { return nil }
func (fakeRepository) CreateReview(context.Context, ReviewRequest) error        { return nil }
func (fakeRepository) ListRoomReviews(context.Context, int64, int64, int, int) ([]Review, int, error) {
	return nil, 0, nil
}
func (fakeRepository) ModerateRoom(context.Context, RoomModerationRequest) error { return nil }
func (fakeRepository) ModerateReview(context.Context, ReviewModerationRequest) error {
	return nil
}
func (fakeRepository) MarkSettlementCharging(context.Context, int64) (SettlementIntent, error) {
	return SettlementIntent{}, nil
}
func (fakeRepository) FinalizeSettlement(context.Context, int64, PaymentSource, string) (SettlementIntent, error) {
	return SettlementIntent{}, nil
}
func (fakeRepository) FailSettlement(context.Context, int64, string) error { return nil }
func (fakeRepository) ListPayoutReceipts(context.Context, int64, int, int) ([]PayoutReceipt, int, error) {
	return nil, 0, nil
}

func TestCreateRoomRejectsInvalidInputBeforeRepository(t *testing.T) {
	service := &Service{repository: fakeRepository{}}
	_, err := service.CreateRoom(context.Background(), CreateRoomRequest{
		OwnerUserID:        1,
		Name:               "room",
		Platform:           "openai",
		Visibility:         VisibilityPrivate,
		SeatLimit:          0,
		LeaseSeconds:       600,
		IdleTimeoutSeconds: 300,
		LeasePrice:         decimal.Zero,
	})
	if got := infraerrors.FromError(err).Code; got != 400 {
		t.Fatalf("status = %d, want 400", got)
	}
}

func TestRoomStatusAfterVisibilityChangeRequiresPublicReviewWithoutRevivingSuspension(t *testing.T) {
	require.Equal(t, RoomPendingReview, roomStatusAfterVisibilityChange(RoomActive, VisibilityPrivate, VisibilityPublic))
	require.Equal(t, RoomActive, roomStatusAfterVisibilityChange(RoomPendingReview, VisibilityPublic, VisibilityPrivate))
	require.Equal(t, RoomSuspended, roomStatusAfterVisibilityChange(RoomSuspended, VisibilityPrivate, VisibilityPublic))
}

func TestAcquireLeaseRequiresIdempotencyKey(t *testing.T) {
	service := &Service{repository: fakeRepository{}}
	_, err := service.AcquireLease(context.Background(), 1, 2, "")
	if got := infraerrors.FromError(err).Code; got != 400 {
		t.Fatalf("status = %d, want 400", got)
	}
}

func TestListRoomReviewsRejectsInvalidPaginationBeforeRepository(t *testing.T) {
	service := &Service{repository: fakeRepository{}}
	_, _, err := service.ListRoomReviews(context.Background(), 1, 1, 0, 0)
	if got := infraerrors.FromError(err).Code; got != 400 {
		t.Fatalf("status = %d, want 400", got)
	}
}

type ownerMembershipRepositoryStub struct {
	fakeRepository
	request MembershipDecisionRequest
}

type ownerRoomAccountRepositoryStub struct {
	fakeRepository
	request RoomAccountLifecycleRequest
}

func (r *ownerRoomAccountRepositoryStub) ListOwnerRoomAccounts(_ context.Context, _, _ int64, _, _ int) ([]RoomAccount, int, error) {
	return []RoomAccount{{RoomID: 8, AccountID: 9, State: RoomAccountActive}}, 1, nil
}

func (r *ownerRoomAccountRepositoryStub) DrainRoomAccount(_ context.Context, request RoomAccountLifecycleRequest) (RoomAccount, error) {
	r.request = request
	return RoomAccount{RoomID: request.RoomID, AccountID: request.AccountID, State: RoomAccountDraining}, nil
}

func (r *ownerRoomAccountRepositoryStub) RemoveRoomAccount(_ context.Context, request RoomAccountLifecycleRequest) error {
	r.request = request
	return nil
}

func (r *ownerMembershipRepositoryStub) ListOwnerRoomMemberships(context.Context, int64, int64, MembershipStatus, int, int) ([]Membership, int, error) {
	return nil, 0, nil
}

func (r *ownerMembershipRepositoryStub) DecideMembershipAndSettle(_ context.Context, request MembershipDecisionRequest, _ *wallet.Service) (JoinResult, error) {
	r.request = request
	return JoinResult{Membership: Membership{ID: request.MembershipID, Status: MembershipActive}}, nil
}

func TestMembershipDecisionRequiresKnownAction(t *testing.T) {
	service := &Service{repository: fakeRepository{}, wallet: &wallet.Service{}}
	_, err := service.DecideMembership(context.Background(), MembershipDecisionRequest{OwnerUserID: 1, RoomID: 2, MembershipID: 3, Decision: "unknown"})
	require.Equal(t, int32(400), infraerrors.FromError(err).Code)
}

func TestMembershipApprovalUsesAtomicOwnerAdmissionRepository(t *testing.T) {
	repository := &ownerMembershipRepositoryStub{}
	service := &Service{repository: repository, wallet: &wallet.Service{}}
	result, err := service.DecideMembership(context.Background(), MembershipDecisionRequest{
		OwnerUserID: 7, RoomID: 8, MembershipID: 9, Decision: MembershipApprove,
	})
	require.NoError(t, err)
	require.Equal(t, int64(9), result.Membership.ID)
	require.Equal(t, MembershipActive, result.Membership.Status)
	require.Equal(t, MembershipApprove, repository.request.Decision)
}

func TestRoomAccountLifecycleUsesOwnerScopedRepository(t *testing.T) {
	repository := &ownerRoomAccountRepositoryStub{}
	service := &Service{repository: repository, wallet: &wallet.Service{}}

	items, total, err := service.ListOwnerRoomAccounts(context.Background(), 7, 8, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, int64(9), items[0].AccountID)

	account, err := service.DrainRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 8, AccountID: 9})
	require.NoError(t, err)
	require.Equal(t, RoomAccountDraining, account.State)
	require.Equal(t, int64(7), repository.request.OwnerUserID)

	require.NoError(t, service.RemoveRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 8, AccountID: 9}))
	require.Equal(t, int64(9), repository.request.AccountID)
}

func TestRoomAccountLifecycleRejectsInvalidRequestBeforeRepository(t *testing.T) {
	service := &Service{repository: &ownerRoomAccountRepositoryStub{}, wallet: &wallet.Service{}}
	_, err := service.DrainRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 8})
	require.Equal(t, int32(400), infraerrors.FromError(err).Code)
	_, _, err = service.ListOwnerRoomAccounts(context.Background(), 7, 8, 0, 0)
	require.Equal(t, int32(400), infraerrors.FromError(err).Code)
}

func TestSettlementRequiresAtomicRepository(t *testing.T) {
	service := &Service{repository: fakeRepository{}, wallet: &wallet.Service{}}
	_, err := service.settle(context.Background(), SettlementIntent{ID: 9})
	if got := infraerrors.FromError(err).Code; got != 500 {
		t.Fatalf("status = %d, want 500", got)
	}
}

func TestSourceFromAllocation(t *testing.T) {
	cases := []struct {
		name       string
		allocation wallet.DebitAllocation
		want       PaymentSource
	}{
		{"bound", wallet.DebitAllocation{Bound: decimal.NewFromInt(1)}, PaymentBound},
		{"normal", wallet.DebitAllocation{Normal: decimal.NewFromInt(1)}, PaymentNormal},
		{"split", wallet.DebitAllocation{Bound: decimal.NewFromInt(1), Normal: decimal.NewFromInt(1)}, PaymentBoundThenNormal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceFromAllocation(tc.allocation); got != tc.want {
				t.Fatalf("source = %q, want %q", got, tc.want)
			}
		})
	}
}
