package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const storeCloseTimeout = 30 * time.Second

// StoreShutdownFloor is the time the clean marker and the WAL truncate are
// guaranteed, whatever the caller's remaining budget. Skipping them costs the
// next boot a quick_check on the startup path and a whole-store audit behind
// it, so an already-spent shutdown context must not be what decides they run.
const StoreShutdownFloor = 3 * time.Second

// Handle is the durable-store interface used by repositories.
type Handle interface {
	DBTX
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	Close() error
}

// ReadHandle adds consistent read snapshots.
type ReadHandle interface {
	Handle
	BeginReadTx(context.Context) (*sql.Tx, error)
}

// Store manages a serialized writer and concurrent readers.
type Store struct {
	health  storeHealth
	dataDir string
	writer  *sql.DB
	reader  *sql.DB

	closeOnce   sync.Once
	closeErr    error
	writeCount  atomic.Uint64
	maintenance chan struct{}
	checkpoint  chan struct{}
	// integrityAuditDue is set when the open followed an unclean shutdown:
	// boot ran quick_check and the whole-store audit still has to run.
	integrityAuditDue bool
}

func newStore(writer, reader *sql.DB) *Store {
	s := &Store{
		health: storeHealth{failed: make(chan struct{})},
		writer: writer, reader: reader,
		maintenance: make(chan struct{}, 1), checkpoint: make(chan struct{}, 1),
	}
	s.signalMaintenance()
	s.signalCheckpoint()
	return s
}

// ExecContext runs on the writer.
func (s *Store) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := s.Failure(); err != nil {
		return nil, err
	}
	if s == nil || s.writer == nil {
		return nil, errors.New("execute durable store: writer is closed")
	}
	result, err := s.writer.ExecContext(ctx, query, args...)
	if err == nil {
		s.noteWrite()
	}
	return result, err
}

// PrepareContext uses the writer because statement access mode is unknown.
func (s *Store) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if err := s.Failure(); err != nil {
		return nil, err
	}
	if s == nil || s.writer == nil {
		return nil, errors.New("prepare durable store: writer is closed")
	}
	return s.writer.PrepareContext(ctx, query)
}

// QueryContext runs on the read-only pool.
func (s *Store) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("query durable store: reader is closed")
	}
	return s.reader.QueryContext(ctx, query, args...)
}

// QueryRowContext runs on the read-only pool.
func (s *Store) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if s == nil || s.reader == nil {
		panic("query durable store: nil reader")
	}
	return s.reader.QueryRowContext(ctx, query, args...)
}

// BeginTx starts an immediate writer transaction.
func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if err := s.Failure(); err != nil {
		return nil, err
	}
	if s == nil || s.writer == nil {
		return nil, errors.New("begin durable transaction: writer is closed")
	}
	tx, err := s.writer.BeginTx(ctx, opts)
	if err == nil {
		if failure := s.Failure(); failure != nil {
			_ = tx.Rollback()
			return nil, failure
		}
		s.noteWrite()
	}
	return tx, err
}

// BeginReadTx starts a consistent snapshot on the concurrent reader pool.
func (s *Store) BeginReadTx(ctx context.Context) (*sql.Tx, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("begin durable read transaction: reader is closed")
	}
	return s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

const maintenanceWriteStride = 512

func (s *Store) noteWrite() {
	if s.writeCount.Add(1)%maintenanceWriteStride == 0 {
		s.signalMaintenance()
		s.signalCheckpoint()
	}
}

func (s *Store) signalMaintenance() {
	select {
	case s.maintenance <- struct{}{}:
	default:
	}
}

func (s *Store) maintenanceSignals() <-chan struct{} { return s.maintenance }

func (s *Store) signalCheckpoint() {
	select {
	case s.checkpoint <- struct{}{}:
	default:
	}
}

func (s *Store) checkpointSignals() <-chan struct{} { return s.checkpoint }

// Close drains the store with a bounded background context.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), storeCloseTimeout)
	defer cancel()
	return s.Shutdown(ctx)
}

// shutdownContext keeps the caller's deadline when it still leaves room for the
// final marker and truncate, and otherwise falls back to the floor.
func shutdownContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent.Err() == nil {
		if deadline, ok := parent.Deadline(); !ok || time.Until(deadline) >= StoreShutdownFloor {
			return context.WithCancel(parent)
		}
	}
	return context.WithTimeout(context.WithoutCancel(parent), StoreShutdownFloor)
}

// Shutdown closes readers, truncates the WAL, and closes the writer once.
func (s *Store) Shutdown(parent context.Context) error {
	if s == nil {
		return nil
	}
	ctx, cancel := shutdownContext(parent)
	defer cancel()
	s.closeOnce.Do(func() {
		var errs []error
		if s.reader != nil {
			if err := s.reader.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close durable readers: %w", err))
			}
		}
		if s.writer != nil && s.Failure() != nil {
			errs = append(errs, s.writer.Close())
		} else if s.writer != nil {
			// Include the clean marker in the final checkpoint.
			markErr := markShutdownState(ctx, s.writer, true)
			if markErr != nil {
				errs = append(errs, fmt.Errorf("mark clean shutdown: %w", markErr))
			}
			result, err := checkpointDatabase(ctx, s.writer, WALCheckpointTruncate)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("truncate durable wal: %w", err))
			case !result.Complete():
				errs = append(errs, fmt.Errorf(
					"truncate durable wal: busy=%v log_frames=%d checkpointed_frames=%d",
					result.Busy, result.LogFrames, result.CheckpointedFrames,
				))
			}
			if err := s.writer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close durable writer: %w", err))
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func (s *Store) writerDB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.writer
}

// StoreStats reports the writer and reader pools separately.
type StoreStats struct {
	Writer sql.DBStats
	Reader sql.DBStats
}

// Stats returns a point-in-time view of both pools.
func (s *Store) Stats() StoreStats {
	if s == nil {
		return StoreStats{}
	}
	var out StoreStats
	if s.writer != nil {
		out.Writer = s.writer.Stats()
	}
	if s.reader != nil {
		out.Reader = s.reader.Stats()
	}
	return out
}
