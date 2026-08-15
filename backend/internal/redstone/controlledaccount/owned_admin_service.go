package controlledaccount

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/service"
)

type ownerScopeContextKey struct{}

func withOwnerScope(ctx context.Context, ownerUserID int64) context.Context {
	return context.WithValue(ctx, ownerScopeContextKey{}, ownerUserID)
}

func ownerScopeFromContext(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, infraerrors.Forbidden("ACCOUNT_OWNER_SCOPE_REQUIRED", "account owner scope is required")
	}
	ownerUserID, ok := ctx.Value(ownerScopeContextKey{}).(int64)
	if !ok || ownerUserID <= 0 {
		return 0, infraerrors.Forbidden("ACCOUNT_OWNER_SCOPE_REQUIRED", "account owner scope is required")
	}
	return ownerUserID, nil
}

type ownedAdminService struct {
	service.AdminService
	base service.AdminService
	db   *sql.DB
}

func newOwnedAdminService(base service.AdminService, db *sql.DB) *ownedAdminService {
	if base == nil || db == nil {
		return nil
	}
	return &ownedAdminService{AdminService: base, base: base, db: db}
}

func (s *ownedAdminService) ListAccounts(
	ctx context.Context,
	page, pageSize int,
	platform, accountType, status, search string,
	groupID int64,
	privacyMode string,
	sortBy, sortOrder string,
) ([]service.Account, int64, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, 0, err
	}
	ids, total, err := s.listOwnedAccountIDs(ctx, ownerUserID, ownedAccountListOptions{
		Page:        page,
		PageSize:    pageSize,
		Platform:    platform,
		AccountType: accountType,
		Status:      status,
		Search:      search,
		GroupID:     groupID,
		PrivacyMode: privacyMode,
		SortBy:      sortBy,
		SortOrder:   sortOrder,
	})
	if err != nil {
		return nil, 0, err
	}
	accounts, err := s.loadOwnedAccountsByIDs(ctx, ids)
	return accounts, total, err
}

func (s *ownedAdminService) ListAccountsForSchedulerScoreFilter(
	ctx context.Context,
	platform, accountType, status, search string,
	groupID int64,
	privacyMode string,
) ([]service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ids, _, err := s.listOwnedAccountIDs(ctx, ownerUserID, ownedAccountListOptions{
		Platform:    platform,
		AccountType: accountType,
		Status:      status,
		Search:      search,
		GroupID:     groupID,
		PrivacyMode: privacyMode,
		SortBy:      "created_at",
		SortOrder:   "desc",
		NoLimit:     true,
	})
	if err != nil {
		return nil, err
	}
	return s.loadOwnedAccountsByIDs(ctx, ids)
}

func (s *ownedAdminService) ListOpenAISchedulableAccountsForSchedulerScore(ctx context.Context, groupID *int64) ([]service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	effectiveGroupID := int64(0)
	if groupID != nil {
		effectiveGroupID = *groupID
	}
	ids, _, err := s.listOwnedAccountIDs(ctx, ownerUserID, ownedAccountListOptions{
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		GroupID:              effectiveGroupID,
		SortBy:               "created_at",
		SortOrder:            "desc",
		NoLimit:              true,
		RequireExplicitGroup: groupID != nil,
		RequireUngrouped:     groupID == nil,
		SchedulableOnly:      true,
	})
	if err != nil {
		return nil, err
	}
	return s.loadOwnedAccountsByIDs(ctx, ids)
}

func (s *ownedAdminService) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	return s.base.GetAccount(ctx, id)
}

func (s *ownedAdminService) GetAccountsByIDs(ctx context.Context, ids []int64) ([]*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizePositiveIDs(ids)
	if err != nil {
		return nil, err
	}
	if err := s.ensureAllOwnedAccountIDs(ctx, ownerUserID, normalized); err != nil {
		return nil, err
	}
	return s.base.GetAccountsByIDs(ctx, normalized)
}

func (s *ownedAdminService) CreateAccount(ctx context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cloned := &service.CreateAccountInput{}
	if input != nil {
		*cloned = *input
	}
	if cloned.OwnerUserID != nil && *cloned.OwnerUserID != ownerUserID {
		return nil, infraerrors.Forbidden("ACCOUNT_OWNER_SCOPE_VIOLATION", "cannot create an account for another user")
	}
	cloned.OwnerUserID = &ownerUserID
	return s.base.CreateAccount(ctx, cloned)
}

func (s *ownedAdminService) DuplicateAccount(ctx context.Context, id int64, _ string, operationKey string) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	return s.base.DuplicateAccount(ctx, id, ownedActorScope(ownerUserID), operationKey)
}

func (s *ownedAdminService) RecoverDuplicateAccount(ctx context.Context, id int64, _ string, operationKey string) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	recovered, err := s.base.RecoverDuplicateAccount(ctx, id, ownedActorScope(ownerUserID), operationKey)
	if err != nil || recovered == nil {
		return recovered, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, recovered.ID); err != nil {
		return nil, err
	}
	return recovered, nil
}

func (s *ownedAdminService) UpdateAccount(ctx context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	return s.base.UpdateAccount(ctx, id, input)
}

func (s *ownedAdminService) UpdateAccountExtra(ctx context.Context, id int64, updates map[string]any) error {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return err
	}
	return s.base.UpdateAccountExtra(ctx, id, updates)
}

func (s *ownedAdminService) DeleteAccount(ctx context.Context, id int64) error {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return err
	}
	return s.base.DeleteAccount(ctx, id)
}

func (s *ownedAdminService) RefreshAccountCredentials(ctx context.Context, id int64) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	return s.base.RefreshAccountCredentials(ctx, id)
}

func (s *ownedAdminService) ClearAccountError(ctx context.Context, id int64) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	return s.base.ClearAccountError(ctx, id)
}

func (s *ownedAdminService) SetAccountError(ctx context.Context, id int64, errorMsg string) error {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return err
	}
	return s.base.SetAccountError(ctx, id, errorMsg)
}

func (s *ownedAdminService) EnsureOpenAIPrivacy(ctx context.Context, account *service.Account) string {
	if !s.ownsAccountValue(ctx, account) {
		return ""
	}
	return s.base.EnsureOpenAIPrivacy(ctx, account)
}

func (s *ownedAdminService) EnsureAntigravityPrivacy(ctx context.Context, account *service.Account) string {
	if !s.ownsAccountValue(ctx, account) {
		return ""
	}
	return s.base.EnsureAntigravityPrivacy(ctx, account)
}

func (s *ownedAdminService) ForceOpenAIPrivacy(ctx context.Context, account *service.Account) string {
	if !s.ownsAccountValue(ctx, account) {
		return ""
	}
	return s.base.ForceOpenAIPrivacy(ctx, account)
}

func (s *ownedAdminService) ForceAntigravityPrivacy(ctx context.Context, account *service.Account) string {
	if !s.ownsAccountValue(ctx, account) {
		return ""
	}
	return s.base.ForceAntigravityPrivacy(ctx, account)
}

func (s *ownedAdminService) SetAccountSchedulable(ctx context.Context, id int64, schedulable bool) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return nil, err
	}
	return s.base.SetAccountSchedulable(ctx, id, schedulable)
}

func (s *ownedAdminService) BulkUpdateAccounts(ctx context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, infraerrors.BadRequest("ACCOUNT_BULK_UPDATE_INVALID", "bulk update payload is required")
	}

	cloned := *input
	if len(input.AccountIDs) > 0 {
		normalized, err := normalizePositiveIDs(input.AccountIDs)
		if err != nil {
			return nil, err
		}
		if err := s.ensureAllOwnedAccountIDs(ctx, ownerUserID, normalized); err != nil {
			return nil, err
		}
		cloned.AccountIDs = normalized
	}

	if input.Filters != nil {
		groupID, err := parseGroupFilter(input.Filters.Group)
		if err != nil {
			return nil, err
		}
		ownedIDs, _, err := s.listOwnedAccountIDs(ctx, ownerUserID, ownedAccountListOptions{
			Platform:    input.Filters.Platform,
			AccountType: input.Filters.Type,
			Status:      input.Filters.Status,
			Search:      input.Filters.Search,
			GroupID:     groupID,
			PrivacyMode: input.Filters.PrivacyMode,
			SortBy:      "created_at",
			SortOrder:   "desc",
			NoLimit:     true,
		})
		if err != nil {
			return nil, err
		}
		cloned.AccountIDs = ownedIDs
		cloned.Filters = nil
	}

	return s.base.BulkUpdateAccounts(ctx, &cloned)
}

func (s *ownedAdminService) CheckMixedChannelRisk(ctx context.Context, currentAccountID int64, currentAccountPlatform string, groupIDs []int64) error {
	if currentAccountID > 0 {
		ownerUserID, err := ownerScopeFromContext(ctx)
		if err != nil {
			return err
		}
		if err := s.ensureOwnedAccount(ctx, ownerUserID, currentAccountID); err != nil {
			return err
		}
	}
	return s.base.CheckMixedChannelRisk(ctx, currentAccountID, currentAccountPlatform, groupIDs)
}

func (s *ownedAdminService) RevertAccountProxyFallback(ctx context.Context, id int64) error {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return err
	}
	return s.base.RevertAccountProxyFallback(ctx, id)
}

func (s *ownedAdminService) CreateShadow(ctx context.Context, parentID int64, opts service.ShadowOptions) (*service.Account, error) {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, parentID); err != nil {
		return nil, err
	}
	return s.base.CreateShadow(ctx, parentID, opts)
}

func (s *ownedAdminService) ResetAccountQuota(ctx context.Context, id int64) error {
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.ensureOwnedAccount(ctx, ownerUserID, id); err != nil {
		return err
	}
	return s.base.ResetAccountQuota(ctx, id)
}

type ownedAccountListOptions struct {
	Page                 int
	PageSize             int
	Platform             string
	AccountType          string
	Status               string
	Search               string
	GroupID              int64
	PrivacyMode          string
	SortBy               string
	SortOrder            string
	NoLimit              bool
	SchedulableOnly      bool
	RequireExplicitGroup bool
	RequireUngrouped     bool
}

func (s *ownedAdminService) listOwnedAccountIDs(ctx context.Context, ownerUserID int64, options ownedAccountListOptions) ([]int64, int64, error) {
	whereParts := []string{"a.deleted_at IS NULL", "a.owner_user_id = $1"}
	args := []any{ownerUserID}
	nextArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if value := strings.TrimSpace(options.Platform); value != "" {
		whereParts = append(whereParts, "a.platform = "+nextArg(value))
	}
	if value := strings.TrimSpace(options.AccountType); value != "" {
		whereParts = append(whereParts, "a.type = "+nextArg(value))
	}
	if value := strings.TrimSpace(options.Status); value != "" {
		whereParts = append(whereParts, "a.status = "+nextArg(value))
	}
	if value := strings.TrimSpace(options.Search); value != "" {
		placeholder := nextArg("%" + value + "%")
		whereParts = append(whereParts, "(a.name ILIKE "+placeholder+" OR COALESCE(a.notes, '') ILIKE "+placeholder+")")
	}
	if options.RequireUngrouped || options.GroupID == service.AccountListGroupUngrouped {
		whereParts = append(whereParts, "NOT EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id)")
	} else if options.RequireExplicitGroup || options.GroupID > 0 {
		whereParts = append(whereParts, "EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id AND ag.group_id = "+nextArg(options.GroupID)+")")
	}
	if value := strings.TrimSpace(options.PrivacyMode); value != "" {
		if value == service.AccountPrivacyModeUnsetFilter {
			whereParts = append(whereParts, "COALESCE(NULLIF(a.extra ->> 'privacy_mode', ''), '__unset__') = '__unset__'")
		} else {
			whereParts = append(whereParts, "a.extra ->> 'privacy_mode' = "+nextArg(value))
		}
	}
	if options.SchedulableOnly {
		whereParts = append(whereParts, "a.schedulable = TRUE")
	}

	whereSQL := strings.Join(whereParts, " AND ")
	countQuery := "SELECT COUNT(*) FROM accounts a WHERE " + whereSQL
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderColumn := ownedAccountSortColumn(options.SortBy)
	orderDirection := "ASC"
	if strings.EqualFold(strings.TrimSpace(options.SortOrder), "desc") {
		orderDirection = "DESC"
	}

	query := "SELECT a.id FROM accounts a WHERE " + whereSQL +
		" ORDER BY " + orderColumn + " " + orderDirection + ", a.id " + orderDirection

	queryArgs := append([]any(nil), args...)
	if !options.NoLimit {
		page := options.Page
		if page <= 0 {
			page = 1
		}
		pageSize := options.PageSize
		if pageSize <= 0 {
			pageSize = 20
		}
		offset := (page - 1) * pageSize
		query += " LIMIT " + fmt.Sprintf("$%d", len(queryArgs)+1) + " OFFSET " + fmt.Sprintf("$%d", len(queryArgs)+2)
		queryArgs = append(queryArgs, pageSize, offset)
	}

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, total, rows.Err()
}

func (s *ownedAdminService) loadOwnedAccountsByIDs(ctx context.Context, ids []int64) ([]service.Account, error) {
	if len(ids) == 0 {
		return []service.Account{}, nil
	}
	loaded, err := s.base.GetAccountsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	accountByID := make(map[int64]*service.Account, len(loaded))
	for _, account := range loaded {
		if account != nil {
			accountByID[account.ID] = account
		}
	}
	accounts := make([]service.Account, 0, len(ids))
	for _, id := range ids {
		if account := accountByID[id]; account != nil {
			accounts = append(accounts, *account)
		}
	}
	return accounts, nil
}

func (s *ownedAdminService) ensureOwnedAccount(ctx context.Context, ownerUserID, accountID int64) error {
	if accountID <= 0 {
		return infraerrors.BadRequest("ACCOUNT_INVALID_ID", "invalid account id")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL)`,
		accountID,
		ownerUserID,
	).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return infraerrors.Forbidden("ACCOUNT_OWNER_SCOPE_VIOLATION", "cannot access another user's account")
	}
	return nil
}

func (s *ownedAdminService) ensureAllOwnedAccountIDs(ctx context.Context, ownerUserID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ownedIDs, err := s.filterOwnedAccountIDs(ctx, ownerUserID, ids)
	if err != nil {
		return err
	}
	if len(ownedIDs) != len(ids) {
		return infraerrors.Forbidden("ACCOUNT_OWNER_SCOPE_VIOLATION", "bulk request contains accounts outside owner scope")
	}
	for i := range ids {
		if ownedIDs[i] != ids[i] {
			return infraerrors.Forbidden("ACCOUNT_OWNER_SCOPE_VIOLATION", "bulk request contains accounts outside owner scope")
		}
	}
	return nil
}

func (s *ownedAdminService) filterOwnedAccountIDs(ctx context.Context, ownerUserID int64, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, ownerUserID)
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	query := `SELECT id FROM accounts WHERE owner_user_id = $1 AND deleted_at IS NULL AND id IN (` + strings.Join(placeholders, ", ") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ownedSet := make(map[int64]struct{}, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ownedSet[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ownedIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := ownedSet[id]; ok {
			ownedIDs = append(ownedIDs, id)
		}
	}
	return ownedIDs, nil
}

func (s *ownedAdminService) ownsAccountValue(ctx context.Context, account *service.Account) bool {
	if account == nil || account.ID <= 0 {
		return false
	}
	ownerUserID, err := ownerScopeFromContext(ctx)
	if err != nil {
		return false
	}
	if account.OwnerUserID != nil {
		return *account.OwnerUserID == ownerUserID
	}
	return s.ensureOwnedAccount(ctx, ownerUserID, account.ID) == nil
}

func ownedAccountSortColumn(raw string) string {
	switch strings.TrimSpace(raw) {
	case "created_at":
		return "a.created_at"
	case "updated_at":
		return "a.updated_at"
	case "platform":
		return "a.platform"
	case "type":
		return "a.type"
	case "status":
		return "a.status"
	case "priority":
		return "a.priority"
	default:
		return "a.name"
	}
}

func ownedActorScope(ownerUserID int64) string {
	return "user:" + strconv.FormatInt(ownerUserID, 10)
}

func parseGroupFilter(raw string) (int64, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return 0, nil
	case "ungrouped":
		return service.AccountListGroupUngrouped, nil
	default:
		groupID, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid group filter: %w", err)
		}
		return groupID, nil
	}
}

func normalizePositiveIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_INVALID_ID", "account id must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized, nil
}
