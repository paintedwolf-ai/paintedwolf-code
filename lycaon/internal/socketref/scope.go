package socketref

import (
	"context"
	"sync"
)

// Scope owns transient references through one invocation, including its approval
// wait and repeated capability preflight. Accepted grants acquire their own share.
type Scope struct {
	mu       sync.Mutex
	closed   bool
	paths    map[string]*Lease
	retained map[string]*Lease
}

type scopeKey struct{}

// WithScope starts a new invocation owner. The caller always closes it on return.
func WithScope(ctx context.Context) (context.Context, *Scope) {
	scope := &Scope{paths: make(map[string]*Lease), retained: make(map[string]*Lease)}
	return context.WithValue(ctx, scopeKey{}, scope), scope
}

// RetainForInvocation acquires the operation's share before a one-use permit is
// consumed. It retains existing authority and never opens a pathname.
func RetainForInvocation(ctx context.Context, binding Binding) error {
	if binding.Mode() == KernelPathname {
		return nil
	}
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	if scope == nil {
		return ErrUnbound
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.closed {
		return ErrUnbound
	}
	for _, owner := range scope.paths {
		if owner.Binding().Key() == binding.Key() {
			return nil
		}
	}
	if scope.retained[binding.Key()] != nil {
		return nil
	}
	owner, err := binding.Acquire()
	if err != nil {
		return err
	}
	scope.retained[binding.Key()] = owner
	return nil
}

// CaptureForInvocation retains the first destination reviewed in this invocation.
// Repeated preflight cannot replace its binding with a later pathname lookup.
func CaptureForInvocation(ctx context.Context, path string) (Binding, error) {
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	if scope == nil {
		return Binding{}, ErrUnbound
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.closed {
		return Binding{}, ErrUnbound
	}
	if prior := scope.paths[path]; prior != nil {
		return prior.Binding(), nil
	}
	lease, err := Capture(path)
	if err != nil {
		return Binding{}, err
	}
	scope.paths[path] = lease
	return lease.Binding(), nil
}

// ReuseForInvocation adopts existing authority only when the invocation captured
// the same still-retained object. Matching path strings alone cannot adopt it.
func ReuseForInvocation(ctx context.Context, path string, existing Binding) bool {
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	if scope == nil {
		return false
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	prior := scope.paths[path]
	if scope.closed || prior == nil || !prior.Binding().SameObject(existing) {
		return false
	}
	if prior.Binding() == existing {
		return true
	}
	next, err := existing.Acquire()
	if err != nil {
		return false
	}
	scope.paths[path] = next
	prior.Close()
	return true
}

// Close releases every transient reference without waiting for garbage collection.
func (s *Scope) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for path, lease := range s.paths {
		lease.Close()
		delete(s.paths, path)
	}
	for id, lease := range s.retained {
		lease.Close()
		delete(s.retained, id)
	}
}
