package controlledaccount

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/stretchr/testify/require"
)

type ownedAdminBaseStub struct {
	service.AdminService
	createInput     *service.CreateAccountInput
	createErr       error
	getAccountsArgs []int64
	getAccountsErr  error
	duplicateID     int64
	duplicateScope  string
	duplicateOpKey  string
	duplicateResult *service.Account
	duplicateErr    error
}

func (s *ownedAdminBaseStub) CreateAccount(_ context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	if input != nil {
		cloned := *input
		if input.OwnerUserID != nil {
			ownerUserID := *input.OwnerUserID
			cloned.OwnerUserID = &ownerUserID
		}
		if input.GroupIDs != nil {
			cloned.GroupIDs = append([]int64(nil), input.GroupIDs...)
		}
		s.createInput = &cloned
	}
	return &service.Account{ID: 91, OwnerUserID: s.createInput.OwnerUserID}, s.createErr
}

func (s *ownedAdminBaseStub) GetAccountsByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	s.getAccountsArgs = append([]int64(nil), ids...)
	return []*service.Account{{ID: 101}, {ID: 202}}, s.getAccountsErr
}

func (s *ownedAdminBaseStub) DuplicateAccount(_ context.Context, id int64, actorScope, operationKey string) (*service.Account, error) {
	s.duplicateID = id
	s.duplicateScope = actorScope
	s.duplicateOpKey = operationKey
	if s.duplicateResult == nil {
		s.duplicateResult = &service.Account{ID: id + 1}
	}
	return s.duplicateResult, s.duplicateErr
}

func TestOwnedAdminServiceCreateAccountInjectsOwnerScope(t *testing.T) {
	base := &ownedAdminBaseStub{}
	svc := newOwnedAdminService(base, &sql.DB{})
	ctx := withOwnerScope(context.Background(), 42)
	input := &service.CreateAccountInput{
		Name:     "owned",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
	}

	account, err := svc.CreateAccount(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, account)
	require.NotNil(t, base.createInput)
	require.NotNil(t, base.createInput.OwnerUserID)
	require.Equal(t, int64(42), *base.createInput.OwnerUserID)
	require.Nil(t, input.OwnerUserID, "caller input must not be mutated")
}

func TestOwnedAdminServiceCreateAccountRejectsCrossOwnerRequest(t *testing.T) {
	base := &ownedAdminBaseStub{}
	svc := newOwnedAdminService(base, &sql.DB{})
	ctx := withOwnerScope(context.Background(), 42)
	requestedOwner := int64(7)

	account, err := svc.CreateAccount(ctx, &service.CreateAccountInput{
		Name:        "owned",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		OwnerUserID: &requestedOwner,
	})

	require.Nil(t, account)
	require.Error(t, err)
	require.Equal(t, "ACCOUNT_OWNER_SCOPE_VIOLATION", infraerrors.Reason(err))
	require.Nil(t, base.createInput)
}

func TestOwnedAdminServiceGetAccountsByIDsRejectsMixedOwnershipBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM accounts WHERE owner_user_id = $1 AND deleted_at IS NULL AND id IN ($2, $3)")).
		WithArgs(int64(42), int64(101), int64(202)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))

	base := &ownedAdminBaseStub{}
	svc := newOwnedAdminService(base, db)
	ctx := withOwnerScope(context.Background(), 42)

	accounts, err := svc.GetAccountsByIDs(ctx, []int64{101, 202})

	require.Nil(t, accounts)
	require.Error(t, err)
	require.Equal(t, "ACCOUNT_OWNER_SCOPE_VIOLATION", infraerrors.Reason(err))
	require.Nil(t, base.getAccountsArgs, "base service must not receive a partially filtered batch")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOwnedAdminServiceDuplicateAccountUsesOwnerActorScope(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL)")).
		WithArgs(int64(15), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	base := &ownedAdminBaseStub{}
	svc := newOwnedAdminService(base, db)
	ctx := withOwnerScope(context.Background(), 42)

	account, err := svc.DuplicateAccount(ctx, 15, "admin:ignored", "dup-op-1")

	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(15), base.duplicateID)
	require.Equal(t, "user:42", base.duplicateScope)
	require.Equal(t, "dup-op-1", base.duplicateOpKey)
	require.NoError(t, mock.ExpectationsWereMet())
}
