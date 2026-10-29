package settings

import (
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/socketref"
)

type socketGrantLease struct {
	lease     *socketref.Lease
	expiresAt *time.Time
}

var errApprovalStoreClosed = errors.New("approval store is closed")

// prepareSocketOwners acquires only authority already owned by this store or
// explicitly being installed. Loading a config file never takes this route.
func (s *ApprovalStore) prepareSocketOwners(cfg ApprovalConfig, incoming []*socketref.Lease) map[string]socketGrantLease {
	allowed := make(map[string]bool)
	s.mu.RLock()
	for _, owner := range s.socketLeases {
		allowed[owner.lease.Binding().Key()] = true
	}
	s.mu.RUnlock()
	for _, owner := range incoming {
		if owner != nil {
			allowed[owner.Binding().Key()] = true
		}
	}
	next := make(map[string]socketGrantLease)
	for _, grant := range cfg.Grants {
		if grant.Category != ApprovalCategorySocketPath || grant.Binding.Mode() != socketref.RetainedObject || !allowed[grant.Binding.Key()] {
			continue
		}
		g := confine.SocketGrant{ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Binding: grant.Binding}
		if confine.RevalidateSocketGrant(g) != nil {
			continue
		}
		lease, err := grant.Binding.Acquire()
		if err == nil {
			next[grant.ID] = socketGrantLease{lease: lease, expiresAt: copySocketExpiry(grant.ExpiresAt)}
		}
	}
	return next
}

func closeSocketOwners(owners map[string]socketGrantLease) {
	for _, owner := range owners {
		owner.lease.Close()
	}
}

// scheduleSocketExpiryLocked is called with mu held after an ownership change.
func (s *ApprovalStore) scheduleSocketExpiryLocked() {
	if s.socketTimer != nil {
		s.socketTimer.Stop()
		s.socketTimer = nil
	}
	var deadline time.Time
	for _, owner := range s.socketLeases {
		if owner.expiresAt != nil && (deadline.IsZero() || owner.expiresAt.Before(deadline)) {
			deadline = *owner.expiresAt
		}
	}
	if !deadline.IsZero() {
		s.socketTimer = time.AfterFunc(time.Until(deadline), func() { s.expireSocketOwners(time.Now()) })
	}
}

func (s *ApprovalStore) expireSocketOwners(now time.Time) {
	s.mu.Lock()
	var expired []*socketref.Lease
	for id, owner := range s.socketLeases {
		if owner.expiresAt != nil && !owner.expiresAt.After(now) {
			expired = append(expired, owner.lease)
			delete(s.socketLeases, id)
		}
	}
	s.scheduleSocketExpiryLocked()
	s.mu.Unlock()
	for _, lease := range expired {
		lease.Close()
	}
}

// Close releases live capabilities while leaving every saved grant unchanged.
func (s *ApprovalStore) Close() {
	if s == nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	s.closed = true
	owners := s.socketLeases
	s.socketLeases = nil
	if s.socketTimer != nil {
		s.socketTimer.Stop()
		s.socketTimer = nil
	}
	s.mu.Unlock()
	closeSocketOwners(owners)
}

func copySocketExpiry(expiry *time.Time) *time.Time {
	if expiry == nil {
		return nil
	}
	value := *expiry
	return &value
}

func (s *ApprovalStore) retireSocketOwners(stale map[string]string) {
	if len(stale) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, key := range stale {
		if owner, ok := s.socketLeases[id]; ok && owner.lease.Binding().Key() == key {
			delete(s.socketLeases, id)
			owner.lease.Close()
		}
	}
	s.scheduleSocketExpiryLocked()
}
