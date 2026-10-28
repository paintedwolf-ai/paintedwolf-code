package approvalstate

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/socketref"
)

// RestoreChatIntent restores saved intent without acquiring an object reference.
// A supported kernel pathname rule remains reusable when its name is valid.
func (r *SocketCapabilityRuntime) RestoreChatIntent(rootSessionID string, record SocketChatGrant) bool {
	if r == nil || record.Binding.Mode() != socketref.RetainedObject && record.Binding.Mode() != socketref.KernelPathname && record.Binding.Mode() != socketref.NoRealization {
		return false
	}
	record.ID = strings.TrimSpace(record.ID)
	if record.ID == "" || record.ApprovedPath == "" || record.ResolvedPath == "" {
		return false
	}
	record.ExpiresAt = cloneSocketExpiry(record.ExpiresAt)
	record.resource = nil
	record.Unavailable = record.Binding.Mode() != socketref.KernelPathname || socketref.Platform().Command != socketref.KernelPathname
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	key := socketGrantPairKey(confine.SocketGrant{ApprovedPath: record.ApprovedPath, ResolvedPath: record.ResolvedPath, Binding: record.Binding})
	return r.guard.mergeGrants(rootSessionID, func(existing []SocketChatGrant) ([]SocketChatGrant, bool) {
		for _, prior := range existing {
			if prior.ID == record.ID && socketGrantPairKey(confine.SocketGrant{ApprovedPath: prior.ApprovedPath, ResolvedPath: prior.ResolvedPath, Binding: prior.Binding}) == key {
				return nil, false
			}
		}
		return append(append([]SocketChatGrant(nil), existing...), record), true
	})
}

func (r *SocketCapabilityRuntime) scheduleSocketExpiryLocked() {
	if r.expiry != nil {
		r.expiry.Stop()
		r.expiry = nil
	}
	var next time.Time
	for _, grants := range r.guard.listAllGrants() {
		for _, grant := range grants {
			if grant.resource != nil && grant.ExpiresAt != nil && (next.IsZero() || grant.ExpiresAt.Before(next)) {
				next = *grant.ExpiresAt
			}
		}
	}
	if !next.IsZero() {
		r.expiry = time.AfterFunc(time.Until(next), func() { r.expireSocketOwners(time.Now()) })
	}
}

func (r *SocketCapabilityRuntime) expireSocketOwners(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for root := range r.guard.listAllGrants() {
		r.guard.mergeGrants(root, func(existing []SocketChatGrant) ([]SocketChatGrant, bool) {
			next := append([]SocketChatGrant(nil), existing...)
			for i := range next {
				if next[i].expired(now) {
					next[i].resource.Close()
					next[i].resource = nil
				}
			}
			return next, true
		})
	}
	r.scheduleSocketExpiryLocked()
}

// Close releases runtime capabilities; durable history belongs to its journal.
func (r *SocketCapabilityRuntime) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.expiry != nil {
		r.expiry.Stop()
		r.expiry = nil
	}
	for root, grants := range r.guard.listAllGrants() {
		for _, grant := range grants {
			grant.resource.Close()
		}
		r.guard.forgetSession(root)
	}
	for _, slot := range r.permits.Snapshot() {
		slot.resource.Close()
	}
	r.permits.Clear()
}

func cloneSocketExpiry(expiry *time.Time) *time.Time {
	if expiry == nil {
		return nil
	}
	value := *expiry
	return &value
}

func cloneSocketGrants(grants []SocketChatGrant) []SocketChatGrant {
	out := append([]SocketChatGrant(nil), grants...)
	for i := range out {
		out[i].ExpiresAt = cloneSocketExpiry(out[i].ExpiresAt)
		out[i].resource = nil
	}
	return out
}

// retireSocketOwners preserves the recorded intent after a failed live-object check.
func (r *SocketCapabilityRuntime) retireSocketOwners(root string, stale map[string]string) {
	if len(stale) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guard.mergeGrants(root, func(existing []SocketChatGrant) ([]SocketChatGrant, bool) {
		next := append([]SocketChatGrant(nil), existing...)
		for i := range next {
			if key, ok := stale[next[i].ID]; ok && key == next[i].Binding.Key() {
				next[i].resource.Close()
				next[i].resource = nil
				next[i].Unavailable = true
			}
		}
		return next, true
	})
	r.scheduleSocketExpiryLocked()
}
