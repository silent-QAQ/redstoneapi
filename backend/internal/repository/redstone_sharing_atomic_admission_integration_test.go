//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/sharing"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestRedstoneSharingAtomicAdmissionRollsBackWhenWalletChargeFails(t *testing.T) {
	ctx, fixture := newRedstoneSharingAtomicAdmissionFixture(t, 0)

	_, err := fixture.sharingService.JoinRoom(ctx, sharing.JoinRoomRequest{
		UserID:         fixture.renter.ID,
		RoomID:         fixture.room.ID,
		IdempotencyKey: fixture.key("insufficient-funds"),
	})
	require.ErrorIs(t, err, wallet.ErrInsufficientFunds)

	var membershipCount, leaseCount, settlementCount, grantCount, allowedGroupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_memberships
		WHERE room_id = $1 AND user_id = $2 AND status IN ('queued', 'active', 'ending')`,
		fixture.room.ID, fixture.renter.ID,
	).Scan(&membershipCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_leases
		WHERE room_id = $1 AND user_id = $2 AND state = 'active'`,
		fixture.room.ID, fixture.renter.ID,
	).Scan(&leaseCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_settlement_intents
		WHERE room_id = $1 AND payer_user_id = $2`,
		fixture.room.ID, fixture.renter.ID,
	).Scan(&settlementCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_group_grants
		WHERE user_id = $1 AND group_id = $2 AND status = 'active'`,
		fixture.renter.ID, fixture.privateGroup.GroupID,
	).Scan(&grantCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2`,
		fixture.renter.ID, fixture.privateGroup.GroupID,
	).Scan(&allowedGroupCount))

	require.Zero(t, membershipCount, "a failed paid admission must not leave a live membership")
	require.Zero(t, leaseCount, "a failed paid admission must not leave an active lease")
	require.Zero(t, settlementCount, "a failed paid admission must not leave a settlement intent")
	require.Zero(t, grantCount, "a failed paid admission must not grant private-group access")
	require.Zero(t, allowedGroupCount, "a failed paid admission must not grant an allowed group")

	renterWallet, err := fixture.walletService.GetSnapshot(ctx, fixture.renter.ID)
	require.NoError(t, err)
	require.True(t, renterWallet.Balances.Normal.IsZero())
	require.True(t, renterWallet.Balances.Bound.IsZero())
}

func TestRedstoneSharingAtomicAdmissionSettlesWalletAndGrantTogether(t *testing.T) {
	ctx, fixture := newRedstoneSharingAtomicAdmissionFixture(t, 10)

	result, err := fixture.sharingService.JoinRoom(ctx, sharing.JoinRoomRequest{
		UserID:         fixture.renter.ID,
		RoomID:         fixture.room.ID,
		IdempotencyKey: fixture.key("settled-admission"),
	})
	require.NoError(t, err)
	require.Equal(t, sharing.MembershipActive, result.Membership.Status)
	require.NotNil(t, result.Lease)
	require.Equal(t, sharing.LeaseActive, result.Lease.State)
	require.NotNil(t, result.Settlement)
	require.Equal(t, sharing.SettlementSettled, result.Settlement.Status)
	require.Equal(t, sharing.PaymentNormal, result.Settlement.PaymentSource)

	var status, paymentSource string
	var grossAmount, platformFee, ownerAmount decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT status, payment_source, gross_amount, platform_fee_amount, owner_amount
		FROM redstone_account_share_settlement_intents WHERE id = $1`, result.Settlement.ID,
	).Scan(&status, &paymentSource, &grossAmount, &platformFee, &ownerAmount))
	require.Equal(t, string(sharing.SettlementSettled), status)
	require.Equal(t, string(sharing.PaymentNormal), paymentSource)
	require.True(t, grossAmount.Equal(fixture.room.LeasePrice))
	require.True(t, platformFee.Equal(fixture.room.LeasePrice.Mul(fixture.room.PlatformFeeRate).Round(wallet.MonetaryScale)))
	require.True(t, ownerAmount.Equal(fixture.room.LeasePrice.Sub(platformFee)))

	renterWallet, err := fixture.walletService.GetSnapshot(ctx, fixture.renter.ID)
	require.NoError(t, err)
	require.True(t, renterWallet.Balances.Bound.IsZero())
	require.True(t, renterWallet.Balances.Normal.Equal(decimal.NewFromInt(10).Sub(grossAmount)))

	ownerWallet, err := fixture.walletService.GetSnapshot(ctx, fixture.owner.ID)
	require.NoError(t, err)
	require.True(t, ownerWallet.Balances.Bound.IsZero())
	require.True(t, ownerWallet.Balances.Normal.Equal(ownerAmount))

	var renterLedgerDelta, ownerLedgerDelta decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(delta), 0) FROM redstone_wallet_ledger
		WHERE user_id = $1 AND operation = 'token_charge'
		  AND reference_type = 'account_share_intent' AND reference_id = $2`,
		fixture.renter.ID, fmt.Sprint(result.Settlement.ID),
	).Scan(&renterLedgerDelta))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(delta), 0) FROM redstone_wallet_ledger
		WHERE user_id = $1 AND operation = 'settlement'
		  AND reference_type = 'account_share_intent' AND reference_id = $2`,
		fixture.owner.ID, fmt.Sprint(result.Settlement.ID),
	).Scan(&ownerLedgerDelta))
	require.True(t, renterLedgerDelta.Equal(grossAmount.Neg()))
	require.True(t, ownerLedgerDelta.Equal(ownerAmount))

	var grantCount, allowedGroupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_group_grants
		WHERE membership_id = $1 AND group_id = $2 AND user_id = $3 AND status = 'active'`,
		result.Membership.ID, fixture.privateGroup.GroupID, fixture.renter.ID,
	).Scan(&grantCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2`,
		fixture.renter.ID, fixture.privateGroup.GroupID,
	).Scan(&allowedGroupCount))
	require.Equal(t, 1, grantCount)
	require.Equal(t, 1, allowedGroupCount)
}

type redstoneSharingAtomicAdmissionFixture struct {
	owner          *service.User
	renter         *service.User
	room           sharing.Room
	privateGroup   sharing.PrivateGroup
	walletService  *wallet.Service
	sharingService *sharing.Service
	suffix         string
}

func (f redstoneSharingAtomicAdmissionFixture) key(operation string) string {
	return "sharing-atomic-" + operation + "-" + f.suffix
}

func newRedstoneSharingAtomicAdmissionFixture(t *testing.T, renterBalance float64) (context.Context, redstoneSharingAtomicAdmissionFixture) {
	t.Helper()

	ctx := context.Background()
	client := testEntClient(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	owner := mustCreateUser(t, client, &service.User{
		Email:   "sharing-owner-" + suffix + "@example.com",
		Balance: 0,
	})
	renter := mustCreateUser(t, client, &service.User{
		Email:   "sharing-renter-" + suffix + "@example.com",
		Balance: renterBalance,
	})

	sharingRepository, err := sharing.NewPostgresRepository(integrationDB)
	require.NoError(t, err)
	walletRepository, err := wallet.NewPostgresRepository(integrationDB)
	require.NoError(t, err)
	walletService, err := wallet.NewService(walletRepository)
	require.NoError(t, err)
	sharingService, err := sharing.NewService(sharingRepository, walletService)
	require.NoError(t, err)

	privateGroup, created, err := sharingService.CreatePrivateGroup(ctx, sharing.CreatePrivateGroupRequest{
		OwnerUserID:    owner.ID,
		Name:           "atomic-group-" + suffix,
		Description:    "atomic admission integration test",
		Platform:       "openai",
		IdempotencyKey: "atomic-group-" + suffix,
	})
	require.NoError(t, err)
	require.True(t, created)

	account := mustCreateAccount(t, client, &service.Account{
		Name:        "atomic-account-" + suffix,
		Platform:    "openai",
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"},
	})
	require.NoError(t, client.Account.UpdateOneID(account.ID).SetOwnerUserID(owner.ID).Exec(ctx))

	room, err := sharingService.CreateRoom(ctx, sharing.CreateRoomRequest{
		OwnerUserID:        owner.ID,
		Name:               "atomic-room-" + suffix,
		Description:        "atomic admission integration test",
		Platform:           "openai",
		Visibility:         sharing.VisibilityPrivate,
		SeatLimit:          1,
		LeaseSeconds:       600,
		IdleTimeoutSeconds: 300,
		LeasePrice:         decimal.NewFromInt(5),
	})
	require.NoError(t, err)
	require.NoError(t, sharingService.BindAccount(ctx, sharing.BindAccountRequest{
		OwnerUserID:    owner.ID,
		RoomID:         room.ID,
		AccountID:      account.ID,
		PrivateGroupID: privateGroup.GroupID,
	}))
	require.NoError(t, sharingService.CreateInvite(ctx, sharing.InviteRequest{
		OwnerUserID:   owner.ID,
		RoomID:        room.ID,
		InvitedUserID: renter.ID,
	}))

	return ctx, redstoneSharingAtomicAdmissionFixture{
		owner:          owner,
		renter:         renter,
		room:           room,
		privateGroup:   privateGroup,
		walletService:  walletService,
		sharingService: sharingService,
		suffix:         suffix,
	}
}
