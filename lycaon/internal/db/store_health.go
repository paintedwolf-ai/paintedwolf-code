package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type storeHealth struct {
	failure  atomic.Pointer[StoreIncompatibleError]
	failed   chan struct{}
	path     string
	identity string
}

// Failure is permanent for this store handle.
func (s *Store) Failure() error {
	if s == nil {
		return nil
	}
	if failure := s.health.failure.Load(); failure != nil {
		return failure
	}
	return nil
}

// Failed closes before quarantine drains the writer.
func (s *Store) Failed() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.health.failed
}

func (s *Store) quarantine(failure *StoreIncompatibleError) {
	if !s.health.failure.CompareAndSwap(nil, failure) {
		return
	}
	if err := recordIntegrityFailure(&s.health, failure); err != nil {
		slog.Error("could not persist confirmed store damage", "component", "db", "error", err)
	}
	close(s.health.failed)
	// Closing also refuses retained statements and queued writer calls.
	_ = s.writer.Close()
}

func prepareAuditState(ctx context.Context, writer DBTX, due bool) error {
	if due {
		_, err := writer.ExecContext(ctx, `INSERT INTO store_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, integrityAuditFailedMetaKey, integrityAuditPendingValue)
		return err
	}
	return nil
}

func integrityAuditNeeded(ctx context.Context, sqlDB DBTX) (bool, error) {
	clean, err := lastShutdownWasClean(ctx, sqlDB)
	if err != nil {
		return false, err
	}
	value, err := integrityAuditState(ctx, sqlDB)
	return !clean || value == integrityAuditPendingValue, err
}

func integrityCheckError(cause error, operation string) error {
	detail := fmt.Sprintf("%s: %v", operation, cause)
	var driverErr *sqlite.Error
	if errors.As(cause, &driverErr) {
		switch driverErr.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return storeIncompatible(RecoveryReasonIntegrityFailed, SchemaVersion, detail)
		}
	}
	return fmt.Errorf("%s: %w", operation, cause)
}
