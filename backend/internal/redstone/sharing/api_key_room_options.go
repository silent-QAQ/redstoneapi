package sharing

import (
	"context"
)

// APIKeyRoomOption is the room-mode projection for the API key selector. The
// scheduler continues to receive GroupID; RoomID is retained by room-mode
// bindings so gateway authorization can enforce the user's exact choice.
type APIKeyRoomOption struct {
	RoomID          int64          `json:"room_id"`
	GroupID         int64          `json:"group_id"`
	Name            string         `json:"name"`
	Platform        string         `json:"platform"`
	Visibility      RoomVisibility `json:"visibility"`
	RateMultiplier  float64        `json:"rate_multiplier"`
	FreeForOwner    bool           `json:"free_for_owner"`
	HasActiveMember bool           `json:"has_active_membership"`
}

// APIKeyRoomOptionRepository is separated from the room mutation repository
// because option listing is a user-scoped projection only. It exposes no
// credential, account, or invitation detail.
type APIKeyRoomOptionRepository interface {
	ListAPIKeyRoomOptions(context.Context, int64, int, int) ([]APIKeyRoomOption, int, error)
}

func (s *Service) ListAPIKeyRoomOptions(ctx context.Context, userID int64, limit, offset int) ([]APIKeyRoomOption, int, error) {
	if userID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPagination)
	}
	repository, ok := s.repository.(APIKeyRoomOptionRepository)
	if !ok {
		return nil, 0, applicationError(ErrRepositoryRequired)
	}
	items, total, err := repository.ListAPIKeyRoomOptions(ctx, userID, limit, offset)
	return items, total, applicationError(err)
}
