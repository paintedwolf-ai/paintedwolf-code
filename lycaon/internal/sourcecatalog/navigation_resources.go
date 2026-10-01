package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
)

// navigationResources reuses one confined root descriptor. Active filesystem
// readers pin it across catalog eviction and drain; structural readers pin their
// immutable generation separately.
type navigationResources struct {
	root    *os.Root
	users   int
	idle    chan struct{}
	closing bool
	retired bool
}

func (s *indexStore) acquireNavigation(ctx context.Context) (*os.Root, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	resources := &s.navigation
	if resources.closing || resources.retired {
		return nil, nil, errors.New("source navigation is draining")
	}
	if resources.root == nil {
		root, err := os.OpenRoot(s.root.Path)
		if err != nil {
			return nil, nil, err
		}
		resources.root = root
	}
	if resources.users == 0 {
		resources.idle = make(chan struct{})
	}
	resources.users++
	var once sync.Once
	return resources.root, func() { once.Do(s.releaseNavigation) }, nil
}

func (s *indexStore) releaseNavigation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	resources := &s.navigation
	resources.users--
	if resources.users == 0 {
		if resources.closing || resources.retired {
			s.closeNavigationLocked()
		}
		close(resources.idle)
	}
}

func (s *indexStore) closeNavigationLocked() {
	resources := &s.navigation
	if resources.root != nil {
		_ = resources.root.Close()
	}
	resources.root = nil
	resources.closing = false
}

func (s *indexStore) drainNavigationLocked() <-chan struct{} {
	resources := &s.navigation
	if resources.users == 0 {
		s.closeNavigationLocked()
		return nil
	}
	resources.closing = true
	return resources.idle
}

func (s *indexStore) acquireIndex(ctx context.Context) (*sql.DB, *os.Root, func(), error) {
	db, err := openTreeDB(ctx, s.file)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := db.ExecContext(ctx, indexSchema); err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	root, err := os.OpenRoot(s.root.Path)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	return db, root, func() { _ = root.Close(); _ = db.Close() }, nil
}
