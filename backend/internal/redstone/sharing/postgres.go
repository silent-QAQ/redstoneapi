package sharing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
)

// PostgresRepository owns the sharing state machine. Account credentials and
// scheduling stay in sub2's accounts domain; this repository reads accounts
// only to verify the owner before a room can bind one.
type PostgresRepository struct{ db *sql.DB }

func NewPostgresRepository(db *sql.DB) (*PostgresRepository, error) {
	if db == nil {
		return nil, ErrRepositoryRequired
	}
	return &PostgresRepository{db: db}, nil
}

func (r *PostgresRepository) ListPublicRooms(ctx context.Context, limit, offset int) ([]Room, int, error) {
	return r.listRooms(ctx, `r.visibility = 'public' AND r.status = 'active' AND r.deleted_at IS NULL`, nil, limit, offset)
}

func (r *PostgresRepository) ListOwnerRooms(ctx context.Context, ownerUserID int64, limit, offset int) ([]Room, int, error) {
	return r.listRooms(ctx, `r.owner_user_id = $1 AND r.deleted_at IS NULL`, []any{ownerUserID}, limit, offset)
}

func (r *PostgresRepository) listRooms(ctx context.Context, where string, args []any, limit, offset int) ([]Room, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_account_share_rooms r WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	query := `
		SELECT r.id, r.owner_user_id, r.name, r.description, r.platform, r.visibility, r.status,
		       r.requires_approval, r.seat_limit, r.lease_seconds, r.idle_timeout_seconds,
		       r.lease_price, r.platform_fee_rate, r.created_at, r.updated_at,
		       COUNT(DISTINCT ra.account_id) FILTER (WHERE ra.state = 'active'),
		       COUNT(DISTINCT m.id) FILTER (WHERE m.status IN ('active', 'ending')),
		       COALESCE(AVG(rv.rating) FILTER (WHERE rv.moderation_status = 'visible'), 0),
		       COUNT(DISTINCT rv.id) FILTER (WHERE rv.moderation_status = 'visible')
		FROM redstone_account_share_rooms r
		LEFT JOIN redstone_account_share_room_accounts ra ON ra.room_id = r.id
		LEFT JOIN redstone_account_share_memberships m ON m.room_id = r.id
		LEFT JOIN redstone_account_share_reviews rv ON rv.room_id = r.id
		WHERE ` + where + `
		GROUP BY r.id
		ORDER BY r.updated_at DESC, r.id DESC
		LIMIT $` + fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	rooms := make([]Room, 0)
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, 0, err
		}
		rooms = append(rooms, room)
	}
	return rooms, total, rows.Err()
}

func (r *PostgresRepository) ListUserMemberships(ctx context.Context, userID int64, limit, offset int) ([]Membership, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_account_share_memberships WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason
		FROM redstone_account_share_memberships
		WHERE user_id = $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Membership, 0)
	for rows.Next() {
		item, err := scanMembership(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *PostgresRepository) ListOwnerRoomMemberships(ctx context.Context, ownerUserID, roomID int64, status MembershipStatus, limit, offset int) ([]Membership, int, error) {
	if status != MembershipQueued || !validPage(limit, offset) {
		return nil, 0, ErrInvalidMembership
	}
	var total int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM redstone_account_share_memberships m
		JOIN redstone_account_share_rooms r ON r.id = m.room_id
		WHERE m.room_id = $1 AND r.owner_user_id = $2 AND r.deleted_at IS NULL AND m.status = $3`, roomID, ownerUserID, status).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.id, m.room_id, m.user_id, m.status, m.queued_at, m.joined_at, m.ended_at, m.end_reason
		FROM redstone_account_share_memberships m
		JOIN redstone_account_share_rooms r ON r.id = m.room_id
		WHERE m.room_id = $1 AND r.owner_user_id = $2 AND r.deleted_at IS NULL AND m.status = $3
		ORDER BY m.queued_at, m.id
		LIMIT $4 OFFSET $5`, roomID, ownerUserID, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Membership, 0)
	for rows.Next() {
		item, err := scanMembership(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *PostgresRepository) CreateRoom(ctx context.Context, request CreateRoomRequest) (Room, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Room{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Serializing room creation on the owner row makes quota checks safe under
	// concurrent create requests; a count by itself cannot enforce a limit.
	var lockedOwnerID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, request.OwnerUserID).Scan(&lockedOwnerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Room{}, ErrRoomForbidden
		}
		return Room{}, err
	}
	var maxLive, maxAccounts, maxDaily int
	err = tx.QueryRowContext(ctx, `
		SELECT max_live_rooms, max_accounts_per_room, max_rooms_created_per_day
		FROM redstone_account_share_quota_policies
		WHERE active AND (scope = 'global' OR (scope = 'owner' AND owner_user_id = $1))
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY CASE WHEN scope = 'owner' THEN 0 ELSE 1 END, created_at DESC
		LIMIT 1 FOR UPDATE`, request.OwnerUserID).Scan(&maxLive, &maxAccounts, &maxDaily)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrQuotaExceeded
	}
	if err != nil {
		return Room{}, err
	}
	var live, today int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE status IN ('draft', 'pending_review', 'active', 'suspended')),
		       COUNT(*) FILTER (WHERE created_at >= date_trunc('day', NOW()))
		FROM redstone_account_share_rooms
		WHERE owner_user_id = $1 AND deleted_at IS NULL`, request.OwnerUserID).Scan(&live, &today); err != nil {
		return Room{}, err
	}
	if live >= maxLive || today >= maxDaily || request.SeatLimit > maxAccounts {
		return Room{}, ErrQuotaExceeded
	}
	var publicAllowed bool
	var maxLeaseSeconds int
	var platformFee decimal.Decimal
	if err := tx.QueryRowContext(ctx, `
		SELECT public_room_allowed, max_lease_seconds, default_platform_fee_rate
		FROM redstone_account_share_policies WHERE status = 'active' FOR UPDATE`).Scan(&publicAllowed, &maxLeaseSeconds, &platformFee); err != nil {
		return Room{}, err
	}
	if request.LeaseSeconds > maxLeaseSeconds || (request.Visibility == VisibilityPublic && !publicAllowed) {
		return Room{}, ErrRoomUnavailable
	}

	status := RoomActive
	if request.Visibility == VisibilityPublic {
		status = RoomPendingReview
	}
	var room Room
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_account_share_rooms
		(owner_user_id, name, description, platform, visibility, status, requires_approval,
		 seat_limit, lease_seconds, idle_timeout_seconds, lease_price, platform_fee_rate)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, owner_user_id, name, description, platform, visibility, status,
		          requires_approval, seat_limit, lease_seconds, idle_timeout_seconds,
		          lease_price, platform_fee_rate, created_at, updated_at`,
		request.OwnerUserID, strings.TrimSpace(request.Name), strings.TrimSpace(request.Description), strings.TrimSpace(request.Platform),
		request.Visibility, status, request.RequiresApproval, request.SeatLimit, request.LeaseSeconds, request.IdleTimeoutSeconds, request.LeasePrice, platformFee,
	).Scan(&room.ID, &room.OwnerUserID, &room.Name, &room.Description, &room.Platform, &room.Visibility, &room.Status,
		&room.RequiresApproval, &room.SeatLimit, &room.LeaseSeconds, &room.IdleTimeoutSeconds, &room.LeasePrice, &room.PlatformFeeRate,
		&room.CreatedAt, &room.UpdatedAt)
	if err != nil {
		return Room{}, err
	}
	if err := appendAudit(ctx, tx, room.ID, request.OwnerUserID, "room_created", ""); err != nil {
		return Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return Room{}, err
	}
	return room, nil
}

func (r *PostgresRepository) UpdateRoom(ctx context.Context, request UpdateRoomRequest) (Room, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Room{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var currentStatus RoomStatus
	var currentVisibility RoomVisibility
	if err := tx.QueryRowContext(ctx, `
		SELECT status, visibility
		FROM redstone_account_share_rooms
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
		FOR UPDATE`, request.RoomID, request.OwnerUserID).Scan(&currentStatus, &currentVisibility); errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrRoomForbidden
	} else if err != nil {
		return Room{}, err
	}
	if currentStatus == RoomClosed {
		return Room{}, ErrRoomUnavailable
	}
	if currentVisibility != request.Visibility {
		var activeMembers int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM redstone_account_share_memberships
			WHERE room_id = $1 AND status IN ('active', 'ending')`, request.RoomID).Scan(&activeMembers); err != nil {
			return Room{}, err
		}
		if activeMembers > 0 {
			return Room{}, ErrRoomHasActiveLeases
		}
	}
	var maxLeaseSeconds int
	var publicAllowed bool
	if err := tx.QueryRowContext(ctx, `
		SELECT max_lease_seconds, public_room_allowed
		FROM redstone_account_share_policies
		WHERE status = 'active' FOR UPDATE`).Scan(&maxLeaseSeconds, &publicAllowed); err != nil {
		return Room{}, err
	}
	if request.LeaseSeconds > maxLeaseSeconds || (request.Visibility == VisibilityPublic && !publicAllowed) {
		return Room{}, ErrRoomUnavailable
	}
	var activeSeats int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_memberships
		WHERE room_id = $1 AND status IN ('active', 'ending')`, request.RoomID).Scan(&activeSeats); err != nil {
		return Room{}, err
	}
	if request.SeatLimit < activeSeats {
		return Room{}, ErrQuotaExceeded
	}
	nextStatus := roomStatusAfterVisibilityChange(currentStatus, currentVisibility, request.Visibility)
	var room Room
	err = tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_rooms
		SET name = $3, description = $4, visibility = $5, requires_approval = $6,
		    seat_limit = $7, lease_seconds = $8, idle_timeout_seconds = $9,
		    lease_price = $10, status = $11, updated_at = NOW()
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
		RETURNING id, owner_user_id, name, description, platform, visibility, status,
		          requires_approval, seat_limit, lease_seconds, idle_timeout_seconds,
		          lease_price, platform_fee_rate, created_at, updated_at`,
		request.RoomID, request.OwnerUserID, strings.TrimSpace(request.Name), strings.TrimSpace(request.Description),
		request.Visibility, request.RequiresApproval, request.SeatLimit, request.LeaseSeconds, request.IdleTimeoutSeconds, request.LeasePrice, nextStatus).
		Scan(&room.ID, &room.OwnerUserID, &room.Name, &room.Description, &room.Platform, &room.Visibility, &room.Status,
			&room.RequiresApproval, &room.SeatLimit, &room.LeaseSeconds, &room.IdleTimeoutSeconds, &room.LeasePrice, &room.PlatformFeeRate,
			&room.CreatedAt, &room.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrRoomForbidden
	}
	if err != nil {
		return Room{}, err
	}
	if err := appendAudit(ctx, tx, request.RoomID, request.OwnerUserID, "room_updated", ""); err != nil {
		return Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return Room{}, err
	}
	return room, nil
}

func (r *PostgresRepository) CloseRoom(ctx context.Context, ownerUserID, roomID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status RoomStatus
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM redstone_account_share_rooms
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL FOR UPDATE`, roomID, ownerUserID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return ErrRoomForbidden
	} else if err != nil {
		return err
	}
	if status == RoomClosed {
		return tx.Commit()
	}
	var activeMembers int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_memberships
		WHERE room_id = $1 AND status IN ('active', 'ending')`, roomID).Scan(&activeMembers); err != nil {
		return err
	}
	if activeMembers > 0 {
		return ErrRoomHasActiveLeases
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_memberships
		SET status = 'revoked', ended_at = NOW(), end_reason = 'owner_closed', updated_at = NOW()
		WHERE room_id = $1 AND status = 'queued'`, roomID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_rooms
		SET status = 'closed', updated_at = NOW()
		WHERE id = $1 AND owner_user_id = $2`, roomID, ownerUserID); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, roomID, ownerUserID, "room_closed", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PostgresRepository) BindAccount(ctx context.Context, request BindAccountRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var platform string
	if err := tx.QueryRowContext(ctx, `
		SELECT platform FROM redstone_account_share_rooms
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
		  AND status IN ('draft', 'pending_review', 'active')
		FOR UPDATE`, request.RoomID, request.OwnerUserID).Scan(&platform); errors.Is(err, sql.ErrNoRows) {
		return ErrRoomForbidden
	} else if err != nil {
		return err
	}
	var accountPlatform string
	if err := tx.QueryRowContext(ctx, `
		SELECT platform FROM accounts
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL AND status = 'active' FOR UPDATE`, request.AccountID, request.OwnerUserID).Scan(&accountPlatform); errors.Is(err, sql.ErrNoRows) {
		return ErrAccountNotOwned
	} else if err != nil {
		return err
	}
	if accountPlatform != platform {
		return ErrInvalidAccount
	}
	if err := r.bindRoomPrivateGroup(ctx, tx, request.RoomID, request.OwnerUserID, request.PrivateGroupID, platform); err != nil {
		return err
	}
	var escrowed bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM redstone_market_account_escrows
			WHERE account_id = $1 AND state IN ('listed', 'reserved', 'transferring')
		)`, request.AccountID).Scan(&escrowed); err != nil {
		return err
	}
	if escrowed {
		return ErrAccountAlreadyBound
	}
	var maxAccounts int
	if err := tx.QueryRowContext(ctx, `
		SELECT max_accounts_per_room
		FROM redstone_account_share_quota_policies
		WHERE active AND (scope = 'global' OR (scope = 'owner' AND owner_user_id = $1))
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY CASE WHEN scope = 'owner' THEN 0 ELSE 1 END, created_at DESC
		LIMIT 1 FOR UPDATE`, request.OwnerUserID).Scan(&maxAccounts); err != nil {
		return err
	}
	var boundAccounts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_account_share_room_accounts WHERE room_id = $1 AND state IN ('active', 'draining')`, request.RoomID).Scan(&boundAccounts); err != nil {
		return err
	}
	if boundAccounts >= maxAccounts {
		return ErrQuotaExceeded
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO redstone_account_share_room_accounts (room_id, account_id)
		VALUES ($1, $2)`, request.RoomID, request.AccountID)
	if err != nil {
		return ErrAccountAlreadyBound
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO account_groups (account_id, group_id)
		VALUES ($1, $2)
		ON CONFLICT (account_id, group_id) DO NOTHING`, request.AccountID, request.PrivateGroupID); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, request.RoomID, request.OwnerUserID, "account_bound", fmt.Sprint(request.AccountID)); err != nil {
		return err
	}
	return tx.Commit()
}

// ListOwnerRoomAccounts returns lifecycle metadata for one owner-scoped room.
// It deliberately omits all account credentials and account-domain details.
func (r *PostgresRepository) ListOwnerRoomAccounts(ctx context.Context, ownerUserID, roomID int64, limit, offset int) ([]RoomAccount, int, error) {
	if ownerUserID <= 0 || roomID <= 0 || !validPage(limit, offset) {
		return nil, 0, ErrInvalidAccount
	}
	var ownedRoomID int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT id FROM redstone_account_share_rooms
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL`, roomID, ownerUserID).Scan(&ownedRoomID); errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrRoomForbidden
	} else if err != nil {
		return nil, 0, err
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM redstone_account_share_room_accounts
		WHERE room_id = $1`, roomID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT room_id, account_id, state, bound_at, unbound_at
		FROM redstone_account_share_room_accounts
		WHERE room_id = $1
		ORDER BY CASE state WHEN 'active' THEN 0 WHEN 'draining' THEN 1 ELSE 2 END, bound_at, account_id
		LIMIT $2 OFFSET $3`, roomID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]RoomAccount, 0)
	for rows.Next() {
		item, err := scanRoomAccount(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// DrainRoomAccount prevents a bound account from being selected for any new
// lease. Existing leases are intentionally not touched and remain usable.
func (r *PostgresRepository) DrainRoomAccount(ctx context.Context, request RoomAccountLifecycleRequest) (RoomAccount, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RoomAccount{}, err
	}
	defer func() { _ = tx.Rollback() }()

	account, err := lockOwnerRoomAccount(ctx, tx, request)
	if err != nil {
		return RoomAccount{}, err
	}
	if account.State != RoomAccountActive {
		return RoomAccount{}, ErrRoomAccountState
	}
	account, err = scanRoomAccount(tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_room_accounts
		SET state = 'draining'
		WHERE room_id = $1 AND account_id = $2 AND state = 'active'
		RETURNING room_id, account_id, state, bound_at, unbound_at`, request.RoomID, request.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomAccount{}, ErrRoomAccountState
	}
	if err != nil {
		return RoomAccount{}, err
	}
	if err := appendAudit(ctx, tx, request.RoomID, request.OwnerUserID, "account_draining", fmt.Sprint(request.AccountID)); err != nil {
		return RoomAccount{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoomAccount{}, err
	}
	return account, nil
}

// RemoveRoomAccount completes the lifecycle after draining. The room lock
// blocks new lease allocation while the active-lease lock makes the absence
// check and removal atomic with respect to lease release/expiry.
func (r *PostgresRepository) RemoveRoomAccount(ctx context.Context, request RoomAccountLifecycleRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	account, err := lockOwnerRoomAccount(ctx, tx, request)
	if err != nil {
		return err
	}
	if account.State != RoomAccountDraining {
		return ErrRoomAccountState
	}
	var activeLeaseID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM redstone_account_share_leases
		WHERE room_id = $1 AND account_id = $2 AND state = 'active'
		ORDER BY id LIMIT 1 FOR UPDATE`, request.RoomID, request.AccountID).Scan(&activeLeaseID)
	if err == nil {
		return ErrRoomAccountHasActiveLease
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var removedAccountID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_room_accounts
		SET state = 'removed', unbound_at = NOW()
		WHERE room_id = $1 AND account_id = $2 AND state = 'draining'
		RETURNING account_id`, request.RoomID, request.AccountID).Scan(&removedAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRoomAccountState
	}
	if err != nil {
		return err
	}
	if privateGroupID, found, err := r.roomPrivateGroupBinding(ctx, tx, request.RoomID); err != nil {
		return err
	} else if found {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM account_groups
			WHERE account_id = $1 AND group_id = $2`, request.AccountID, privateGroupID); err != nil {
			return err
		}
	}
	if err := appendAudit(ctx, tx, request.RoomID, request.OwnerUserID, "account_removed", fmt.Sprint(request.AccountID)); err != nil {
		return err
	}
	return tx.Commit()
}

func lockOwnerRoomAccount(ctx context.Context, tx *sql.Tx, request RoomAccountLifecycleRequest) (RoomAccount, error) {
	var roomID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM redstone_account_share_rooms
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL FOR UPDATE`, request.RoomID, request.OwnerUserID).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return RoomAccount{}, ErrRoomForbidden
	}
	if err != nil {
		return RoomAccount{}, err
	}
	account, err := scanRoomAccount(tx.QueryRowContext(ctx, `
		SELECT room_id, account_id, state, bound_at, unbound_at
		FROM redstone_account_share_room_accounts
		WHERE room_id = $1 AND account_id = $2 FOR UPDATE`, request.RoomID, request.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomAccount{}, ErrRoomAccountNotFound
	}
	return account, err
}

func (r *PostgresRepository) CreateInvite(ctx context.Context, request InviteRequest) error {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO redstone_account_share_invites (room_id, invited_user_id, invited_by_user_id, expires_at)
		SELECT id, $3, $2, $4
		FROM redstone_account_share_rooms
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
		ON CONFLICT (room_id, invited_user_id) WHERE status = 'active'
		DO UPDATE SET expires_at = EXCLUDED.expires_at, updated_at = NOW()`,
		request.RoomID, request.OwnerUserID, request.InvitedUserID, request.ExpiresAt)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrRoomForbidden
	}
	return nil
}

func (r *PostgresRepository) JoinRoom(ctx context.Context, request JoinRoomRequest) (JoinResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return JoinResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.joinRoomTx(ctx, tx, request)
	if err != nil {
		return JoinResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return JoinResult{}, err
	}
	return result, nil
}

// JoinAndSettle creates a paid admission atomically. In particular, the
// private group is not granted until wallet debit and owner credit succeed in
// this same transaction.
func (r *PostgresRepository) JoinAndSettle(ctx context.Context, request JoinRoomRequest, walletService *wallet.Service) (JoinResult, error) {
	if walletService == nil {
		return JoinResult{}, ErrWalletRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return JoinResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.joinRoomTx(ctx, tx, request)
	if err != nil {
		return JoinResult{}, err
	}
	if result.Settlement != nil {
		settled, err := r.settleIntentTx(ctx, tx, result.Settlement.ID, walletService)
		if err != nil {
			return JoinResult{}, err
		}
		result.Settlement = &settled
	}
	if err := tx.Commit(); err != nil {
		return JoinResult{}, err
	}
	return result, nil
}

// DecideMembershipAndSettle resolves a queued, approval-gated admission. A
// successful approval is intentionally not a state-only operation: it charges
// the member and creates an active lease in the same transaction, avoiding an
// active member who permanently occupies a seat without payment or access.
func (r *PostgresRepository) DecideMembershipAndSettle(ctx context.Context, request MembershipDecisionRequest, walletService *wallet.Service) (JoinResult, error) {
	if walletService == nil {
		return JoinResult{}, ErrWalletRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return JoinResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var membershipRoomID int64
	err = tx.QueryRowContext(ctx, `SELECT room_id FROM redstone_account_share_memberships WHERE id = $1`, request.MembershipID).Scan(&membershipRoomID)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinResult{}, ErrMembershipNotFound
	}
	if err != nil {
		return JoinResult{}, err
	}
	if membershipRoomID != request.RoomID {
		return JoinResult{}, ErrMembershipNotFound
	}
	room, err := lockJoinableRoom(ctx, tx, request.RoomID)
	if err != nil {
		return JoinResult{}, err
	}
	if room.OwnerUserID != request.OwnerUserID {
		return JoinResult{}, ErrRoomForbidden
	}
	if !room.RequiresApproval {
		return JoinResult{}, ErrRoomUnavailable
	}

	var membership Membership
	err = tx.QueryRowContext(ctx, `
		SELECT id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason
		FROM redstone_account_share_memberships
		WHERE id = $1 AND room_id = $2 FOR UPDATE`, request.MembershipID, request.RoomID).Scan(
		&membership.ID, &membership.RoomID, &membership.UserID, &membership.Status, &membership.QueuedAt,
		&membership.JoinedAt, &membership.EndedAt, &membership.EndReason)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinResult{}, ErrMembershipNotFound
	}
	if err != nil {
		return JoinResult{}, err
	}

	if request.Decision == MembershipReject {
		if membership.Status == MembershipRevoked && membership.EndReason == "owner_rejected" {
			if err := tx.Commit(); err != nil {
				return JoinResult{}, err
			}
			return JoinResult{Membership: membership}, nil
		}
		if membership.Status != MembershipQueued {
			return JoinResult{}, ErrRoomUnavailable
		}
		err = tx.QueryRowContext(ctx, `
			UPDATE redstone_account_share_memberships
			SET status = 'revoked', ended_at = NOW(), end_reason = 'owner_rejected', updated_at = NOW()
			WHERE id = $1 AND status = 'queued'
			RETURNING id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason`, membership.ID).Scan(
			&membership.ID, &membership.RoomID, &membership.UserID, &membership.Status, &membership.QueuedAt,
			&membership.JoinedAt, &membership.EndedAt, &membership.EndReason)
		if errors.Is(err, sql.ErrNoRows) {
			return JoinResult{}, ErrRoomUnavailable
		}
		if err != nil {
			return JoinResult{}, err
		}
		if err := appendAudit(ctx, tx, room.ID, request.OwnerUserID, "membership_rejected", fmt.Sprint(membership.ID)); err != nil {
			return JoinResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return JoinResult{}, err
		}
		return JoinResult{Membership: membership}, nil
	}

	if membership.Status == MembershipActive {
		leaseResult, err := r.acquireLeaseTx(ctx, tx, membership.UserID, membership.ID, fmt.Sprintf("account-share-owner-approval-%d", membership.ID))
		if err != nil {
			return JoinResult{}, err
		}
		if leaseResult.Settlement != nil {
			settled, err := r.settleIntentTx(ctx, tx, leaseResult.Settlement.ID, walletService)
			if err != nil {
				return JoinResult{}, err
			}
			leaseResult.Settlement = &settled
		}
		if err := tx.Commit(); err != nil {
			return JoinResult{}, err
		}
		return leaseResult, nil
	}
	if membership.Status != MembershipQueued {
		return JoinResult{}, ErrRoomUnavailable
	}
	var activeSeats int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_memberships
		WHERE room_id = $1 AND status IN ('active', 'ending')`, room.ID).Scan(&activeSeats); err != nil {
		return JoinResult{}, err
	}
	if activeSeats >= room.SeatLimit {
		return JoinResult{}, ErrRoomUnavailable
	}
	if _, err := r.activeRoomPrivateGroup(ctx, tx, room.ID); err != nil {
		return JoinResult{}, err
	}
	err = tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_memberships
		SET status = 'active', joined_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'queued'
		RETURNING id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason`, membership.ID).Scan(
		&membership.ID, &membership.RoomID, &membership.UserID, &membership.Status, &membership.QueuedAt,
		&membership.JoinedAt, &membership.EndedAt, &membership.EndReason)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinResult{}, ErrRoomUnavailable
	}
	if err != nil {
		return JoinResult{}, err
	}
	lease, intent, err := createLeaseAndIntent(ctx, tx, room, membership, fmt.Sprintf("account-share-owner-approval-%d", membership.ID))
	if err != nil {
		return JoinResult{}, err
	}
	settled, err := r.settleIntentTx(ctx, tx, intent.ID, walletService)
	if err != nil {
		return JoinResult{}, err
	}
	if err := appendAudit(ctx, tx, room.ID, request.OwnerUserID, "membership_approved", fmt.Sprint(membership.ID)); err != nil {
		return JoinResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return JoinResult{}, err
	}
	return JoinResult{Membership: membership, Lease: &lease, Settlement: &settled}, nil
}

func (r *PostgresRepository) joinRoomTx(ctx context.Context, tx *sql.Tx, request JoinRoomRequest) (JoinResult, error) {
	room, err := lockJoinableRoom(ctx, tx, request.RoomID)
	if err != nil {
		return JoinResult{}, err
	}
	if room.OwnerUserID == request.UserID {
		return JoinResult{}, ErrRoomForbidden
	}
	privateGroupID, err := r.activeRoomPrivateGroup(ctx, tx, room.ID)
	if err != nil {
		return JoinResult{}, err
	}
	if room.Visibility == VisibilityPrivate {
		var inviteID int64
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM redstone_account_share_invites
			WHERE room_id = $1 AND invited_user_id = $2 AND status = 'active'
			  AND (expires_at IS NULL OR expires_at > NOW()) FOR UPDATE`, request.RoomID, request.UserID).Scan(&inviteID)
		if errors.Is(err, sql.ErrNoRows) {
			return JoinResult{}, ErrPrivateInviteRequired
		}
		if err != nil {
			return JoinResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE redstone_account_share_invites SET status = 'accepted', updated_at = NOW() WHERE id = $1`, inviteID); err != nil {
			return JoinResult{}, err
		}
	}
	if existing, found, err := findLiveMembership(ctx, tx, request.RoomID, request.UserID); err != nil {
		return JoinResult{}, err
	} else if found {
		return JoinResult{Membership: existing}, nil
	}
	// A room row lock serializes admission. Expire both hard-expired and idle
	// leases before counting seats, otherwise an abandoned heartbeat can keep a
	// queue blocked forever.
	expiredMemberships, err := tx.QueryContext(ctx, `
		WITH expired_leases AS (
			UPDATE redstone_account_share_leases l
			SET state = 'expired', released_at = NOW(), release_reason = 'idle_timeout', updated_at = NOW()
			FROM redstone_account_share_rooms r
			WHERE l.room_id = $1 AND r.id = l.room_id AND l.state = 'active'
			  AND (l.expires_at <= NOW() OR l.heartbeat_at + (r.idle_timeout_seconds * INTERVAL '1 second') <= NOW())
			RETURNING l.membership_id
		)
		UPDATE redstone_account_share_memberships m
		SET status = 'ended', ended_at = NOW(), end_reason = 'idle_timeout', updated_at = NOW()
		FROM expired_leases e
		WHERE m.id = e.membership_id AND m.status IN ('active', 'ending')
		RETURNING m.id, m.user_id`, request.RoomID)
	if err != nil {
		return JoinResult{}, err
	}
	type expiredMembership struct{ id, userID int64 }
	expired := make([]expiredMembership, 0)
	for expiredMemberships.Next() {
		var membershipID, memberUserID int64
		if err := expiredMemberships.Scan(&membershipID, &memberUserID); err != nil {
			expiredMemberships.Close()
			return JoinResult{}, err
		}
		expired = append(expired, expiredMembership{id: membershipID, userID: memberUserID})
	}
	if err := expiredMemberships.Close(); err != nil {
		return JoinResult{}, err
	}
	if err := expiredMemberships.Err(); err != nil {
		return JoinResult{}, err
	}
	for _, item := range expired {
		if err := revokeShareGroupAccess(ctx, tx, item.id, item.userID, privateGroupID); err != nil {
			return JoinResult{}, err
		}
	}
	var activeSeats int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_account_share_memberships
		WHERE room_id = $1 AND status IN ('active', 'ending')`, request.RoomID).Scan(&activeSeats); err != nil {
		return JoinResult{}, err
	}
	status := MembershipQueued
	if !room.RequiresApproval && activeSeats < room.SeatLimit {
		status = MembershipActive
	}
	var membership Membership
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_account_share_memberships (room_id, user_id, status, joined_at)
		VALUES ($1, $2, $3, CASE WHEN $3 = 'active' THEN NOW() ELSE NULL END)
		RETURNING id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason`, request.RoomID, request.UserID, status).Scan(
		&membership.ID, &membership.RoomID, &membership.UserID, &membership.Status, &membership.QueuedAt, &membership.JoinedAt, &membership.EndedAt, &membership.EndReason)
	if err != nil {
		return JoinResult{}, err
	}
	result := JoinResult{Membership: membership}
	if status == MembershipActive {
		lease, intent, leaseErr := createLeaseAndIntent(ctx, tx, room, membership, request.IdempotencyKey)
		if leaseErr != nil {
			return JoinResult{}, leaseErr
		}
		result.Lease, result.Settlement = &lease, &intent
	}
	return result, nil
}

func (r *PostgresRepository) HeartbeatLease(ctx context.Context, userID, leaseID int64) (Lease, error) {
	var lease Lease
	err := r.db.QueryRowContext(ctx, `
		UPDATE redstone_account_share_leases l
		SET heartbeat_at = NOW(),
		    expires_at = LEAST(l.granted_at + (r.lease_seconds * INTERVAL '1 second'), NOW() + (r.idle_timeout_seconds * INTERVAL '1 second')),
		    updated_at = NOW()
		FROM redstone_account_share_rooms r
		WHERE l.id = $1 AND l.user_id = $2 AND l.state = 'active' AND l.expires_at > NOW() AND r.id = l.room_id
		RETURNING l.id, l.room_id, l.membership_id, l.account_id, l.user_id, l.state, l.granted_at, l.heartbeat_at, l.expires_at, l.released_at, l.release_reason`,
		leaseID, userID).Scan(&lease.ID, &lease.RoomID, &lease.MembershipID, &lease.AccountID, &lease.UserID, &lease.State,
		&lease.GrantedAt, &lease.HeartbeatAt, &lease.ExpiresAt, &lease.ReleasedAt, &lease.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrLeaseNotFound
	}
	return lease, err
}

// ExpireDueLeases releases hard-expired and idle leases in PostgreSQL. It
// locks rooms before their leases so it shares the admission lock order used
// by JoinRoom, and SKIP LOCKED lets every application node run the worker.
func (r *PostgresRepository) ExpireDueLeases(ctx context.Context, batch int) (LeaseExpiryBatchResult, error) {
	if batch < 1 || batch > 100 {
		return LeaseExpiryBatchResult{}, ErrInvalidPagination
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return LeaseExpiryBatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Claim room rows first. JoinRoom also takes the room row lock before it
	// counts capacity or creates a membership, preventing stale promotions.
	roomRows, err := tx.QueryContext(ctx, `
		SELECT r.id
		FROM redstone_account_share_rooms r
		WHERE EXISTS (
			SELECT 1
			FROM redstone_account_share_leases l
			WHERE l.room_id = r.id AND l.state = 'active'
			  AND (
				l.expires_at <= NOW()
				OR l.heartbeat_at + (r.idle_timeout_seconds * INTERVAL '1 second') <= NOW()
			  )
		)
		ORDER BY r.id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, batch)
	if err != nil {
		return LeaseExpiryBatchResult{}, err
	}
	roomIDs := make([]int64, 0, batch)
	for roomRows.Next() {
		var roomID int64
		if err := roomRows.Scan(&roomID); err != nil {
			roomRows.Close()
			return LeaseExpiryBatchResult{}, err
		}
		roomIDs = append(roomIDs, roomID)
	}
	if err := roomRows.Close(); err != nil {
		return LeaseExpiryBatchResult{}, err
	}
	if err := roomRows.Err(); err != nil {
		return LeaseExpiryBatchResult{}, err
	}

	result := LeaseExpiryBatchResult{}
	for _, roomID := range roomIDs {
		remaining := batch - result.Processed
		if remaining <= 0 {
			break
		}
		leaseRows, err := tx.QueryContext(ctx, `
			SELECT l.id,
			       CASE WHEN l.expires_at <= NOW() THEN 'lease_expired' ELSE 'idle_timeout' END
			FROM redstone_account_share_leases l
			JOIN redstone_account_share_rooms r ON r.id = l.room_id
			WHERE l.room_id = $1 AND l.state = 'active'
			  AND (
				l.expires_at <= NOW()
				OR l.heartbeat_at + (r.idle_timeout_seconds * INTERVAL '1 second') <= NOW()
			  )
			ORDER BY l.expires_at, l.id
			LIMIT $2
			FOR UPDATE OF l SKIP LOCKED`, roomID, remaining)
		if err != nil {
			return LeaseExpiryBatchResult{}, err
		}

		type dueLease struct {
			id     int64
			reason string
		}
		leases := make([]dueLease, 0, remaining)
		for leaseRows.Next() {
			var lease dueLease
			if err := leaseRows.Scan(&lease.id, &lease.reason); err != nil {
				leaseRows.Close()
				return LeaseExpiryBatchResult{}, err
			}
			leases = append(leases, lease)
		}
		if err := leaseRows.Close(); err != nil {
			return LeaseExpiryBatchResult{}, err
		}
		if err := leaseRows.Err(); err != nil {
			return LeaseExpiryBatchResult{}, err
		}

		for _, lease := range leases {
			processed, promoted, err := r.expireLeaseTx(ctx, tx, roomID, lease.id, lease.reason)
			if err != nil {
				return LeaseExpiryBatchResult{}, err
			}
			if processed {
				result.Processed++
			}
			if promoted {
				result.Promoted++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return LeaseExpiryBatchResult{}, err
	}
	return result, nil
}

func (r *PostgresRepository) expireLeaseTx(ctx context.Context, tx *sql.Tx, roomID, leaseID int64, reason string) (bool, bool, error) {
	var membershipID, userID int64
	err := tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_leases l
		SET state = 'expired', released_at = NOW(), release_reason = $2, updated_at = NOW()
		FROM redstone_account_share_rooms r
		WHERE l.id = $1 AND l.room_id = $3 AND l.state = 'active' AND r.id = l.room_id
		  AND (
			l.expires_at <= NOW()
			OR l.heartbeat_at + (r.idle_timeout_seconds * INTERVAL '1 second') <= NOW()
		  )
		RETURNING l.membership_id, l.user_id`, leaseID, reason, roomID).Scan(&membershipID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_memberships
		SET status = 'ended', ended_at = NOW(), end_reason = $2, updated_at = NOW()
		WHERE id = $1 AND status IN ('active', 'ending')`, membershipID, reason); err != nil {
		return false, false, err
	}
	if err := revokeActiveShareGroupAccess(ctx, tx, membershipID, userID); err != nil {
		return false, false, err
	}

	var promotedMembershipID int64
	err = tx.QueryRowContext(ctx, `
		WITH next_member AS (
			SELECT id FROM redstone_account_share_memberships
			WHERE room_id = $1 AND status = 'queued'
			ORDER BY queued_at, id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE redstone_account_share_memberships m
		SET status = 'active', joined_at = NOW(), updated_at = NOW()
		FROM next_member WHERE m.id = next_member.id
		RETURNING m.id`, roomID).Scan(&promotedMembershipID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	}
	if err != nil {
		return false, false, err
	}
	// Promotion only enables a later AcquireAndSettle call. Do not grant the
	// private group before its wallet debit and lease are committed.
	return true, true, nil
}

func (r *PostgresRepository) AcquireLease(ctx context.Context, userID, membershipID int64, idempotencyKey string) (JoinResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return JoinResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.acquireLeaseTx(ctx, tx, userID, membershipID, idempotencyKey)
	if err != nil {
		return JoinResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return JoinResult{}, err
	}
	return result, nil
}

// AcquireAndSettle activates a queued member only when their payment and
// group access can commit with the resulting lease.
func (r *PostgresRepository) AcquireAndSettle(ctx context.Context, userID, membershipID int64, idempotencyKey string, walletService *wallet.Service) (JoinResult, error) {
	if walletService == nil {
		return JoinResult{}, ErrWalletRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return JoinResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.acquireLeaseTx(ctx, tx, userID, membershipID, idempotencyKey)
	if err != nil {
		return JoinResult{}, err
	}
	if result.Settlement != nil {
		settled, err := r.settleIntentTx(ctx, tx, result.Settlement.ID, walletService)
		if err != nil {
			return JoinResult{}, err
		}
		result.Settlement = &settled
	}
	if err := tx.Commit(); err != nil {
		return JoinResult{}, err
	}
	return result, nil
}

func (r *PostgresRepository) acquireLeaseTx(ctx context.Context, tx *sql.Tx, userID, membershipID int64, idempotencyKey string) (JoinResult, error) {
	var membership Membership
	err := tx.QueryRowContext(ctx, `
		SELECT id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason
		FROM redstone_account_share_memberships
		WHERE id = $1 AND user_id = $2 FOR UPDATE`, membershipID, userID).Scan(
		&membership.ID, &membership.RoomID, &membership.UserID, &membership.Status, &membership.QueuedAt,
		&membership.JoinedAt, &membership.EndedAt, &membership.EndReason)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinResult{}, ErrMembershipNotFound
	}
	if err != nil {
		return JoinResult{}, err
	}
	if membership.Status != MembershipActive {
		return JoinResult{}, ErrRoomUnavailable
	}
	var existingLease Lease
	err = tx.QueryRowContext(ctx, `
		SELECT id, room_id, membership_id, account_id, user_id, state, granted_at, heartbeat_at, expires_at, released_at, release_reason
		FROM redstone_account_share_leases
		WHERE membership_id = $1 AND state = 'active' AND expires_at > NOW()
		ORDER BY id DESC LIMIT 1 FOR UPDATE`, membershipID).Scan(
		&existingLease.ID, &existingLease.RoomID, &existingLease.MembershipID, &existingLease.AccountID, &existingLease.UserID,
		&existingLease.State, &existingLease.GrantedAt, &existingLease.HeartbeatAt, &existingLease.ExpiresAt,
		&existingLease.ReleasedAt, &existingLease.Reason)
	if err == nil {
		intent, intentErr := loadSettlementIntentByLease(ctx, tx, existingLease.ID, true)
		if intentErr != nil && !errors.Is(intentErr, ErrSettlementNotFound) {
			return JoinResult{}, intentErr
		}
		result := JoinResult{Membership: membership, Lease: &existingLease}
		if intentErr == nil && (intent.Status == SettlementPending || intent.Status == SettlementCharging) {
			result.Settlement = &intent
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return JoinResult{}, err
	}
	room, err := lockJoinableRoom(ctx, tx, membership.RoomID)
	if err != nil {
		return JoinResult{}, err
	}
	if _, err := r.activeRoomPrivateGroup(ctx, tx, room.ID); err != nil {
		return JoinResult{}, err
	}
	lease, intent, err := createLeaseAndIntent(ctx, tx, room, membership, idempotencyKey)
	if err != nil {
		return JoinResult{}, err
	}
	return JoinResult{Membership: membership, Lease: &lease, Settlement: &intent}, nil
}

func (r *PostgresRepository) ReleaseLease(ctx context.Context, userID, leaseID int64, reason string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var roomID int64
	var membershipID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_leases
		SET state = 'released', released_at = NOW(), release_reason = $3, updated_at = NOW()
		WHERE id = $1 AND user_id = $2 AND state = 'active'
		RETURNING room_id, membership_id`, leaseID, userID, reason).Scan(&roomID, &membershipID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_memberships
		SET status = 'ended', ended_at = NOW(), end_reason = $2, updated_at = NOW()
		WHERE id = $1 AND status IN ('active', 'ending')`, membershipID, reason); err != nil {
		return err
	}
	if privateGroupID, found, err := r.roomPrivateGroupBinding(ctx, tx, roomID); err != nil {
		return err
	} else if found {
		if err := revokeShareGroupAccess(ctx, tx, membershipID, userID, privateGroupID); err != nil {
			return err
		}
	}
	var nextMembershipID, nextUserID int64
	err = tx.QueryRowContext(ctx, `
		WITH next_member AS (
			SELECT id FROM redstone_account_share_memberships
			WHERE room_id = $1 AND status = 'queued'
			ORDER BY queued_at, id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE redstone_account_share_memberships m
		SET status = 'active', joined_at = NOW(), updated_at = NOW()
		FROM next_member WHERE m.id = next_member.id
		RETURNING m.id, m.user_id`, roomID).Scan(&nextMembershipID, &nextUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	// Promotion only makes the queued member eligible to call AcquireAndSettle.
	// Granting the private group here would expose the account before their
	// wallet debit and lease settlement have succeeded.
	return tx.Commit()
}

func (r *PostgresRepository) CreateReview(ctx context.Context, request ReviewRequest) error {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO redstone_account_share_reviews (room_id, membership_id, reviewer_user_id, rating, body)
		SELECT m.room_id, m.id, m.user_id, $4, $5
		FROM redstone_account_share_memberships m
		WHERE m.id = $1 AND m.room_id = $2 AND m.user_id = $3 AND m.status = 'ended'
		ON CONFLICT (membership_id) DO UPDATE SET rating = EXCLUDED.rating, body = EXCLUDED.body, updated_at = NOW()`,
		request.MembershipID, request.RoomID, request.UserID, request.Rating, strings.TrimSpace(request.Body))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrReviewNotEligible
	}
	return nil
}

func (r *PostgresRepository) ListRoomReviews(ctx context.Context, viewerUserID, roomID int64, limit, offset int) ([]Review, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM redstone_account_share_reviews review
		JOIN redstone_account_share_rooms room ON room.id = review.room_id AND room.deleted_at IS NULL
		WHERE review.room_id = $2 AND review.moderation_status = 'visible'
		  AND (
			(room.visibility = 'public' AND room.status = 'active')
			OR room.owner_user_id = $1
			OR EXISTS (
				SELECT 1 FROM redstone_account_share_memberships member
				WHERE member.room_id = room.id AND member.user_id = $1
			)
		  )`, viewerUserID, roomID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT review.id, review.room_id, review.membership_id, review.reviewer_user_id, review.rating, review.body, review.created_at, review.updated_at
		FROM redstone_account_share_reviews review
		JOIN redstone_account_share_rooms room ON room.id = review.room_id AND room.deleted_at IS NULL
		WHERE review.room_id = $2 AND review.moderation_status = 'visible'
		  AND (
			(room.visibility = 'public' AND room.status = 'active')
			OR room.owner_user_id = $1
			OR EXISTS (
				SELECT 1 FROM redstone_account_share_memberships member
				WHERE member.room_id = room.id AND member.user_id = $1
			)
		  )
		ORDER BY review.created_at DESC, review.id DESC
		LIMIT $3 OFFSET $4`, viewerUserID, roomID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Review, 0)
	for rows.Next() {
		var item Review
		if err := rows.Scan(&item.ID, &item.RoomID, &item.MembershipID, &item.ReviewerUserID, &item.Rating, &item.Body, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *PostgresRepository) ModerateRoom(ctx context.Context, request RoomModerationRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_rooms
		SET status = $2, review_note = $3, reviewed_by_user_id = $4, reviewed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, request.RoomID, request.Status, strings.TrimSpace(request.Note), request.AdminUserID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrRoomNotFound
	}
	if request.Status == string(RoomActive) {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_leases
		SET state = 'released', released_at = NOW(), release_reason = ('room_' || $2), updated_at = NOW()
		WHERE room_id = $1 AND state = 'active'`, request.RoomID, request.Status); err != nil {
		return err
	}
	privateGroupID, found, err := r.roomPrivateGroupBinding(ctx, tx, request.RoomID)
	if err != nil {
		return err
	}
	members, err := tx.QueryContext(ctx, `
		UPDATE redstone_account_share_memberships
		SET status = 'ended', ended_at = NOW(), end_reason = ('room_' || $2), updated_at = NOW()
		WHERE room_id = $1 AND status IN ('active', 'ending')
		RETURNING id, user_id`, request.RoomID, request.Status)
	if err != nil {
		return err
	}
	type liveMembership struct{ id, userID int64 }
	live := make([]liveMembership, 0)
	for members.Next() {
		var membershipID, memberUserID int64
		if err := members.Scan(&membershipID, &memberUserID); err != nil {
			members.Close()
			return err
		}
		live = append(live, liveMembership{id: membershipID, userID: memberUserID})
	}
	if err := members.Close(); err != nil {
		return err
	}
	if err := members.Err(); err != nil {
		return err
	}
	if found {
		for _, item := range live {
			if err := revokeShareGroupAccess(ctx, tx, item.id, item.userID, privateGroupID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *PostgresRepository) ModerateReview(ctx context.Context, request ReviewModerationRequest) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE redstone_account_share_reviews
		SET moderation_status = $2, moderated_by_user_id = $3, moderation_note = $4, updated_at = NOW()
		WHERE id = $1`, request.ReviewID, request.Status, request.AdminUserID, strings.TrimSpace(request.Note))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrRoomNotFound
	}
	return nil
}

func (r *PostgresRepository) MarkSettlementCharging(ctx context.Context, intentID int64) (SettlementIntent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SettlementIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := loadSettlementIntent(ctx, tx, intentID, true)
	if err != nil {
		return SettlementIntent{}, err
	}
	switch intent.Status {
	case SettlementSettled, SettlementCharging:
		if err = tx.Commit(); err != nil {
			return SettlementIntent{}, err
		}
		return intent, nil
	case SettlementPending:
		if _, err = tx.ExecContext(ctx, `UPDATE redstone_account_share_settlement_intents SET status = 'charging', updated_at = NOW() WHERE id = $1`, intentID); err != nil {
			return SettlementIntent{}, err
		}
		intent.Status = SettlementCharging
		if err = tx.Commit(); err != nil {
			return SettlementIntent{}, err
		}
		return intent, nil
	default:
		return SettlementIntent{}, ErrSettlementState
	}
}

// Settle is deliberately the only paid settlement path. It uses the wallet's
// executor APIs inside this transaction so no committed debit can exist
// without its owner payout, share receipt, and settled intent.
func (r *PostgresRepository) Settle(ctx context.Context, intentID int64, walletService *wallet.Service) (SettlementIntent, error) {
	if walletService == nil {
		return SettlementIntent{}, ErrWalletRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SettlementIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := r.settleIntentTx(ctx, tx, intentID, walletService)
	if err != nil {
		return SettlementIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return SettlementIntent{}, err
	}
	return intent, nil
}

func (r *PostgresRepository) settleIntentTx(ctx context.Context, tx *sql.Tx, intentID int64, walletService *wallet.Service) (SettlementIntent, error) {
	intent, err := lockSettlementIntent(ctx, tx, intentID)
	if err != nil {
		return SettlementIntent{}, err
	}
	if intent.Status == SettlementSettled {
		return intent, nil
	}
	if intent.Status != SettlementPending && intent.Status != SettlementCharging {
		return SettlementIntent{}, ErrSettlementState
	}
	if err := ensureSettlementLeaseActive(ctx, tx, intent); err != nil {
		return SettlementIntent{}, err
	}

	// Stable lock order protects reciprocal rentals from balance-row deadlock.
	if _, err := tx.ExecContext(ctx, `
		SELECT id FROM users
		WHERE id IN ($1, $2) AND deleted_at IS NULL
		ORDER BY id FOR UPDATE`, intent.PayerUserID, intent.OwnerUserID); err != nil {
		return SettlementIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_settlement_intents
		SET status = 'charging', updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`, intent.ID); err != nil {
		return SettlementIntent{}, err
	}

	source := PaymentNormal
	if intent.GrossAmount.IsPositive() {
		charge, err := walletService.ChargeTokenInTransaction(ctx, tx, wallet.TokenChargeRequest{
			UserID:         intent.PayerUserID,
			Amount:         intent.GrossAmount,
			Reference:      wallet.Reference{Type: "account_share_intent", ID: fmt.Sprint(intent.ID)},
			IdempotencyKey: fmt.Sprintf("account-share-debit-%d", intent.ID),
		})
		if err != nil {
			return SettlementIntent{}, err
		}
		source = sourceFromAllocation(charge.Allocation)
	}
	walletKey := fmt.Sprintf("account-share-payout-%d", intent.ID)
	if intent.OwnerAmount.IsPositive() {
		if _, err := walletService.CreditInExecutor(ctx, tx, wallet.CreditRequest{
			UserID:         intent.OwnerUserID,
			Asset:          wallet.AssetNormal,
			Amount:         intent.OwnerAmount,
			Reason:         wallet.CreditSettlement,
			Reference:      wallet.Reference{Type: "account_share_intent", ID: fmt.Sprint(intent.ID)},
			IdempotencyKey: walletKey,
		}); err != nil {
			return SettlementIntent{}, err
		}
	}
	privateGroupID, err := r.activeRoomPrivateGroup(ctx, tx, intent.RoomID)
	if err != nil {
		return SettlementIntent{}, err
	}
	if err := grantShareGroupAccess(ctx, tx, intent.MembershipID, intent.PayerUserID, privateGroupID); err != nil {
		return SettlementIntent{}, err
	}
	if err := tx.QueryRowContext(ctx, `
		UPDATE redstone_account_share_settlement_intents
		SET status = 'settled', payment_source = $2, settled_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'charging'
		RETURNING id, lease_id, membership_id, room_id, payer_user_id, owner_user_id, subscription_id,
		          gross_amount, platform_fee_amount, owner_amount, payment_source, status, idempotency_key,
		          failure_reason, settled_at`, intent.ID, source).Scan(
		&intent.ID, &intent.LeaseID, &intent.MembershipID, &intent.RoomID, &intent.PayerUserID, &intent.OwnerUserID, &intent.SubscriptionID,
		&intent.GrossAmount, &intent.PlatformFee, &intent.OwnerAmount, &intent.PaymentSource, &intent.Status, &intent.IdempotencyKey,
		&intent.FailureReason, &intent.SettledAt); err != nil {
		return SettlementIntent{}, err
	}
	if intent.OwnerAmount.IsPositive() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO redstone_account_share_payout_receipts
			(settlement_intent_id, owner_user_id, receipt_number, amount, wallet_operation_key)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (settlement_intent_id) DO NOTHING`, intent.ID, intent.OwnerUserID,
			fmt.Sprintf("share-%d", intent.ID), intent.OwnerAmount, walletKey); err != nil {
			return SettlementIntent{}, err
		}
	}
	if err := appendAudit(ctx, tx, intent.RoomID, intent.PayerUserID, "settlement_settled", fmt.Sprint(intent.ID)); err != nil {
		return SettlementIntent{}, err
	}
	return intent, nil
}

// ensureSettlementLeaseActive prevents historical or operator-retried intents
// from charging a lease that is no longer usable. Normal admission already
// holds these rows in the same transaction; this is the fail-closed guard for
// every other caller of Settle.
func ensureSettlementLeaseActive(ctx context.Context, tx *sql.Tx, intent SettlementIntent) error {
	var roomID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM redstone_account_share_rooms
		WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		FOR UPDATE`, intent.RoomID).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSettlementState
	}
	if err != nil {
		return err
	}

	var leaseID int64
	err = tx.QueryRowContext(ctx, `
		SELECT l.id
		FROM redstone_account_share_leases l
		JOIN redstone_account_share_memberships m ON m.id = l.membership_id
		WHERE l.id = $1
		  AND l.room_id = $2
		  AND l.membership_id = $3
		  AND l.user_id = $4
		  AND l.state = 'active'
		  AND l.expires_at > NOW()
		  AND m.room_id = $2
		  AND m.id = $3
		  AND m.user_id = $4
		  AND m.status = 'active'
		FOR UPDATE OF l, m`, intent.LeaseID, intent.RoomID, intent.MembershipID, intent.PayerUserID).Scan(&leaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSettlementState
	}
	return err
}

func (r *PostgresRepository) FinalizeSettlement(ctx context.Context, intentID int64, source PaymentSource, walletKey string) (SettlementIntent, error) {
	// Kept only for source compatibility with older callers. A state-only
	// finalizer could mark a fixed lease paid without its wallet debit and owner
	// credit, including by treating an unrelated subscription as payment. The
	// only legal path is settleIntentTx, which commits every effect together.
	return SettlementIntent{}, ErrSettlementState
}

func (r *PostgresRepository) FailSettlement(ctx context.Context, intentID int64, reason string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := loadSettlementIntent(ctx, tx, intentID, true)
	if err != nil {
		return err
	}
	if intent.Status == SettlementSettled || intent.Status == SettlementReversed {
		return ErrSettlementState
	}
	if _, err = tx.ExecContext(ctx, `UPDATE redstone_account_share_settlement_intents SET status = 'failed', failure_reason = $2, updated_at = NOW() WHERE id = $1`, intentID, strings.TrimSpace(reason)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE redstone_account_share_leases SET state = 'released', released_at = NOW(), release_reason = $2, updated_at = NOW() WHERE id = $1 AND state = 'active'`, intent.LeaseID, strings.TrimSpace(reason)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE redstone_account_share_memberships SET status = 'ended', ended_at = NOW(), end_reason = $2, updated_at = NOW() WHERE id = $1 AND status IN ('active','ending')`, intent.MembershipID, strings.TrimSpace(reason)); err != nil {
		return err
	}
	if privateGroupID, found, err := r.roomPrivateGroupBinding(ctx, tx, intent.RoomID); err != nil {
		return err
	} else if found {
		if err := revokeShareGroupAccess(ctx, tx, intent.MembershipID, intent.PayerUserID, privateGroupID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *PostgresRepository) ListPayoutReceipts(ctx context.Context, ownerUserID int64, limit, offset int) ([]PayoutReceipt, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_account_share_payout_receipts WHERE owner_user_id = $1`, ownerUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, settlement_intent_id, receipt_number, amount, created_at
		FROM redstone_account_share_payout_receipts
		WHERE owner_user_id = $1
		ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, ownerUserID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]PayoutReceipt, 0)
	for rows.Next() {
		var item PayoutReceipt
		if err := rows.Scan(&item.ID, &item.SettlementIntentID, &item.ReceiptNumber, &item.Amount, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func lockJoinableRoom(ctx context.Context, tx *sql.Tx, roomID int64) (Room, error) {
	var room Room
	err := tx.QueryRowContext(ctx, `
		SELECT id, owner_user_id, name, description, platform, visibility, status, requires_approval,
		       seat_limit, lease_seconds, idle_timeout_seconds, lease_price, platform_fee_rate, created_at, updated_at
		FROM redstone_account_share_rooms
		WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, roomID).Scan(
		&room.ID, &room.OwnerUserID, &room.Name, &room.Description, &room.Platform, &room.Visibility, &room.Status,
		&room.RequiresApproval, &room.SeatLimit, &room.LeaseSeconds, &room.IdleTimeoutSeconds,
		&room.LeasePrice, &room.PlatformFeeRate, &room.CreatedAt, &room.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrRoomNotFound
	}
	if err != nil {
		return Room{}, err
	}
	if room.Status != RoomActive {
		return Room{}, ErrRoomUnavailable
	}
	return room, nil
}

func lockSettlementIntent(ctx context.Context, tx *sql.Tx, intentID int64) (SettlementIntent, error) {
	var intent SettlementIntent
	err := tx.QueryRowContext(ctx, `
		SELECT id, lease_id, membership_id, room_id, payer_user_id, owner_user_id, subscription_id,
		       gross_amount, platform_fee_amount, owner_amount, payment_source, status, idempotency_key,
		       failure_reason, settled_at
		FROM redstone_account_share_settlement_intents
		WHERE id = $1 FOR UPDATE`, intentID).Scan(
		&intent.ID, &intent.LeaseID, &intent.MembershipID, &intent.RoomID, &intent.PayerUserID, &intent.OwnerUserID, &intent.SubscriptionID,
		&intent.GrossAmount, &intent.PlatformFee, &intent.OwnerAmount, &intent.PaymentSource, &intent.Status, &intent.IdempotencyKey,
		&intent.FailureReason, &intent.SettledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SettlementIntent{}, ErrSettlementNotFound
	}
	return intent, err
}

func findLiveMembership(ctx context.Context, tx *sql.Tx, roomID, userID int64) (Membership, bool, error) {
	var item Membership
	err := tx.QueryRowContext(ctx, `
		SELECT id, room_id, user_id, status, queued_at, joined_at, ended_at, end_reason
		FROM redstone_account_share_memberships
		WHERE room_id = $1 AND user_id = $2 AND status IN ('queued', 'active', 'ending')
		ORDER BY id DESC LIMIT 1 FOR UPDATE`, roomID, userID).Scan(
		&item.ID, &item.RoomID, &item.UserID, &item.Status, &item.QueuedAt, &item.JoinedAt, &item.EndedAt, &item.EndReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Membership{}, false, nil
	}
	return item, err == nil, err
}

func createLeaseAndIntent(ctx context.Context, tx *sql.Tx, room Room, membership Membership, key string) (Lease, SettlementIntent, error) {
	var accountID int64
	err := tx.QueryRowContext(ctx, `
		SELECT ra.account_id
		FROM redstone_account_share_room_accounts ra
		JOIN accounts a ON a.id = ra.account_id
		WHERE ra.room_id = $1 AND ra.state = 'active'
		  AND a.owner_user_id = $2 AND a.deleted_at IS NULL AND a.status = 'active'
		ORDER BY ra.bound_at, ra.account_id
		LIMIT 1 FOR UPDATE OF ra SKIP LOCKED`, room.ID, room.OwnerUserID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, SettlementIntent{}, ErrRoomUnavailable
	}
	if err != nil {
		return Lease{}, SettlementIntent{}, err
	}
	var lease Lease
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_account_share_leases
		(room_id, membership_id, account_id, user_id, expires_at)
		VALUES ($1, $2, $3, $4, LEAST(NOW() + ($5 * INTERVAL '1 second'), NOW() + ($6 * INTERVAL '1 second')))
		RETURNING id, room_id, membership_id, account_id, user_id, state, granted_at, heartbeat_at, expires_at, released_at, release_reason`,
		room.ID, membership.ID, accountID, membership.UserID, room.LeaseSeconds, room.IdleTimeoutSeconds).Scan(
		&lease.ID, &lease.RoomID, &lease.MembershipID, &lease.AccountID, &lease.UserID, &lease.State,
		&lease.GrantedAt, &lease.HeartbeatAt, &lease.ExpiresAt, &lease.ReleasedAt, &lease.Reason)
	if err != nil {
		return Lease{}, SettlementIntent{}, err
	}
	fee := room.LeasePrice.Mul(room.PlatformFeeRate).Round(wallet.MonetaryScale)
	ownerAmount := room.LeasePrice.Sub(fee)
	var intent SettlementIntent
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_account_share_settlement_intents
		(lease_id, membership_id, room_id, payer_user_id, owner_user_id, subscription_id, gross_amount, platform_fee_amount, owner_amount, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, lease_id, membership_id, room_id, payer_user_id, owner_user_id, subscription_id, gross_amount,
		          platform_fee_amount, owner_amount, payment_source, status, idempotency_key, failure_reason, settled_at`,
		lease.ID, membership.ID, room.ID, membership.UserID, room.OwnerUserID, nil, room.LeasePrice, fee, ownerAmount, key).Scan(
		&intent.ID, &intent.LeaseID, &intent.MembershipID, &intent.RoomID, &intent.PayerUserID, &intent.OwnerUserID, &intent.SubscriptionID,
		&intent.GrossAmount, &intent.PlatformFee, &intent.OwnerAmount, &intent.PaymentSource, &intent.Status, &intent.IdempotencyKey,
		&intent.FailureReason, &intent.SettledAt)
	if err != nil {
		return Lease{}, SettlementIntent{}, err
	}
	return lease, intent, nil
}

type rowScanner interface{ Scan(...any) error }

func scanRoom(row rowScanner) (Room, error) {
	var room Room
	err := row.Scan(&room.ID, &room.OwnerUserID, &room.Name, &room.Description, &room.Platform, &room.Visibility, &room.Status,
		&room.RequiresApproval, &room.SeatLimit, &room.LeaseSeconds, &room.IdleTimeoutSeconds,
		&room.LeasePrice, &room.PlatformFeeRate, &room.CreatedAt, &room.UpdatedAt,
		&room.AccountCount, &room.ActiveSeats, &room.AverageRating, &room.ReviewCount)
	return room, err
}

func scanMembership(row rowScanner) (Membership, error) {
	var item Membership
	err := row.Scan(&item.ID, &item.RoomID, &item.UserID, &item.Status, &item.QueuedAt, &item.JoinedAt, &item.EndedAt, &item.EndReason)
	return item, err
}

func scanRoomAccount(row rowScanner) (RoomAccount, error) {
	var item RoomAccount
	var unboundAt sql.NullTime
	err := row.Scan(&item.RoomID, &item.AccountID, &item.State, &item.BoundAt, &unboundAt)
	if err != nil {
		return RoomAccount{}, err
	}
	if unboundAt.Valid {
		value := unboundAt.Time.UTC()
		item.UnboundAt = &value
	}
	return item, nil
}

func loadSettlementIntent(ctx context.Context, tx *sql.Tx, intentID int64, forUpdate bool) (SettlementIntent, error) {
	query := `
		SELECT id, lease_id, membership_id, room_id, payer_user_id, owner_user_id, subscription_id,
		       gross_amount, platform_fee_amount, owner_amount, payment_source, status, idempotency_key,
		       failure_reason, settled_at
		FROM redstone_account_share_settlement_intents WHERE id = $1`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var intent SettlementIntent
	var subscriptionID sql.NullInt64
	var settledAt sql.NullTime
	err := tx.QueryRowContext(ctx, query, intentID).Scan(
		&intent.ID, &intent.LeaseID, &intent.MembershipID, &intent.RoomID, &intent.PayerUserID, &intent.OwnerUserID, &subscriptionID,
		&intent.GrossAmount, &intent.PlatformFee, &intent.OwnerAmount, &intent.PaymentSource, &intent.Status, &intent.IdempotencyKey,
		&intent.FailureReason, &settledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SettlementIntent{}, ErrSettlementNotFound
	}
	if err != nil {
		return SettlementIntent{}, err
	}
	if subscriptionID.Valid {
		value := subscriptionID.Int64
		intent.SubscriptionID = &value
	}
	if settledAt.Valid {
		value := settledAt.Time.UTC()
		intent.SettledAt = &value
	}
	return intent, nil
}

func loadSettlementIntentByLease(ctx context.Context, tx *sql.Tx, leaseID int64, forUpdate bool) (SettlementIntent, error) {
	query := `
		SELECT id, lease_id, membership_id, room_id, payer_user_id, owner_user_id, subscription_id,
		       gross_amount, platform_fee_amount, owner_amount, payment_source, status, idempotency_key,
		       failure_reason, settled_at
		FROM redstone_account_share_settlement_intents WHERE lease_id = $1`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var intent SettlementIntent
	var subscriptionID sql.NullInt64
	var settledAt sql.NullTime
	err := tx.QueryRowContext(ctx, query, leaseID).Scan(
		&intent.ID, &intent.LeaseID, &intent.MembershipID, &intent.RoomID, &intent.PayerUserID, &intent.OwnerUserID, &subscriptionID,
		&intent.GrossAmount, &intent.PlatformFee, &intent.OwnerAmount, &intent.PaymentSource, &intent.Status, &intent.IdempotencyKey,
		&intent.FailureReason, &settledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SettlementIntent{}, ErrSettlementNotFound
	}
	if err != nil {
		return SettlementIntent{}, err
	}
	if subscriptionID.Valid {
		value := subscriptionID.Int64
		intent.SubscriptionID = &value
	}
	if settledAt.Valid {
		value := settledAt.Time.UTC()
		intent.SettledAt = &value
	}
	return intent, nil
}

func appendAudit(ctx context.Context, tx *sql.Tx, roomID, actorUserID int64, action, detail string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_account_share_audits (room_id, actor_user_id, action, detail)
		VALUES ($1, $2, $3, $4)`, roomID, actorUserID, action, detail)
	return err
}

// Compile-time assertion keeps the decimal import coupled to the SQL scanner
// contract; postgres returns NUMERIC into shopspring/decimal directly.
var _ decimal.Decimal
