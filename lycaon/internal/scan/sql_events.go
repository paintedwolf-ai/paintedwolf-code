package scan

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetEventOutbox makes a scan transition and its wire event one commit.
func (s *SQLStore) SetEventOutbox(outbox scanEventOutbox) {
	if s != nil {
		s.outbox = outbox
	}
}

type scanEventOutbox interface {
	EnqueueTx(ctx context.Context, tx *sql.Tx, topic api.EventTopic, key events.PublishKey, data any) error
	Notify()
}

// emitScanTx publishes the scan summary and event in the mutation transaction.
// The innermost attached root determines the event's project.
func (s *SQLStore) emitScanTx(ctx context.Context, tx *sql.Tx, id string) error {
	qtx := s.queries.WithTx(tx)
	if err := refreshAssessmentRollups(ctx, qtx, id); err != nil {
		return err
	}
	if err := refreshBoardComparisons(ctx, qtx, id); err != nil {
		return err
	}
	row, err := qtx.GetCodeScan(ctx, id)
	if err != nil {
		return err
	}
	scan := codeScanFromRow(row)
	if err := hydrateScanFacts(ctx, tx, scan); err != nil {
		return err
	}
	if err := publishScanSummary(ctx, qtx, scan); err != nil {
		return err
	}
	if s.outbox == nil {
		return nil
	}
	projectID, err := qtx.ProjectIDForCanonicalPath(ctx, scan.CanonicalPath)
	if db.IsNoRows(err) {
		// Unattached paths have no project event destination.
		return nil
	}
	if err != nil {
		return err
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicScan,
		events.PublishKey{Project: projectID, Facet: scan.ID},
		api.CodeScanEvent{
			ScanID:         scan.ID,
			AssessmentID:   scan.AssessmentID,
			Categories:     scan.Categories,
			Status:         scan.Status,
			CoverageStatus: scan.CoverageStatus,
			FailureCode:    scan.FailureCode,
			FindingsCount:  scan.FindingsCount,
			Error:          strings.TrimSpace(scan.Error),
			Guidance:       scan.Guidance,
			Runtime:        scan.Runtime,
			Progress:       scan.Progress,
			StartedAt:      scan.StartedAt,
			LongRunningAt:  scan.LongRunningAt,
			LongRunning:    scan.LongRunning,
		})
}

func (s *SQLStore) notify() {
	if s != nil && s.outbox != nil {
		s.outbox.Notify()
	}
}

// inTx commits the mutation and its staged event together, then wakes delivery.
func (s *SQLStore) inTx(ctx context.Context, fn func(qtx *db.Queries, tx *sql.Tx) error) error {
	return db.InTx(ctx, s.db, s.queries, func() {
		s.notify()
		s.QueueChanged.Notify()
		s.SeriesChanged.Notify()
	}, fn)
}

// casInTx emits an event only for the winning transition.
func (s *SQLStore) casInTx(ctx context.Context, id string, mutate func(qtx *db.Queries) (int64, error)) (bool, error) {
	won := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		n, err := mutate(qtx)
		if err != nil {
			return err
		}
		won = n == 1
		if !won {
			return nil
		}
		return s.emitScanTx(ctx, tx, id)
	})
	return won && err == nil, err
}
