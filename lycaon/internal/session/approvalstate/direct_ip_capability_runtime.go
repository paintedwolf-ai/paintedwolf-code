package approvalstate

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// maxDirectIPRunGrants bounds the unnamed leases one chat derives while commands run.
const maxDirectIPRunGrants = 32

// DirectIPCapabilityRuntime holds per-call permits and chat leases.
type DirectIPCapabilityRuntime struct {
	guard sandboxAskGuard[DirectIPChatGrant]
	// mu guards permits only; the guard has its own lock for leases.
	mu      sync.Mutex
	permits scopedstore.LRU[*directIPPermitSlot]
}

// DirectIPChatGrant is one direct-IP lease.
type DirectIPChatGrant struct {
	ID                    string
	ActionDigest          string
	RequestDigest         string
	ConfinementDigest     string
	ChatConfinementDigest string
	DeclaredDestinations  []string
	CommandSummary        string
	CreatedAt             time.Time
	// ExpiresAt bounds the lease; nil means the chat's lifetime.
	ExpiresAt          *time.Time
	SourceCheckpointID string
}

// expired reports whether the lease's time bound has passed.
func (tg DirectIPChatGrant) expired(now time.Time) bool {
	return tg.ExpiresAt != nil && !tg.ExpiresAt.After(now)
}

func (tg DirectIPChatGrant) grantID() string           { return tg.ID }
func (tg DirectIPChatGrant) grantCheckpointID() string { return tg.SourceCheckpointID }

func (tg DirectIPChatGrant) identityKey() string {
	return hitl.DirectIPLease{
		ActionDigest:      tg.ActionDigest,
		RequestDigest:     tg.RequestDigest,
		ConfinementDigest: tg.ConfinementDigest,
	}.IdentityKey()
}

var (
	errDirectIPPermitMismatch = errors.New("session: direct IP permit mismatch")
)

// NewDirectIPCapabilityRuntime constructs empty direct-IP capability state.
func NewDirectIPCapabilityRuntime() *DirectIPCapabilityRuntime {
	return &DirectIPCapabilityRuntime{
		guard: sandboxAskGuard[DirectIPChatGrant]{overlayCap: maxDirectIPRunGrants},
	}
}

// GrantChat records a direct-IP lease after human approval.
func (r *DirectIPCapabilityRuntime) GrantChat(chatSessionID string, lease hitl.DirectIPLease, grantID, checkpointID string, expiresAt *time.Time) bool {
	if r == nil || strings.TrimSpace(grantID) == "" {
		return false
	}
	if !lease.Complete() && strings.TrimSpace(lease.ChatConfinementDigest) == "" {
		return false
	}
	grantID = strings.TrimSpace(grantID)
	now := time.Now().UTC()
	return r.guard.mergeGrants(chatSessionID, func(existing []DirectIPChatGrant) ([]DirectIPChatGrant, bool) {
		next := make([]DirectIPChatGrant, 0, len(existing)+1)
		for _, prior := range existing {
			if prior.ID != grantID {
				next = append(next, prior)
				continue
			}
			if !prior.expired(now) {
				return nil, false
			}
		}
		return append(next, DirectIPChatGrant{
			ID:                    grantID,
			ActionDigest:          strings.TrimSpace(lease.ActionDigest),
			RequestDigest:         strings.TrimSpace(lease.RequestDigest),
			ConfinementDigest:     strings.TrimSpace(lease.ConfinementDigest),
			ChatConfinementDigest: strings.TrimSpace(lease.ChatConfinementDigest),
			DeclaredDestinations:  append([]string(nil), lease.DeclaredDestinations...),
			CommandSummary:        strings.TrimSpace(lease.CommandSummary),
			CreatedAt:             now,
			ExpiresAt:             expiresAt,
			SourceCheckpointID:    strings.TrimSpace(checkpointID),
		}), true
	})
}

// LeaseCovers reports whether a live lease already authorizes this action.
func (r *DirectIPCapabilityRuntime) LeaseCovers(chatSessionID string, lease hitl.DirectIPLease) bool {
	if r == nil {
		return false
	}
	if !lease.Complete() && strings.TrimSpace(lease.ChatConfinementDigest) == "" {
		return false
	}
	chatKey := strings.TrimSpace(lease.ChatConfinementDigest)
	exactKey := lease.IdentityKey()
	now := time.Now().UTC()
	covered := false
	r.guard.readGrants(chatSessionID, func(grants []DirectIPChatGrant) {
		for _, tg := range grants {
			if tg.expired(now) {
				continue
			}
			if chatKey != "" && strings.TrimSpace(tg.ChatConfinementDigest) == chatKey {
				covered = true
				return
			}
			if exactKey != "" && tg.identityKey() == exactKey {
				covered = true
				return
			}
		}
	})
	return covered
}

// ListChatGrants includes expired rows for review.
func (r *DirectIPCapabilityRuntime) ListChatGrants(chatSessionID string) []DirectIPChatGrant {
	if r == nil {
		return nil
	}
	out := r.guard.listGrants(chatSessionID)
	if len(out) == 0 {
		return nil
	}
	return out
}

// ListAllChatGrants groups lease records by chat.
func (r *DirectIPCapabilityRuntime) ListAllChatGrants() map[string][]DirectIPChatGrant {
	if r == nil {
		return nil
	}
	return r.guard.listAllGrants()
}

// FindByID locates a lease by opaque approval-option id.
func (r *DirectIPCapabilityRuntime) FindByID(id string) (DirectIPChatGrant, string, bool) {
	if r == nil {
		return DirectIPChatGrant{}, "", false
	}
	return r.guard.findByID(id)
}

// RevokeByID removes one lease by opaque id. Idempotent. Revocation applies to the
// next process start; it cannot retract a permit already consumed at spawn.
func (r *DirectIPCapabilityRuntime) RevokeByID(id string) (DirectIPChatGrant, bool) {
	return r.revokeByIDInstalledBy(id, "")
}

// RevokeByIDInstalledBy removes a chat grant only when its installing checkpoint still identifies it.
func (r *DirectIPCapabilityRuntime) RevokeByIDInstalledBy(id, checkpointID string) (DirectIPChatGrant, bool) {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return DirectIPChatGrant{}, false
	}
	return r.revokeByIDInstalledBy(id, checkpointID)
}

func (r *DirectIPCapabilityRuntime) revokeByIDInstalledBy(id, checkpointID string) (DirectIPChatGrant, bool) {
	if r == nil {
		return DirectIPChatGrant{}, false
	}
	return r.guard.revokeByID(id, checkpointID)
}

// IssuePermit mints an ephemeral current-call permit.
func (r *DirectIPCapabilityRuntime) IssuePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) {
	if r == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	actionDigest = strings.TrimSpace(actionDigest)
	requestDigest = strings.TrimSpace(requestDigest)
	confinementDigest = strings.TrimSpace(confinementDigest)
	if sessionID == "" || toolCallID == "" || actionDigest == "" || requestDigest == "" || confinementDigest == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slotKey := directIPPermitKey(sessionID, toolCallID, actionDigest)
	r.permits.Store(slotKey, &directIPPermitSlot{
		requestDigest:     requestDigest,
		confinementDigest: confinementDigest,
	})
}

// RevokePermit removes an unconsumed current-action permit during atomic
// approval rollback.
func (r *DirectIPCapabilityRuntime) RevokePermit(sessionID, toolCallID, actionDigest string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permits.Delete(directIPPermitKey(strings.TrimSpace(sessionID), strings.TrimSpace(toolCallID), strings.TrimSpace(actionDigest)))
}

// ConsumePermit atomically consumes a current-call permit once for the invocation.
func (r *DirectIPCapabilityRuntime) ConsumePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) (bool, error) {
	if r == nil {
		return false, errDirectIPPermitMismatch
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	actionDigest = strings.TrimSpace(actionDigest)
	requestDigest = strings.TrimSpace(requestDigest)
	confinementDigest = strings.TrimSpace(confinementDigest)
	if sessionID == "" || toolCallID == "" || actionDigest == "" || requestDigest == "" || confinementDigest == "" {
		return false, errDirectIPPermitMismatch
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slotKey := directIPPermitKey(sessionID, toolCallID, actionDigest)
	slot, ok := r.permits.LoadAndDelete(slotKey)
	if !ok {
		return false, nil
	}
	if slot.requestDigest != requestDigest || slot.confinementDigest != confinementDigest {
		return false, errDirectIPPermitMismatch
	}
	return true, nil
}

// Authorized reports whether an unconsumed permit exists for this invocation.
func (r *DirectIPCapabilityRuntime) Authorized(sessionID, toolCallID, actionDigest string) bool {
	if r == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	actionDigest = strings.TrimSpace(actionDigest)
	if sessionID == "" || toolCallID == "" || actionDigest == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slot, ok := r.permits.Load(directIPPermitKey(sessionID, toolCallID, actionDigest))
	return ok && slot != nil
}

func (r *DirectIPCapabilityRuntime) forgetPermitsLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "_"
	}
	prefix := sessionID + "\x01"
	for key := range r.permits.Snapshot() {
		if strings.HasPrefix(key, prefix) || key == sessionID {
			r.permits.Delete(key)
		}
	}
}

// ReleaseRun clears per-call permits and open reviews; approved leases last for the chat.
func (r *DirectIPCapabilityRuntime) ReleaseRun(rootSessionID string) {
	if r == nil {
		return
	}
	r.guard.releaseRun(rootSessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forgetPermitsLocked(rootSessionID)
}

// ForgetSession clears leases before per-call permits to narrow concurrent reads first.
func (r *DirectIPCapabilityRuntime) ForgetSession(rootSessionID string) {
	if r == nil {
		return
	}
	r.guard.forgetSession(rootSessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forgetPermitsLocked(rootSessionID)
}

type directIPPermitSlot struct {
	requestDigest     string
	confinementDigest string
}

func directIPPermitKey(sessionID, toolCallID, actionDigest string) string {
	return sessionID + "\x01" + toolCallID + "\x01" + actionDigest
}
