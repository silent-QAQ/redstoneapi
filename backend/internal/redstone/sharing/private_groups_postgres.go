package sharing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (r *PostgresRepository) CreatePrivateGroup(ctx context.Context, request CreatePrivateGroupRequest) (PrivateGroup, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PrivateGroup{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockActiveUser(ctx, tx, request.OwnerUserID); err != nil {
		return PrivateGroup{}, false, err
	}
	fingerprint := privateGroupFingerprintHash(request)
	if existing, found, err := privateGroupByKey(ctx, tx, request.OwnerUserID, request.IdempotencyKey); err != nil {
		return PrivateGroup{}, false, err
	} else if found {
		if existingFingerprint, err := privateGroupRequestFingerprint(ctx, tx, existing.GroupID); err != nil {
			return PrivateGroup{}, false, err
		} else if existingFingerprint != fingerprint {
			return PrivateGroup{}, false, ErrPrivateGroupConflict
		}
		if err := tx.Commit(); err != nil {
			return PrivateGroup{}, false, err
		}
		return existing, false, nil
	}

	backingName := fmt.Sprintf("redstone-private-%d-%s", request.OwnerUserID, uuid.NewString())
	var groupID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO groups (name, description, rate_multiplier, is_exclusive, status, platform)
		VALUES ($1, $2, 1, TRUE, 'active', $3)
		RETURNING id`, backingName, request.Description, request.Platform).Scan(&groupID); err != nil {
		return PrivateGroup{}, false, err
	}
	var group PrivateGroup
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO redstone_private_groups
			(group_id, owner_user_id, name, platform, status, idempotency_key, request_fingerprint)
		VALUES ($1, $2, $3, $4, 'active', $5, $6)
		RETURNING group_id, owner_user_id, name, platform, status, created_at, updated_at`,
		groupID, request.OwnerUserID, request.Name, request.Platform, request.IdempotencyKey, fingerprint,
	).Scan(&group.GroupID, &group.OwnerUserID, &group.Name, &group.Platform, &group.Status, &group.CreatedAt, &group.UpdatedAt); err != nil {
		return PrivateGroup{}, false, err
	}
	group.Description = request.Description
	group.MemberCount = 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)
		ON CONFLICT (user_id, group_id) DO NOTHING`, request.OwnerUserID, groupID); err != nil {
		return PrivateGroup{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_private_group_members (group_id, user_id, role, status)
		VALUES ($1, $2, 'owner', 'active')`, groupID, request.OwnerUserID); err != nil {
		return PrivateGroup{}, false, err
	}
	if err := appendPrivateGroupAudit(ctx, tx, groupID, request.OwnerUserID, "private_group_created", request.Name); err != nil {
		return PrivateGroup{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PrivateGroup{}, false, err
	}
	return group, true, nil
}

func (r *PostgresRepository) ListPrivateGroups(ctx context.Context, ownerUserID int64, limit, offset int) ([]PrivateGroup, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_private_groups
		WHERE owner_user_id = $1 AND status = 'active'`, ownerUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT pg.group_id, pg.owner_user_id, pg.name, COALESCE(g.description, ''), pg.platform, pg.status,
		       COUNT(m.user_id) FILTER (WHERE m.status = 'active'), pg.created_at, pg.updated_at
		FROM redstone_private_groups pg
		JOIN groups g ON g.id = pg.group_id AND g.deleted_at IS NULL
		LEFT JOIN redstone_private_group_members m ON m.group_id = pg.group_id
		WHERE pg.owner_user_id = $1 AND pg.status = 'active'
		GROUP BY pg.group_id, g.description
		ORDER BY pg.updated_at DESC, pg.group_id DESC
		LIMIT $2 OFFSET $3`, ownerUserID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]PrivateGroup, 0)
	for rows.Next() {
		var item PrivateGroup
		if err := rows.Scan(&item.GroupID, &item.OwnerUserID, &item.Name, &item.Description, &item.Platform, &item.Status, &item.MemberCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *PostgresRepository) ListPrivateGroupMembers(ctx context.Context, ownerUserID, groupID int64, limit, offset int) ([]PrivateGroupMember, int, error) {
	if err := r.requirePrivateGroupOwner(ctx, nil, ownerUserID, groupID, false); err != nil {
		return nil, 0, err
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_private_group_members
		WHERE group_id = $1 AND status = 'active'`, groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT group_id, user_id, role, status, granted_at, revoked_at
		FROM redstone_private_group_members
		WHERE group_id = $1 AND status = 'active'
		ORDER BY granted_at, user_id LIMIT $2 OFFSET $3`, groupID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]PrivateGroupMember, 0)
	for rows.Next() {
		var item PrivateGroupMember
		if err := rows.Scan(&item.GroupID, &item.UserID, &item.Role, &item.Status, &item.GrantedAt, &item.RevokedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *PostgresRepository) GrantPrivateGroupMember(ctx context.Context, request PrivateGroupMemberRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.requirePrivateGroupOwner(ctx, tx, request.OwnerUserID, request.GroupID, true); err != nil {
		return err
	}
	if request.UserID == request.OwnerUserID {
		return ErrPrivateGroupConflict
	}
	if err := lockActiveUser(ctx, tx, request.UserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_private_group_members (group_id, user_id, role, status, granted_at, revoked_at)
		VALUES ($1, $2, 'member', 'active', NOW(), NULL)
		ON CONFLICT (group_id, user_id) DO UPDATE SET
			role = 'member', status = 'active', granted_at = NOW(), revoked_at = NULL`, request.GroupID, request.UserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)
		ON CONFLICT (user_id, group_id) DO NOTHING`, request.UserID, request.GroupID); err != nil {
		return err
	}
	if err := appendPrivateGroupAudit(ctx, tx, request.GroupID, request.OwnerUserID, "private_group_member_granted", fmt.Sprint(request.UserID)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PostgresRepository) RevokePrivateGroupMember(ctx context.Context, request PrivateGroupMemberRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.requirePrivateGroupOwner(ctx, tx, request.OwnerUserID, request.GroupID, true); err != nil {
		return err
	}
	if request.UserID == request.OwnerUserID {
		return ErrPrivateGroupOwnerRequired
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_private_group_members
		SET status = 'revoked', revoked_at = NOW()
		WHERE group_id = $1 AND user_id = $2 AND role = 'member' AND status = 'active'`, request.GroupID, request.UserID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrPrivateGroupNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2`, request.UserID, request.GroupID); err != nil {
		return err
	}
	if err := appendPrivateGroupAudit(ctx, tx, request.GroupID, request.OwnerUserID, "private_group_member_revoked", fmt.Sprint(request.UserID)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PostgresRepository) ArchivePrivateGroup(ctx context.Context, ownerUserID, groupID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.requirePrivateGroupOwner(ctx, tx, ownerUserID, groupID, true); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_private_groups SET status = 'archived', updated_at = NOW()
		WHERE group_id = $1 AND status = 'active'`, groupID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE groups SET status = 'disabled', updated_at = NOW() WHERE id = $1`, groupID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_private_group_members
		SET status = 'revoked', revoked_at = NOW()
		WHERE group_id = $1 AND status = 'active'`, groupID); err != nil {
		return err
	}
	// The established user_allowed_groups trigger enqueues API key cache
	// invalidation for each revoked user.
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_allowed_groups WHERE group_id = $1`, groupID); err != nil {
		return err
	}
	if err := appendPrivateGroupAudit(ctx, tx, groupID, ownerUserID, "private_group_archived", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PostgresRepository) requirePrivateGroupOwner(ctx context.Context, tx *sql.Tx, ownerUserID, groupID int64, forUpdate bool) error {
	query := `
		SELECT pg.owner_user_id
		FROM redstone_private_groups pg
		JOIN groups g ON g.id = pg.group_id
		WHERE pg.group_id = $1 AND pg.status = 'active'
		  AND g.deleted_at IS NULL AND g.status = 'active' AND g.is_exclusive = TRUE`
	if forUpdate {
		query += ` FOR UPDATE OF pg, g`
	}
	var actualOwner int64
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, query, groupID).Scan(&actualOwner)
	} else {
		err = r.db.QueryRowContext(ctx, query, groupID).Scan(&actualOwner)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPrivateGroupNotFound
	}
	if err != nil {
		return err
	}
	if actualOwner != ownerUserID {
		return ErrPrivateGroupForbidden
	}
	return nil
}

func privateGroupByKey(ctx context.Context, tx *sql.Tx, ownerUserID int64, key string) (PrivateGroup, bool, error) {
	query := `
		SELECT pg.group_id, pg.owner_user_id, pg.name, COALESCE(g.description, ''), pg.platform, pg.status,
		       COUNT(m.user_id) FILTER (WHERE m.status = 'active'), pg.created_at, pg.updated_at
		FROM redstone_private_groups pg
		JOIN groups g ON g.id = pg.group_id AND g.deleted_at IS NULL
		LEFT JOIN redstone_private_group_members m ON m.group_id = pg.group_id
		WHERE pg.owner_user_id = $1 AND pg.idempotency_key = $2
		GROUP BY pg.group_id, g.description`
	var group PrivateGroup
	err := tx.QueryRowContext(ctx, query, ownerUserID, key).Scan(
		&group.GroupID, &group.OwnerUserID, &group.Name, &group.Description, &group.Platform, &group.Status,
		&group.MemberCount, &group.CreatedAt, &group.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PrivateGroup{}, false, nil
	}
	return group, err == nil, err
}

func privateGroupRequestFingerprint(ctx context.Context, tx *sql.Tx, groupID int64) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint FROM redstone_private_groups WHERE group_id = $1`, groupID).Scan(&value)
	return value, err
}

func privateGroupFingerprintHash(request CreatePrivateGroupRequest) string {
	hash := sha256.Sum256([]byte(privateGroupFingerprint(request)))
	return hex.EncodeToString(hash[:])
}

func lockActiveUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL AND status = 'active' FOR UPDATE`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPrivateGroupForbidden
	}
	return err
}

func appendPrivateGroupAudit(ctx context.Context, tx *sql.Tx, groupID, actorUserID int64, action, detail string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_private_group_audits (group_id, actor_user_id, action, detail)
		VALUES ($1, $2, $3, $4)`, groupID, actorUserID, action, strings.TrimSpace(detail))
	return err
}

// bindRoomPrivateGroup establishes the only gateway group a room may expose.
// It is called under the room row lock taken by BindAccount, so a room cannot
// switch groups while a second account is being attached.
func (r *PostgresRepository) bindRoomPrivateGroup(ctx context.Context, tx *sql.Tx, roomID, ownerUserID, groupID int64, platform string) error {
	var groupOwner int64
	var groupPlatform string
	err := tx.QueryRowContext(ctx, `
		SELECT pg.owner_user_id, g.platform
		FROM redstone_private_groups pg
		JOIN groups g ON g.id = pg.group_id
		WHERE pg.group_id = $1 AND pg.status = 'active'
		  AND g.deleted_at IS NULL AND g.status = 'active' AND g.is_exclusive = TRUE
		FOR UPDATE OF pg, g`, groupID).Scan(&groupOwner, &groupPlatform)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPrivateGroupNotFound
	}
	if err != nil {
		return err
	}
	if groupOwner != ownerUserID {
		return ErrPrivateGroupForbidden
	}
	if groupPlatform != platform {
		return ErrInvalidAccount
	}

	var boundGroupID int64
	err = tx.QueryRowContext(ctx, `
		SELECT group_id FROM redstone_account_share_room_private_groups
		WHERE room_id = $1 FOR UPDATE`, roomID).Scan(&boundGroupID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO redstone_account_share_room_private_groups (room_id, group_id)
			VALUES ($1, $2)`, roomID, groupID)
		return err
	}
	if err != nil {
		return err
	}
	if boundGroupID != groupID {
		return ErrPrivateGroupConflict
	}
	return nil
}

func (r *PostgresRepository) activeRoomPrivateGroup(ctx context.Context, tx *sql.Tx, roomID int64) (int64, error) {
	var groupID int64
	err := tx.QueryRowContext(ctx, `
		SELECT rpg.group_id
		FROM redstone_account_share_room_private_groups rpg
		JOIN redstone_private_groups pg ON pg.group_id = rpg.group_id
		JOIN groups g ON g.id = rpg.group_id
		WHERE rpg.room_id = $1 AND pg.status = 'active'
		  AND g.deleted_at IS NULL AND g.status = 'active' AND g.is_exclusive = TRUE
		FOR UPDATE OF rpg, pg, g`, roomID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrPrivateGroupRequired
	}
	return groupID, err
}

func (r *PostgresRepository) roomPrivateGroupBinding(ctx context.Context, tx *sql.Tx, roomID int64) (int64, bool, error) {
	var groupID int64
	err := tx.QueryRowContext(ctx, `
		SELECT group_id FROM redstone_account_share_room_private_groups
		WHERE room_id = $1`, roomID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return groupID, err == nil, err
}

func grantShareGroupAccess(ctx context.Context, tx *sql.Tx, membershipID, userID, groupID int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_account_share_group_grants (membership_id, group_id, user_id, status, granted_at, revoked_at)
		VALUES ($1, $2, $3, 'active', NOW(), NULL)
		ON CONFLICT (membership_id, group_id) DO UPDATE SET
			status = 'active', granted_at = NOW(), revoked_at = NULL`, membershipID, groupID, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)
		ON CONFLICT (user_id, group_id) DO NOTHING`, userID, groupID)
	return err
}

func revokeShareGroupAccess(ctx context.Context, tx *sql.Tx, membershipID, userID, groupID int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_account_share_group_grants
		SET status = 'revoked', revoked_at = NOW()
		WHERE membership_id = $1 AND group_id = $2 AND user_id = $3 AND status = 'active'`, membershipID, groupID, userID); err != nil {
		return err
	}
	// A user can retain access through a different live room or a direct
	// private-group membership. Never remove either source when one lease ends.
	_, err := tx.ExecContext(ctx, `
		DELETE FROM user_allowed_groups uag
		WHERE uag.user_id = $1 AND uag.group_id = $2
		  AND NOT EXISTS (
			  SELECT 1 FROM redstone_account_share_group_grants sg
			  WHERE sg.user_id = $1 AND sg.group_id = $2 AND sg.status = 'active'
		  )
		  AND NOT EXISTS (
			  SELECT 1 FROM redstone_private_group_members pgm
			  WHERE pgm.user_id = $1 AND pgm.group_id = $2 AND pgm.status = 'active'
		  )`, userID, groupID)
	return err
}

func revokeActiveShareGroupAccess(ctx context.Context, tx *sql.Tx, membershipID, userID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT group_id
		FROM redstone_account_share_group_grants
		WHERE membership_id = $1 AND user_id = $2 AND status = 'active'
		FOR UPDATE`, membershipID, userID)
	if err != nil {
		return err
	}

	groupIDs := make([]int64, 0, 1)
	for rows.Next() {
		var groupID int64
		if err := rows.Scan(&groupID); err != nil {
			return err
		}
		groupIDs = append(groupIDs, groupID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, groupID := range groupIDs {
		if err := revokeShareGroupAccess(ctx, tx, membershipID, userID, groupID); err != nil {
			return err
		}
	}
	return nil
}
