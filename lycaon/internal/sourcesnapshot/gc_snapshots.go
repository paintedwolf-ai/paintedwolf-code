package sourcesnapshot

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

func (s *snapshotCollector) sweepSnapshots(ctx context.Context, cutoff string) (bool, error) {
	return s.sweepCascade(ctx, cascadeSweep{
		page: func() ([]sweepCandidate, error) {
			rows, err := s.queries.ListSourceSnapshotSweepCandidates(ctx, db.ListSourceSnapshotSweepCandidatesParams{
				CreatedBefore: cutoff,
				AfterCreated: s.cursor.afterCreated, AfterID: s.cursor.afterID, BatchLimit: snapshotSweepBatch,
			})
			candidates := make([]sweepCandidate, len(rows))
			for i, row := range rows {
				candidates[i] = sweepCandidate{id: row.ID, created: row.CreatedTs, referenced: row.Referenced != 0}
			}
			return candidates, err
		},
		advance: func(c sweepCandidate) { s.cursor.afterCreated, s.cursor.afterID = c.created, c.id },
		children: func(c sweepCandidate) (int64, error) {
			return s.queries.CountSourceSnapshotCascadeRows(ctx, db.CountSourceSnapshotCascadeRowsParams{ID: c.id, ChildLimit: sweepTxChildRows + 1})
		},
		pinned: func(_, _ func(string) bool) func(string) bool {
			return func(string) bool { return false }
		},
		remove: func(ctx context.Context, q *db.Queries, c sweepCandidate) (int64, error) {
			return q.DeleteUnreferencedSourceSnapshot(ctx, db.DeleteUnreferencedSourceSnapshotParams{ID: c.id, CreatedBefore: cutoff})
		},
		deleted: &s.report.SnapshotsDeleted,
	})
}

type sweepCandidate struct {
	id, created string
	referenced  bool
	childRows   int64
}

// cascadeSweep is one level of the graph: a parent table whose deletion cascades
// to child rows.
type cascadeSweep struct {
	page     func() ([]sweepCandidate, error)
	advance  func(sweepCandidate)
	children func(sweepCandidate) (int64, error)
	pinned   func(manifestPinned, chunkPinned func(string) bool) func(string) bool
	remove   func(context.Context, *db.Queries, sweepCandidate) (int64, error)
	deleted  *int64
}

// sweepCascade walks candidates until the level ends or the admission is spent.
// The candidate read is advisory: each delete rechecks references, and pins are
// frozen for the transaction.
func (s *snapshotCollector) sweepCascade(ctx context.Context, level cascadeSweep) (bool, error) {
	for !s.spent() {
		candidates, err := level.page()
		if err != nil {
			return false, err
		}
		if len(candidates) == 0 {
			return true, nil
		}
		taken, eligible, err := s.takeCandidates(level, candidates)
		if err != nil {
			return false, err
		}
		if err := s.removeCandidates(ctx, level, eligible); err != nil {
			return false, err
		}
		level.advance(candidates[taken-1])
	}
	return false, nil
}

// takeCandidates selects the prefix one transaction can carry. A retained or
// oversized candidate costs no transaction, so a retained page opens no writer.
func (s *snapshotCollector) takeCandidates(level cascadeSweep, candidates []sweepCandidate) (int, []sweepCandidate, error) {
	var eligible []sweepCandidate
	rows, taken := int64(0), 0
	defer func() { s.report.Examined += int64(taken) }()
	for _, candidate := range candidates {
		if candidate.referenced {
			s.report.Retained++
			taken++
			continue
		}
		children, err := level.children(candidate)
		if err != nil {
			return taken, nil, err
		}
		candidate.childRows = children
		switch {
		case children > sweepTxChildRows:
			s.report.OversizedDeferred++
		case rows+children > sweepTxChildRows:
			return taken, eligible, nil
		default:
			rows += children
			eligible = append(eligible, candidate)
		}
		taken++
	}
	return taken, eligible, nil
}

func (s *snapshotCollector) removeCandidates(ctx context.Context, level cascadeSweep, eligible []sweepCandidate) error {
	if len(eligible) == 0 {
		return nil
	}
	retained, deleted, children := int64(0), int64(0), int64(0)
	err := s.inPinnedTx(ctx, func(q *db.Queries, manifestPinned, chunkPinned func(string) bool) error {
		pinned := level.pinned(manifestPinned, chunkPinned)
		for _, candidate := range eligible {
			if pinned(candidate.id) {
				retained++
				continue
			}
			removed, err := level.remove(ctx, q, candidate)
			if err != nil {
				return err
			}
			if removed == 0 {
				retained++
				continue
			}
			deleted += removed
			children += candidate.childRows
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.report.Retained += retained
	*level.deleted += deleted
	s.report.ChildRowsDeleted += children
	s.report.Transactions++
	s.report.MaxTxChildRows = max(s.report.MaxTxChildRows, children)
	return nil
}

func (s *snapshotCollector) inTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// Acquire the writer before freezing pins so writer contention cannot hold captures.
func (s *snapshotCollector) inPinnedTx(ctx context.Context, fn func(*db.Queries, func(string) bool, func(string) bool) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return s.pins.sweepUnpinned(ctx, func(manifestPinned, chunkPinned func(string) bool) error {
		if err := fn(s.queries.WithTx(tx), manifestPinned, chunkPinned); err != nil {
			return err
		}
		return tx.Commit()
	})
}
