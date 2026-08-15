package repository

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsMissingAPIKeyRoomBindingTable(t *testing.T) {
	require.True(t, isMissingAPIKeyRoomBindingTable(errors.New("SQL logic error: no such table: redstone_api_key_room_bindings (1)")))
	require.True(t, isMissingAPIKeyRoomBindingTable(errors.New(`relation "redstone_api_key_room_bindings" does not exist`)))
	require.False(t, isMissingAPIKeyRoomBindingTable(errors.New("database connection refused")))
}
