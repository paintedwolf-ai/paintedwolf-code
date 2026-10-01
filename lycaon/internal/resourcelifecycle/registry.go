// Package resourcelifecycle releases resources by architectural scope.
package resourcelifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ScopeKind names an architectural scope that owns resources.
type ScopeKind string

const (
	ScopeDevice  ScopeKind = "device"
	ScopeSession ScopeKind = "session"
)

// Scope is one concrete instance of a scope kind.
type Scope struct {
	Kind ScopeKind
	ID   string
}

// DeviceScope returns the single device scope.
func DeviceScope() Scope { return Scope{Kind: ScopeDevice, ID: "device"} }

// SessionScope returns the scope for one session ID.
func SessionScope(id string) Scope { return Scope{Kind: ScopeSession, ID: strings.TrimSpace(id)} }

// Cleanup releases what a rule or tracked resource holds for scope.
type Cleanup func(context.Context, Scope) error

type entry struct {
	name    string
	order   int
	cleanup Cleanup
	tracked bool
	// disposalOnly runs only when the scope is disposed, not on each release.
	disposalOnly bool
}

type scopeState struct {
	gate      sync.Mutex
	refs      int
	tracked   map[string]entry
	disposed  bool
	forgotten bool
}

// Registry coordinates cleanup rules and tracked scoped resources.
type Registry struct {
	mu     sync.Mutex
	kinds  map[ScopeKind]map[string]entry
	scopes map[Scope]*scopeState
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		kinds:  make(map[ScopeKind]map[string]entry),
		scopes: make(map[Scope]*scopeState),
	}
}

// Register applies a cleanup rule to every release of one scope kind.
func (r *Registry) Register(kind ScopeKind, name string, order int, cleanup Cleanup) error {
	return r.register(kind, entry{name: name, order: order, cleanup: cleanup})
}

// RegisterDisposal applies a cleanup rule only when a scope of this kind is
// disposed, for state that outlives a release.
func (r *Registry) RegisterDisposal(kind ScopeKind, name string, order int, cleanup Cleanup) error {
	return r.register(kind, entry{name: name, order: order, cleanup: cleanup, disposalOnly: true})
}

func (r *Registry) register(kind ScopeKind, rule entry) error {
	if r == nil || strings.TrimSpace(string(kind)) == "" || strings.TrimSpace(rule.name) == "" || rule.cleanup == nil {
		return fmt.Errorf("resource cleanup needs kind, name, and function")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kinds[kind] == nil {
		r.kinds[kind] = make(map[string]entry)
	}
	r.kinds[kind][rule.name] = rule
	return nil
}

// Track records one resource under an exact scope.
func (r *Registry) Track(scope Scope, name string, order int, cleanup Cleanup) error {
	if r == nil || strings.TrimSpace(string(scope.Kind)) == "" || strings.TrimSpace(scope.ID) == "" || strings.TrimSpace(name) == "" || cleanup == nil {
		return fmt.Errorf("tracked resource needs scope, name, and function")
	}
	state := r.acquireScope(scope)
	state.gate.Lock()
	if state.disposed {
		removable := state.forgotten
		state.gate.Unlock()
		r.releaseScope(scope, state, removable)
		return fmt.Errorf("scope already disposed: %s/%s", scope.Kind, scope.ID)
	}
	if state.tracked == nil {
		state.tracked = make(map[string]entry)
	}
	state.tracked[name] = entry{name: name, order: order, cleanup: cleanup, tracked: true}
	state.gate.Unlock()
	r.releaseScope(scope, state, false)
	return nil
}

// Release runs cleanup rules while leaving the scope reusable.
func (r *Registry) Release(ctx context.Context, scope Scope) error {
	return r.release(ctx, scope, false)
}

// Dispose runs cleanup rules and permanently closes the scope.
func (r *Registry) Dispose(ctx context.Context, scope Scope) error {
	return r.release(ctx, scope, true)
}

// ForgetDisposed releases a tombstone after its durable parent is deleted.
func (r *Registry) ForgetDisposed(scope Scope) error {
	if r == nil {
		return nil
	}
	state := r.acquireScope(scope)
	state.gate.Lock()
	if !state.disposed {
		removable := len(state.tracked) == 0
		state.gate.Unlock()
		r.releaseScope(scope, state, removable)
		return fmt.Errorf("scope is not disposed: %s/%s", scope.Kind, scope.ID)
	}
	state.forgotten = true
	state.gate.Unlock()
	r.releaseScope(scope, state, true)
	return nil
}

func (r *Registry) release(ctx context.Context, scope Scope, terminal bool) error {
	if r == nil {
		return nil
	}
	state := r.acquireScope(scope)
	state.gate.Lock()
	defer func() {
		removable := state.forgotten || (!state.disposed && len(state.tracked) == 0)
		state.gate.Unlock()
		r.releaseScope(scope, state, removable)
	}()
	r.mu.Lock()
	entries := make([]entry, 0, len(r.kinds[scope.Kind])+len(state.tracked))
	for _, candidate := range r.kinds[scope.Kind] {
		if candidate.disposalOnly && !terminal {
			continue
		}
		entries = append(entries, candidate)
	}
	r.mu.Unlock()
	if state.disposed {
		return nil
	}
	for _, candidate := range state.tracked {
		entries = append(entries, candidate)
	}
	state.tracked = nil

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].order == entries[j].order {
			return entries[i].name < entries[j].name
		}
		return entries[i].order < entries[j].order
	})
	var errs []error
	failedTracked := make([]entry, 0)
	for _, candidate := range entries {
		if err := candidate.cleanup(ctx, scope); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidate.name, err))
			if candidate.tracked {
				failedTracked = append(failedTracked, candidate)
			}
		}
	}
	if len(errs) == 0 && terminal {
		state.disposed = true
	}
	if len(failedTracked) > 0 {
		if state.tracked == nil {
			state.tracked = make(map[string]entry)
		}
		for _, candidate := range failedTracked {
			state.tracked[candidate.name] = candidate
		}
	}
	return errors.Join(errs...)
}

func (r *Registry) acquireScope(scope Scope) *scopeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.scopes[scope]
	if state == nil {
		state = &scopeState{}
		r.scopes[scope] = state
	}
	state.refs++
	return state
}

func (r *Registry) releaseScope(scope Scope, state *scopeState, removable bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state.refs--
	if state.refs == 0 && removable && r.scopes[scope] == state {
		delete(r.scopes, scope)
	}
}
