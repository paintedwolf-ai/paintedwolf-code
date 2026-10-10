package webindex

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

// Open opens (or creates) the index, wiping it on schema version mismatch.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create index directory: %w", err)
	}
	db, err := openVersioned(ctx, path)
	if err != nil {
		return nil, err
	}
	readDB, err := openReadPool(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, fileMode)
	s := &Store{db: db, readDB: readDB, ops: make(chan queuedWrite, opQueueSize)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for op := range s.ops {
			start := time.Now()
			// Report the first write failure from the rebuildable queue.
			if err := op(db); err != nil {
				s.writesFailed.Add(1)
				if s.failWarned.CompareAndSwap(false, true) {
					logger.Warn("web index write failed; search data is degraded until it succeeds again", "error", err)
				}
			}
			s.writesApplied.Add(1)
			if d := time.Since(start); d > slowThreshold {
				logger.Debug("slow web index writer op", "duration_ms", d.Milliseconds())
			}
		}
	}()
	return s, nil
}

// openReadPool isolates query traffic from writer maintenance.
// Active readers may produce a partial checkpoint.
func openReadPool(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(10000)", path))
	if err != nil {
		return nil, fmt.Errorf("open web index read pool: %w", err)
	}
	db.SetMaxOpenConns(2)
	return db, nil
}

func openVersioned(ctx context.Context, path string) (*sql.DB, error) {
	current, err := currentVersion(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !current {
		// Recreate an unreadable or mismatched cache.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
	}
	return openFile(ctx, path)
}

// currentVersion accepts missing files for fresh creation.
func currentVersion(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true, nil
	}
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return false, nil
	}
	defer func() { _ = database.Close() }()
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		return false, nil
	}
	return version == schemaVersion, nil
}

func openFile(ctx context.Context, path string) (*sql.DB, error) {
	// Configure incremental vacuum before the file header is initialized.
	dsn := fmt.Sprintf("file:%s?_pragma=auto_vacuum(2)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open web index: %w", err)
	}
	// The writer uses this single connection.
	db.SetMaxOpenConns(1)
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stamp schema version: %w", err)
	}
	return db, nil
}
