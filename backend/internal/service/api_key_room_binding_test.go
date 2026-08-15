//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type apiKeyRoomBindingRepoStub struct {
	apiKeyRepoStub
	bindingCalls   []APIKeyRoomBinding
	unbindingCalls []int64
	bindingErr     error
	unbindingErr   error
}

func (s *apiKeyRoomBindingRepoStub) BindSharingRoom(_ context.Context, apiKeyID, userID, roomID int64) (*APIKeyRoomBinding, error) {
	if s.bindingErr != nil {
		return nil, s.bindingErr
	}
	binding := APIKeyRoomBinding{APIKeyID: apiKeyID, RoomID: roomID, GroupID: 42, RateMultiplier: 1}
	s.bindingCalls = append(s.bindingCalls, binding)
	groupID := binding.GroupID
	sharingRoomID := binding.RoomID
	s.apiKey.GroupID = &groupID
	s.apiKey.SharingRoomID = &sharingRoomID
	rateMultiplier := binding.RateMultiplier
	s.apiKey.SharingRoomRateMultiplier = &rateMultiplier
	return &binding, nil
}

func (s *apiKeyRoomBindingRepoStub) UnbindSharingRoom(_ context.Context, apiKeyID, _ int64) error {
	if s.unbindingErr != nil {
		return s.unbindingErr
	}
	s.unbindingCalls = append(s.unbindingCalls, apiKeyID)
	s.apiKey.GroupID = nil
	s.apiKey.SharingRoomID = nil
	s.apiKey.SharingRoomRateMultiplier = nil
	return nil
}

func TestAPIKeyServiceBindSharingRoomUpdatesAuthorizationAndInvalidatesCache(t *testing.T) {
	repo := &apiKeyRoomBindingRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
		ID: 7, UserID: 11, Key: "sk-sharing", Status: StatusActive,
	}}}
	cache := &apiKeyCacheStub{}
	svc := &APIKeyService{apiKeyRepo: repo, cache: cache}

	key, err := svc.BindSharingRoom(context.Background(), 7, 11, 23)
	require.NoError(t, err)
	require.Equal(t, []APIKeyRoomBinding{{APIKeyID: 7, RoomID: 23, GroupID: 42, RateMultiplier: 1}}, repo.bindingCalls)
	require.NotNil(t, key.GroupID)
	require.Equal(t, int64(42), *key.GroupID)
	require.NotNil(t, key.SharingRoomID)
	require.Equal(t, int64(23), *key.SharingRoomID)
	require.Len(t, cache.deleteAuthKeys, 1)
}

func TestAPIKeyServiceUnbindSharingRoomClearsAuthorizationAndInvalidatesCache(t *testing.T) {
	groupID, roomID := int64(42), int64(23)
	repo := &apiKeyRoomBindingRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
		ID: 7, UserID: 11, Key: "sk-sharing", Status: StatusActive, GroupID: &groupID, SharingRoomID: &roomID,
	}}}
	cache := &apiKeyCacheStub{}
	svc := &APIKeyService{apiKeyRepo: repo, cache: cache}

	key, err := svc.UnbindSharingRoom(context.Background(), 7, 11)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, repo.unbindingCalls)
	require.Nil(t, key.GroupID)
	require.Nil(t, key.SharingRoomID)
	require.Len(t, cache.deleteAuthKeys, 1)
}

func TestAPIKeyServiceBindSharingRoomRejectsOtherUsers(t *testing.T) {
	repo := &apiKeyRoomBindingRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
		ID: 7, UserID: 11, Key: "sk-sharing", Status: StatusActive,
	}}}
	svc := &APIKeyService{apiKeyRepo: repo}

	_, err := svc.BindSharingRoom(context.Background(), 7, 12, 23)
	require.ErrorIs(t, err, ErrInsufficientPerms)
	require.Empty(t, repo.bindingCalls)
}
