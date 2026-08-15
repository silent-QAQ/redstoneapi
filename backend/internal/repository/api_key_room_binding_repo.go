package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/silent-QAQ/redstoneapi/internal/service"
)

// BindSharingRoom atomically pins an API key to a room and to that room's
// private group. Authorization is evaluated in SQL so a public room, its
// owner, and an active private-room member all follow the same rule.
func (r *apiKeyRepository) BindSharingRoom(ctx context.Context, apiKeyID, userID, roomID int64) (*service.APIKeyRoomBinding, error) {
	db, ok := r.sql.(*sql.DB)
	if !ok || db == nil {
		return nil, service.ErrAPIKeyRoomBindingUnavailable
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin api key room binding: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var keyID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM api_keys
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		FOR UPDATE`, apiKeyID, userID).Scan(&keyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("lock api key for room binding: %w", err)
	}

	binding := &service.APIKeyRoomBinding{APIKeyID: apiKeyID, RoomID: roomID, RateMultiplier: 1}
	if err := tx.QueryRowContext(ctx, `
		SELECT rpg.group_id
		FROM redstone_account_share_rooms r
		JOIN redstone_account_share_room_private_groups rpg ON rpg.room_id = r.id
		JOIN redstone_private_groups pg ON pg.group_id = rpg.group_id AND pg.status = 'active'
		WHERE r.id = $1
		  AND r.status = 'active'
		  AND r.deleted_at IS NULL
		  AND (
			r.visibility = 'public'
			OR r.owner_user_id = $2
			OR EXISTS (
				SELECT 1
				FROM redstone_account_share_memberships m
				WHERE m.room_id = r.id
				  AND m.user_id = $2
				  AND m.status IN ('active', 'ending')
			)
		  )
		FOR UPDATE OF r`, roomID, userID).Scan(&binding.GroupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrAPIKeyRoomUnavailable
		}
		return nil, fmt.Errorf("resolve accessible sharing room: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE api_keys
		SET group_id = $1, updated_at = NOW()
		WHERE id = $2 AND user_id = $3 AND deleted_at IS NULL`, binding.GroupID, apiKeyID, userID); err != nil {
		return nil, fmt.Errorf("set api key room group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_api_key_room_bindings
			(api_key_id, room_id, group_id, rate_multiplier, bound_by_user_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (api_key_id) DO UPDATE
		SET room_id = EXCLUDED.room_id,
			group_id = EXCLUDED.group_id,
			rate_multiplier = EXCLUDED.rate_multiplier,
			bound_by_user_id = EXCLUDED.bound_by_user_id,
			updated_at = NOW()`, apiKeyID, roomID, binding.GroupID, binding.RateMultiplier, userID); err != nil {
		return nil, fmt.Errorf("upsert api key room binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_api_key_room_binding_audits
			(api_key_id, room_id, group_id, actor_user_id, action)
		VALUES ($1, $2, $3, $4, 'bound')`, apiKeyID, roomID, binding.GroupID, userID); err != nil {
		return nil, fmt.Errorf("audit api key room binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit api key room binding: %w", err)
	}
	return binding, nil
}

// UnbindSharingRoom is deliberately idempotent. It clears group_id only when
// a room binding existed, so deleting an already-absent binding never erases a
// separately configured ordinary API-key group.
func (r *apiKeyRepository) UnbindSharingRoom(ctx context.Context, apiKeyID, userID int64) error {
	db, ok := r.sql.(*sql.DB)
	if !ok || db == nil {
		return service.ErrAPIKeyRoomBindingUnavailable
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin api key room unbinding: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var keyID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM api_keys
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		FOR UPDATE`, apiKeyID, userID).Scan(&keyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrAPIKeyNotFound
		}
		return fmt.Errorf("lock api key for room unbinding: %w", err)
	}

	var boundRoomID, boundGroupID int64
	err = tx.QueryRowContext(ctx, `
		DELETE FROM redstone_api_key_room_bindings
		WHERE api_key_id = $1
		RETURNING room_id, group_id`, apiKeyID).Scan(&boundRoomID, &boundGroupID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("delete api key room binding: %w", err)
	}
	if err == nil {
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_keys
			SET group_id = NULL, updated_at = NOW()
			WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, apiKeyID, userID); err != nil {
			return fmt.Errorf("clear api key room group: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO redstone_api_key_room_binding_audits
				(api_key_id, room_id, group_id, actor_user_id, action)
			VALUES ($1, $2, $3, $4, 'unbound')`, apiKeyID, boundRoomID, boundGroupID, userID); err != nil {
			return fmt.Errorf("audit api key room unbinding: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit api key room unbinding: %w", err)
	}
	return nil
}
