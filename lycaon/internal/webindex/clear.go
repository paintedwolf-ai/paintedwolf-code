package webindex

import (
	"context"
	"database/sql"
	"fmt"
)

// Clear removes every indexed page, anchor, warming record, and quota counter.
// The database file stays open; later searches rebuild the cache from scratch.
func (s *Store) Clear(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// The eviction counter is plain writer-goroutine state, so reset it inside the
	// queued op — resetting it from the caller races the writer's own increment.
	if err := s.syncWrite(ctx, func(db *sql.DB) error {
		if err := clearAllTables(ctx, db); err != nil {
			return err
		}
		s.writes = 0
		return nil
	}); err != nil {
		return err
	}
	s.writesApplied.Store(0)
	s.writesDropped.Store(0)
	s.queueHighWater.Store(0)
	s.dropWarned.Store(false)
	s.lastEvictMs.Store(0)
	s.lastEvictRows.Store(0)
	s.lastEvictBytes.Store(0)
	s.lastSearchMs.Store(0)
	s.maxSearchMs.Store(0)
	return nil
}

func clearAllTables(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		"DELETE FROM docs_fts",
		"DELETE FROM anchors",
		"DELETE FROM docs",
		"DELETE FROM warm_activity",
		"DELETE FROM warm_state",
		"DELETE FROM search_outcomes",
		"DELETE FROM provider_quota",
		"DELETE FROM stats",
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("clear web index: %w", err)
		}
	}
	if err := vacuumAndTruncate(ctx, db); err != nil {
		return fmt.Errorf("clear web index: %w", err)
	}
	return nil
}
