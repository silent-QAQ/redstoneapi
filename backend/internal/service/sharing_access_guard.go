package service

import (
	"context"
	"fmt"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/ctxkey"
)

// SharingAccessGuard keeps Redstone's time-bound sharing authorization out of
// the scheduler snapshots. It is deliberately account-level: a group can hold
// multiple accounts, but a lease grants only one of them.
type SharingAccessGuard interface {
	AllowedAccountIDs(context.Context, int64, *int64, []int64) (map[int64]struct{}, error)
}

func sharingRequestUserID(ctx context.Context) int64 {
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	return userID
}

func sharingRequestGroupID(ctx context.Context) *int64 {
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil || group.ID <= 0 {
		return nil
	}
	groupID := group.ID
	return &groupID
}

func sharingRequestRoomID(ctx context.Context) *int64 {
	roomID, _ := ctx.Value(ctxkey.SharingRoomID).(int64)
	if roomID <= 0 {
		return nil
	}
	return &roomID
}

func filterAccountsForSharingAccess(ctx context.Context, guard SharingAccessGuard, accounts []Account) ([]Account, error) {
	if guard == nil || len(accounts) == 0 {
		return accounts, nil
	}
	userID := sharingRequestUserID(ctx)
	// Internal jobs and legacy direct service calls have no authenticated API-key
	// subject. Gateway HTTP requests always receive this value in middleware.
	if userID <= 0 {
		return accounts, nil
	}
	accountIDs := make([]int64, 0, len(accounts))
	for i := range accounts {
		accountIDs = append(accountIDs, accounts[i].ID)
	}
	allowed, err := guard.AllowedAccountIDs(ctx, userID, sharingRequestGroupID(ctx), accountIDs)
	if err != nil {
		return nil, fmt.Errorf("validate shared account lease: %w", err)
	}
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if _, ok := allowed[accounts[i].ID]; ok {
			filtered = append(filtered, accounts[i])
		}
	}
	return filtered, nil
}

func ensureAccountSharingAccess(ctx context.Context, guard SharingAccessGuard, account *Account) error {
	if guard == nil || account == nil || sharingRequestUserID(ctx) <= 0 {
		return nil
	}
	allowed, err := guard.AllowedAccountIDs(ctx, sharingRequestUserID(ctx), sharingRequestGroupID(ctx), []int64{account.ID})
	if err != nil {
		return fmt.Errorf("validate selected shared account lease: %w", err)
	}
	if _, ok := allowed[account.ID]; !ok {
		return ErrNoAvailableAccounts
	}
	return nil
}
