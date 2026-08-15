package sharing

import (
	"context"
	"database/sql"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/ctxkey"
	"github.com/lib/pq"
)

// AccessGuard answers whether gateway candidates are usable by the authenticated
// user. Accounts that are not bound to a live sharing room remain unaffected.
// Bound accounts require an active, unexpired lease in the selected private
// group; this is intentionally a direct database read rather than a cache.
type AccessGuard struct {
	db *sql.DB
}

func NewAccessGuard(db *sql.DB) *AccessGuard {
	return &AccessGuard{db: db}
}

// AllowedAccountIDs returns the subset that may be used for this request.
// A nil group cannot authorize a shared account. Draining bindings cannot be
// selected for new leases, but an existing active lease continues to work
// until it is released or expires.
//
// The guarded gateway calls this method only for authenticated requests. The
// query therefore also records bounded lease activity for a selected shared
// account. Keeping that update in the authorization boundary prevents a
// browser-only heartbeat from being the sole way to keep an actively used
// account alive, while the interval predicate avoids writing on every request.
func (g *AccessGuard) AllowedAccountIDs(ctx context.Context, userID int64, groupID *int64, accountIDs []int64) (map[int64]struct{}, error) {
	allowed := make(map[int64]struct{}, len(accountIDs))
	if len(accountIDs) == 0 {
		return allowed, nil
	}
	if g == nil || g.db == nil {
		return nil, ErrRepositoryRequired
	}
	if userID <= 0 {
		return allowed, nil
	}
	roomID, _ := ctx.Value(ctxkey.SharingRoomID).(int64)
	roomPredicate := ""
	ownerRoomPredicate := " AND room.id = -1"
	// A room-mode key must never fall back to ordinary accounts merely because
	// they share the mapped private group. Without this branch, any unbound
	// account in that group could bypass the key's exact room selection.
	ordinaryAccountPredicate := `NOT EXISTS (
			SELECT 1
			FROM redstone_account_share_room_accounts binding
			WHERE binding.account_id = requested.account_id
			  AND binding.state IN ('active', 'draining')
		)`
	args := []any{pq.Array(accountIDs), userID, groupID}
	if roomID > 0 {
		roomPredicate = " AND room.id = $4"
		ownerRoomPredicate = roomPredicate
		ordinaryAccountPredicate = "FALSE"
		args = append(args, roomID)
	}

	rows, err := g.db.QueryContext(ctx, `
		WITH lease_activity AS (
			SELECT l.id, r.lease_seconds, r.idle_timeout_seconds
			FROM redstone_account_share_leases l
			JOIN redstone_account_share_room_accounts binding
			  ON binding.room_id = l.room_id
			 AND binding.account_id = l.account_id
			 AND binding.state IN ('active', 'draining')
			JOIN redstone_account_share_rooms room
			  ON room.id = l.room_id
			 AND room.status = 'active'
			 AND room.deleted_at IS NULL
			JOIN redstone_account_share_room_private_groups room_group
			  ON room_group.room_id = room.id
			JOIN redstone_private_groups private_group
			  ON private_group.group_id = room_group.group_id
			 AND private_group.status = 'active'
			JOIN redstone_account_share_memberships membership
			  ON membership.id = l.membership_id
			 AND membership.room_id = room.id
			 AND membership.user_id = $2
			 AND membership.status = 'active'
			JOIN redstone_account_share_group_grants grant
			  ON grant.membership_id = membership.id
			 AND grant.group_id = room_group.group_id
			 AND grant.user_id = $2
			 AND grant.status = 'active'
			WHERE l.account_id = ANY($1::bigint[])
			  AND l.user_id = $2
			  AND l.state = 'active'
			  AND l.expires_at > NOW()
			  AND room_group.group_id = $3
			  `+roomPredicate+`
		), refreshed_leases AS (
			UPDATE redstone_account_share_leases lease
			SET heartbeat_at = NOW(),
			    expires_at = LEAST(
			      lease.granted_at + (activity.lease_seconds * INTERVAL '1 second'),
			      NOW() + (activity.idle_timeout_seconds * INTERVAL '1 second')
			    ),
			    updated_at = NOW()
			FROM lease_activity activity
			WHERE lease.id = activity.id
			  AND lease.heartbeat_at + (
			    GREATEST(5, LEAST(30, activity.idle_timeout_seconds / 3)) * INTERVAL '1 second'
			  ) <= NOW()
			RETURNING lease.id
		)
		SELECT requested.account_id
		FROM unnest($1::bigint[]) AS requested(account_id)
		CROSS JOIN (SELECT COUNT(*) FROM refreshed_leases) AS refresh_marker
		WHERE `+ordinaryAccountPredicate+`
		OR EXISTS (
			SELECT 1
			FROM redstone_account_share_room_accounts binding
			JOIN redstone_account_share_rooms room ON room.id = binding.room_id
			JOIN redstone_account_share_room_private_groups room_group ON room_group.room_id = room.id
			JOIN redstone_private_groups private_group ON private_group.group_id = room_group.group_id
			JOIN redstone_account_share_memberships membership
			  ON membership.room_id = room.id
			 AND membership.user_id = $2
			 AND membership.status = 'active'
			JOIN redstone_account_share_group_grants grant
			  ON grant.membership_id = membership.id
			 AND grant.group_id = room_group.group_id
			 AND grant.user_id = $2
			 AND grant.status = 'active'
			JOIN redstone_account_share_leases lease
			  ON lease.room_id = room.id
			 AND lease.membership_id = membership.id
			 AND lease.account_id = binding.account_id
			 AND lease.user_id = $2
			 AND lease.state = 'active'
			 AND lease.expires_at > NOW()
			WHERE binding.account_id = requested.account_id
			  AND binding.state IN ('active', 'draining')
			  AND room.status = 'active'
			  AND room.deleted_at IS NULL
			  AND private_group.status = 'active'
			  AND room_group.group_id = $3
			  `+roomPredicate+`
		)
		OR EXISTS (
			SELECT 1
			FROM redstone_account_share_room_accounts binding
			JOIN redstone_account_share_rooms room ON room.id = binding.room_id
			JOIN redstone_account_share_room_private_groups room_group ON room_group.room_id = room.id
			JOIN redstone_private_groups private_group ON private_group.group_id = room_group.group_id
			WHERE binding.account_id = requested.account_id
			  AND binding.state IN ('active', 'draining')
			  AND room.owner_user_id = $2
			  AND room.visibility = 'private'
			  AND room.status = 'active'
			  AND room.deleted_at IS NULL
			  AND private_group.status = 'active'
			  AND room_group.group_id = $3
			  `+ownerRoomPredicate+`
		)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return nil, err
		}
		allowed[accountID] = struct{}{}
	}
	return allowed, rows.Err()
}
