package webindex

// Retention bounds the rebuildable index by rows and bytes.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Earned and recent rows survive row-cap eviction first.
const evictKeepOrder = `ORDER BY (origin = 'earned') DESC, updated_at DESC`

// evictDropOrder is the byte-budget drop order: warmed first, oldest first.
const evictDropOrder = `ORDER BY (origin = 'warmed') DESC, updated_at ASC`

// errEvictionStalled reports documents left over budget that a delete pass
// could not remove. Retrying cannot change the outcome.
var errEvictionStalled = errors.New("web index eviction made no progress")

// maybeEvict runs a retention sweep every evictEvery writes.
func (s *Store) maybeEvict(ctx context.Context, db *sql.DB) {
	s.writes++
	if s.writes%evictEvery != 0 {
		return
	}
	if err := s.evictionPass(ctx, db, maxDocs, maxIndexBytes); err != nil {
		logger.Warn("web index eviction pass failed", "error", err)
	}
}

// evictionPass records one eviction and compaction sweep.
func (s *Store) evictionPass(ctx context.Context, db *sql.DB, rowCap int, byteBudget int64) error {
	start := time.Now()
	rowsBefore, err := docCount(ctx, db)
	if err != nil {
		return err
	}
	bytesBefore, err := fileBytes(ctx, db)
	if err != nil {
		return err
	}
	if err := s.evictOverRowCap(ctx, db, rowCap); err != nil {
		return err
	}
	if err := vacuumAndTruncate(ctx, db); err != nil {
		return err
	}
	if err := s.evictOverByteBudget(ctx, db, byteBudget); err != nil {
		return err
	}
	rowsAfter, err := docCount(ctx, db)
	if err != nil {
		return err
	}
	bytesAfter, err := fileBytes(ctx, db)
	if err != nil {
		return err
	}

	ms := time.Since(start).Milliseconds()
	reclaimed := max(bytesBefore-bytesAfter, 0)
	s.lastEvictMs.Store(ms)
	s.lastEvictRows.Store(rowsBefore - rowsAfter)
	s.lastEvictBytes.Store(reclaimed)
	logger.Debug("web index eviction pass",
		"duration_ms", ms, "rows_evicted", rowsBefore-rowsAfter, "bytes_reclaimed", reclaimed)
	return nil
}

// evictOverRowCap drops the documents ranked past rowCap.
func (s *Store) evictOverRowCap(ctx context.Context, db *sql.DB, rowCap int) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM docs_fts WHERE url IN (
		SELECT url FROM docs `+evictKeepOrder+` LIMIT -1 OFFSET ?)`, rowCap); err != nil {
		return fmt.Errorf("evict docs_fts over row cap: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM anchors WHERE url IN (
		SELECT url FROM docs `+evictKeepOrder+` LIMIT -1 OFFSET ?)`, rowCap); err != nil {
		return fmt.Errorf("evict anchors over row cap: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM docs WHERE url IN (
		SELECT url FROM docs `+evictKeepOrder+` LIMIT -1 OFFSET ?)`, rowCap); err != nil {
		return fmt.Errorf("evict docs over row cap: %w", err)
	}
	return nil
}

// evictOverByteBudget stops when the budget is met or eviction stalls.
func (s *Store) evictOverByteBudget(ctx context.Context, db *sql.DB, budget int64) error {
	for {
		if err := vacuumAndTruncate(ctx, db); err != nil {
			return err
		}
		size, err := fileBytes(ctx, db)
		if err != nil {
			return err
		}
		if size <= budget {
			return nil
		}
		remaining, err := docCount(ctx, db)
		if err != nil {
			return err
		}
		if remaining == 0 {
			// Empty database pages can remain above the byte budget.
			return nil
		}
		dropped, err := evictDropChunk(ctx, db, evictChunk)
		if err != nil {
			return err
		}
		if dropped == 0 {
			return fmt.Errorf("%w: %d documents remain over a %d byte budget",
				errEvictionStalled, remaining, budget)
		}
	}
}

// evictDropChunk removes warmed and older documents first, reporting how many
// it dropped so the caller can tell progress from a silent failure.
func evictDropChunk(ctx context.Context, db *sql.DB, n int) (int64, error) {
	if _, err := db.ExecContext(ctx, `DELETE FROM docs_fts WHERE url IN (
		SELECT url FROM docs `+evictDropOrder+` LIMIT ?)`, n); err != nil {
		return 0, fmt.Errorf("drop docs_fts chunk: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM anchors WHERE url IN (
		SELECT url FROM docs `+evictDropOrder+` LIMIT ?)`, n); err != nil {
		return 0, fmt.Errorf("drop anchors chunk: %w", err)
	}
	res, err := db.ExecContext(ctx, `DELETE FROM docs WHERE url IN (
		SELECT url FROM docs `+evictDropOrder+` LIMIT ?)`, n)
	if err != nil {
		return 0, fmt.Errorf("drop docs chunk: %w", err)
	}
	dropped, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("drop docs chunk rows affected: %w", err)
	}
	return dropped, nil
}

// vacuumAndTruncate releases pages before size checks.
// Both operations must be drained to completion.
func vacuumAndTruncate(ctx context.Context, db *sql.DB) error {
	if err := drainPragma(ctx, db, "PRAGMA incremental_vacuum"); err != nil {
		return err
	}
	return drainPragma(ctx, db, "PRAGMA wal_checkpoint(TRUNCATE)")
}

// drainPragma runs a pragma that reports progress as result rows. Scanning them
// is the wait for it to finish.
func drainPragma(ctx context.Context, db *sql.DB, pragma string) error {
	rows, err := db.QueryContext(ctx, pragma)
	if err != nil {
		return fmt.Errorf("%s: %w", pragma, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: %w", pragma, err)
	}
	return nil
}

// docCount reports how many documents the index holds.
func docCount(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM docs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count web index docs: %w", err)
	}
	return n, nil
}

// fileBytes reports the database file size from its page geometry.
func fileBytes(ctx context.Context, db *sql.DB) (int64, error) {
	var pages, pageSize int64
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return 0, fmt.Errorf("web index page_count: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("web index page_size: %w", err)
	}
	return pages * pageSize, nil
}
