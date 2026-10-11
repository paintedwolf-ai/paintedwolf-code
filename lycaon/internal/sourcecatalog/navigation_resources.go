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
	mu      sync.Mutex
	root    *os.Root
	users   int
	idle    chan struct{}
	retired bool
}

func (n *navigationResources) Acquire(ctx context.Context, path string) (*os.Root, func(), error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if n.retired {
		return nil, nil, errors.New("source navigation is draining")
	}
	if n.root == nil {
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, nil, err
		}
		n.root = root
	}
	if n.users == 0 {
		n.idle = make(chan struct{})
	}
	n.users++
	var once sync.Once
	return n.root, func() { once.Do(n.release) }, nil
}

func (n *navigationResources) release() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.users--
	if n.users == 0 {
		if n.retired {
			n.closeLocked()
		}
		close(n.idle)
	}
}

func (n *navigationResources) closeLocked() {
	if n.root != nil {
		_ = n.root.Close()
	}
	n.root = nil
}

func (n *navigationResources) Drain() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.retired = true
	if n.users == 0 {
		n.closeLocked()
		return nil
	}
	return n.idle
}

func (n *navigationResources) Retired() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.retired
}

func (n *navigationResources) Active() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.users > 0
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
