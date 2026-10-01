package scan

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrNoPendingScans is returned when ClaimNext finds no queued work.
var ErrNoPendingScans = errors.New("no pending code scans")

// SQLStore persists code scan records.
type SQLStore struct {
	SecretIgnores SecretIgnoreSource
	db            db.Handle
	queries       *db.Queries
	outbox        scanEventOutbox
	// QueueChanged wakes the execution runner when queued scans change.
	QueueChanged WorkSignal
	// SeriesChanged wakes the cadence loop when a scan series changes.
	SeriesChanged WorkSignal
}

// NewSQLStore creates a code scan store.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{db: database, queries: db.New(database)}
}

// DB returns the underlying database handle.
func (s *SQLStore) DB() db.Handle {
	if s == nil {
		return nil
	}
	return s.db
}

// Insert stores a code scan row.
func (s *SQLStore) Insert(ctx context.Context, scan api.CodeScan, paths []string, baseSnapshotID string) error {
	if scan.ID == "" {
		scan.ID = uuid.NewString()
	}
	if scan.CreatedAt.IsZero() {
		scan.CreatedAt = time.Now().UTC()
	}
	if scan.Trigger == "" {
		scan.Trigger = api.ScanTriggerManual
	}
	if err := prepareScanFacts(&scan, paths); err != nil {
		return err
	}
	if err := s.ensureAssessmentForScan(ctx, scan); err != nil {
		return err
	}
	categoriesJSON, err := categoriesJSON(scan.Categories)
	if err != nil {
		return err
	}
	pathsJSON, err := db.MarshalJSON(NormalizeScanPaths(paths))
	if err != nil {
		return err
	}
	resultJSON, err := db.MarshalJSON(scan.Result)
	if err != nil {
		return err
	}
	reuseKey, err := scanReuseKey(scan, baseSnapshotID)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		if err := qtx.InsertCodeScan(ctx, db.InsertCodeScanRowParams{
			ID:                scan.ID,
			CanonicalPath:     scan.CanonicalPath,
			CategoriesJson:    categoriesJSON,
			ScannerID:         db.NullString(scan.ScannerID),
			ClaimedBy:         db.NullString(scan.ClaimedBy),
			ClaimToken:        db.NullString(scan.ClaimToken),
			Attempt:           int64(scan.Attempt),
			HeartbeatAt:       db.NullTimePtr(scan.HeartbeatAt),
			LeaseExpiresAt:    db.NullTimePtr(scan.LeaseExpiresAt),
			Status:            string(scan.Status),
			ResultJson:        resultJSON,
			CreatedAt:         db.FormatTime(scan.CreatedAt),
			DelegationID:      scan.DelegationID,
			HeadSha:           scan.HeadSHA,
			SourceSnapshotID:  scan.SourceSnapshotID,
			ReplacementScanID: scan.ReplacementScanID,
			Trigger:           string(scan.Trigger),
			CompletedAt:       db.NullTimePtr(scan.CompletedAt),
			PathsJson:         pathsJSON.String,
			ReuseKey:          reuseKey,
			Error:             db.NullString(scan.Error),
		}); err != nil {
			return err
		}
		if err := insertRunFactsTx(ctx, tx, scan, baseSnapshotID); err != nil {
			return err
		}
		if _, err := bindScanContextsTx(ctx, qtx, scan.ID, scan.AssessmentID, scan.WorkflowRunID, scan.SessionID, scan.CreatedAt); err != nil {
			return err
		}
		return s.emitScanTx(ctx, tx, scan.ID)
	})
}

const scanSupersededMessage = "superseded by the scan for this snapshot"

// WorkSignal coalesces wakeups; pending work remains in the database.
type WorkSignal struct {
	once sync.Once
	wake chan struct{}
}

// Wake is the channel a waiter selects on.
func (s *WorkSignal) Wake() <-chan struct{} {
	s.once.Do(func() { s.wake = make(chan struct{}, 1) })
	return s.wake
}

// Notify wakes one waiter without blocking.
func (s *WorkSignal) Notify() {
	s.Wake()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
