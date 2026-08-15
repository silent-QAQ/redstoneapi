package controlledaccount

import (
	"strings"
	"sync"
	"time"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
)

const oauthBindingTTL = 15 * time.Minute

type oauthBindingStore struct {
	mu      sync.Mutex
	entries map[string]oauthBindingEntry
}

type oauthBindingEntry struct {
	UserID    int64
	State     string
	ExpiresAt time.Time
}

func newOAuthBindingStore() *oauthBindingStore {
	return &oauthBindingStore{
		entries: make(map[string]oauthBindingEntry),
	}
}

func (s *oauthBindingStore) Bind(sessionID, state string, userID int64) {
	s.BindProvider("", sessionID, state, userID)
}

func (s *oauthBindingStore) BindProvider(provider, sessionID, state string, userID int64) {
	if s == nil || sessionID == "" || userID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(time.Now())
	s.entries[oauthBindingKey(provider, sessionID)] = oauthBindingEntry{
		UserID:    userID,
		State:     state,
		ExpiresAt: time.Now().Add(oauthBindingTTL),
	}
}

func (s *oauthBindingStore) Consume(sessionID, state string, userID int64) error {
	return s.ConsumeProvider("", sessionID, state, userID)
}

func (s *oauthBindingStore) ConsumeProvider(provider, sessionID, state string, userID int64) error {
	if s == nil || sessionID == "" {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(now)

	bindingKey := oauthBindingKey(provider, sessionID)
	entry, ok := s.entries[bindingKey]
	if !ok {
		return infraerrors.Forbidden("USER_ACCOUNT_OAUTH_BINDING_MISSING", "oauth session is missing, expired, or already used")
	}

	if entry.UserID != userID || userID <= 0 {
		return infraerrors.Forbidden("USER_ACCOUNT_OAUTH_BINDING_MISMATCH", "oauth session does not belong to the current user")
	}
	if entry.State != "" && entry.State != state {
		return infraerrors.Forbidden("USER_ACCOUNT_OAUTH_STATE_MISMATCH", "oauth state does not match the current session")
	}
	// A caller that does not own the binding must not invalidate the owner's
	// pending exchange. Successful validation consumes the session exactly once.
	delete(s.entries, bindingKey)
	return nil
}

func oauthBindingKey(provider, sessionID string) string {
	return strings.TrimSpace(provider) + ":" + sessionID
}

func (s *oauthBindingStore) gcLocked(now time.Time) {
	for sessionID, entry := range s.entries {
		if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
			delete(s.entries, sessionID)
		}
	}
}
