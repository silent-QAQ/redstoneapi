package controlledaccount

import (
	"testing"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestOAuthBindingStoreConsumeSuccessIsSingleUse(t *testing.T) {
	store := newOAuthBindingStore()
	store.Bind("session-1", "state-1", 7)

	err := store.Consume("session-1", "state-1", 7)
	require.NoError(t, err)

	err = store.Consume("session-1", "state-1", 7)
	require.Error(t, err)
	require.Equal(t, "USER_ACCOUNT_OAUTH_BINDING_MISSING", infraerrors.Reason(err))
}

func TestOAuthBindingStoreConsumeRejectsDifferentUser(t *testing.T) {
	store := newOAuthBindingStore()
	store.Bind("session-2", "state-2", 7)

	err := store.Consume("session-2", "state-2", 8)
	require.Error(t, err)
	require.Equal(t, "USER_ACCOUNT_OAUTH_BINDING_MISMATCH", infraerrors.Reason(err))
}

func TestOAuthBindingStoreConsumeRequiresExactStateWhenBound(t *testing.T) {
	store := newOAuthBindingStore()
	store.Bind("session-3", "state-3", 7)

	err := store.Consume("session-3", "", 7)
	require.Error(t, err)
	require.Equal(t, "USER_ACCOUNT_OAUTH_STATE_MISMATCH", infraerrors.Reason(err))
}
