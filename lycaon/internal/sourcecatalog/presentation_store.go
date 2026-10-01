package sourcecatalog

import (
	"context"
	"database/sql"
	"sync"
)

// Roots and views share one temporary pager and its resident cache.
type presentationStore struct {
	mu    sync.Mutex
	db    *sql.DB
	users int
}

func (c *Catalog) acquirePresentation(ctx context.Context) (*sql.DB, func(), error) {
	store := &c.presentations
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.db == nil {
		db, err := sql.Open("sqlite", "")
		if err != nil {
			return nil, nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		// The pager disappears on process exit; rollback still protects live transactions.
		_, err = db.ExecContext(ctx, `PRAGMA page_size=4096; PRAGMA cache_size=-2048; PRAGMA synchronous=OFF;`+rangeSchema+projectionSchema+treeOverlaySchema)
		if err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		store.db = db
	}
	store.users++
	var once sync.Once
	return store.db, func() {
		once.Do(func() {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.users--
			if store.users == 0 {
				_ = store.db.Close()
				store.db = nil
			}
		})
	}, nil
}
