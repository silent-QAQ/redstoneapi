package sharing

import "context"

// ListAPIKeyRoomOptions returns the rooms a user can inspect from the API-key
// room-mode picker. Public rooms remain visible before admission; the gateway
// still requires an active lease before it routes a shared account for them.
func (r *PostgresRepository) ListAPIKeyRoomOptions(ctx context.Context, userID int64, limit, offset int) ([]APIKeyRoomOption, int, error) {
	const eligibleRooms = `
		FROM redstone_account_share_rooms room
		JOIN redstone_account_share_room_private_groups room_group ON room_group.room_id = room.id
		JOIN redstone_private_groups private_group
		  ON private_group.group_id = room_group.group_id AND private_group.status = 'active'
		JOIN groups group_record
		  ON group_record.id = room_group.group_id
		 AND group_record.deleted_at IS NULL AND group_record.status = 'active' AND group_record.is_exclusive = TRUE
		WHERE room.status = 'active' AND room.deleted_at IS NULL
		  AND (
			room.owner_user_id = $1
			OR room.visibility = 'public'
			OR EXISTS (
				SELECT 1 FROM redstone_account_share_memberships membership
				WHERE membership.room_id = room.id
				  AND membership.user_id = $1
				  AND membership.status = 'active'
			)
		  )`

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) "+eligibleRooms, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT room.id, room_group.group_id, room.name, room.platform, room.visibility,
			COALESCE(room.room_rate_multiplier, 1.0) AS rate_multiplier,
			(room.owner_user_id = $1 AND room.visibility = 'private') AS free_for_owner,
			EXISTS (
				SELECT 1 FROM redstone_account_share_memberships membership
				WHERE membership.room_id = room.id
				  AND membership.user_id = $1
				  AND membership.status = 'active'
			) AS has_active_membership
	`+eligibleRooms+`
		ORDER BY
			CASE WHEN room.owner_user_id = $1 THEN 0
				 WHEN EXISTS (
					SELECT 1 FROM redstone_account_share_memberships membership
					WHERE membership.room_id = room.id AND membership.user_id = $1 AND membership.status = 'active'
				 ) THEN 1
				 ELSE 2 END,
			room.updated_at DESC, room.id DESC
		LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]APIKeyRoomOption, 0)
	for rows.Next() {
		var item APIKeyRoomOption
		if err := rows.Scan(
			&item.RoomID, &item.GroupID, &item.Name, &item.Platform, &item.Visibility,
			&item.RateMultiplier, &item.FreeForOwner, &item.HasActiveMember,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}
