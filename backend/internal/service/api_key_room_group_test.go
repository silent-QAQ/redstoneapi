//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type roomGroupIDRepositoryStub struct {
	GroupRepository
	ids []int64
}

func (s *roomGroupIDRepositoryStub) ListSharingRoomGroupIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), s.ids...), nil
}

func TestEnsureOrdinaryAPIKeyGroupRejectsReservedRoomGroup(t *testing.T) {
	svc := &APIKeyService{groupRepo: &roomGroupIDRepositoryStub{ids: []int64{42}}}
	err := svc.ensureOrdinaryAPIKeyGroup(context.Background(), 42)
	require.ErrorIs(t, err, ErrAPIKeyRoomGroupReserved)
}

func TestEnsureOrdinaryAPIKeyGroupAllowsNormalGroup(t *testing.T) {
	svc := &APIKeyService{groupRepo: &roomGroupIDRepositoryStub{ids: []int64{42}}}
	require.NoError(t, svc.ensureOrdinaryAPIKeyGroup(context.Background(), 43))
}
