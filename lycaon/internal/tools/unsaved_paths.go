package tools

import (
	"context"
	"sync"
)

type unsavedPathsKey struct{}

// UnsavedPaths remembers, per root, which paths held unsaved text when a
// program first read that root, so a walk over many files checks a map
// instead of asking the document store for each one.
type UnsavedPaths struct {
	mu    sync.Mutex
	roots map[string]map[string]bool
}

// WithUnsavedPaths binds one program's view of which paths hold unsaved text.
func WithUnsavedPaths(ctx context.Context) context.Context {
	return context.WithValue(ctx, unsavedPathsKey{}, &UnsavedPaths{roots: map[string]map[string]bool{}})
}

// ProgramUnsavedPaths returns the program's view; ok is false outside a program.
func ProgramUnsavedPaths(ctx context.Context) (*UnsavedPaths, bool) {
	paths, ok := ctx.Value(unsavedPathsKey{}).(*UnsavedPaths)
	return paths, ok
}

// Holds reports whether path in root held unsaved text, loading the root's
// paths through load the first time the root is asked about.
func (u *UnsavedPaths) Holds(rootID, path string, load func() (map[string]bool, error)) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	paths, loaded := u.roots[rootID]
	if !loaded {
		var err error
		if paths, err = load(); err != nil {
			return false, err
		}
		u.roots[rootID] = paths
	}
	return paths[path], nil
}
