package hitl

import (
	"context"
	"sync"
	"time"
)

// Checkpoints owns checkpoint creation, resolution serialization, and expiry.
type Checkpoints struct {
	Store           Store
	Authority       *ApprovalAuthority
	Presence        *VaultPresence
	Sessions        *SessionCoupling
	events          EventPublisher
	expiryFor       func() time.Duration
	resolutionLocks keyedMutex
	expiries        expiryTimers
}

type checkpointSettlement interface {
	LockResolution(string) func()
	announceResolved(context.Context, StoredCheckpoint, bool)
	settlePending(context.Context, string, DecisionStatus, DecisionResult, Resolution) error
}

// NewCheckpoints assembles the services sharing one durable checkpoint journal.
func NewCheckpoints(store Store, events EventPublisher, authz AuthzRecorder) *Checkpoints {
	if store == nil {
		panic("hitl: store is required")
	}
	if authz == nil {
		panic("hitl: authz recorder is required")
	}
	c := &Checkpoints{Store: store, events: events, expiryFor: func() time.Duration { return DefaultCheckpointTimeout }}
	c.Sessions = &SessionCoupling{store: store}
	c.Presence = &VaultPresence{store: store, checkpoints: c}
	c.Authority = &ApprovalAuthority{store: store, authzRecorder: authz, checkpoints: c, sessions: c.Sessions, presence: c.Presence}
	return c
}

func (c *Checkpoints) LockResolution(checkpointID string) func() {
	return c.resolutionLocks.Lock(checkpointID)
}

type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	refs int
	mu   sync.Mutex
}

func (k *keyedMutex) Lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.entries == nil {
		k.entries = map[string]*keyedMutexEntry{}
	}
	e := k.entries[key]
	if e == nil {
		e = &keyedMutexEntry{}
		k.entries[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}
