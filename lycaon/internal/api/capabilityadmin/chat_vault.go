package capabilityadmin

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/presence"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ChatVaultState is a chat's unlock state on the wire.
func ChatVaultState(chatSessionID string, unlocks *presence.Unlocks) wire.ChatVault {
	state := wire.ChatVault{ChatSessionID: chatSessionID}
	unlock, open := unlocks.Active(chatSessionID)
	if !open {
		return state
	}
	state.Unlocked = true
	state.UnlockedAt = unlock.UnlockedAt.UTC().Format(time.RFC3339Nano)
	state.ClosesAt = unlock.ClosesAt().UTC().Format(time.RFC3339Nano)
	state.ExpiresAt = unlock.ExpiresAt().UTC().Format(time.RFC3339Nano)
	return state
}

// HandleGetChatVault reports whether a chat, or the chat a worker belongs
// to, is unlocked.
func (s *Handler) HandleGetChatVault(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.elevatedAccessChat(w, r)
	if !ok {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, ChatVaultState(chat.ID, s.Vault))
}

// HandleLockChatVault ends a chat's unlock at the person's request.
func (s *Handler) HandleLockChatVault(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.elevatedAccessChat(w, r)
	if !ok {
		return
	}
	s.Vault.Lock(chat.ID, presence.EndManual)
	httpio.WriteJSON(w, http.StatusOK, ChatVaultState(chat.ID, s.Vault))
}

// HandleLockVault ends every chat's unlock when the person steps away from
// the device. Locking only removes authority, so any caller may ask.
func (s *Handler) HandleLockVault(w http.ResponseWriter, r *http.Request) {
	var body wire.LockVaultRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	reason := presence.EndReason(body.Reason)
	if !presence.IsLockReason(reason) || reason == presence.EndManual {
		s.responses.InvalidField(w, "reason", "must be screen_locked, sleep, or app_quit")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.LockVaultResponse{Locked: s.Vault.LockAll(reason)})
}
