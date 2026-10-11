package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type storeHealth struct {
	failure atomic.Pointer[StoreIncompatibleError]
	failed  chan struct{}
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
	close(s.health.failed)
	// Closing also refuses retained statements and queued writer calls.
	_ = s.writer.Close()
}

func prepareAuditState(ctx context.Context, writer *sql.DB, due bool) error {
	if due {
		return stampIntegrityAuditFailed(ctx, writer, errors.New("integrity audit pending"))
	}
	return clearIntegrityAuditFailed(ctx, writer)
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
