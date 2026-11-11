package sourcesnapshot

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ErrSweepInProgress leaves collection to the active maintenance pass.
var ErrSweepInProgress = errors.New("source snapshot sweep already running")

const (
	// Retention keeps inactive snapshot cache entries available for reuse.
	Retention          = 7 * 24 * time.Hour
	snapshotSweepBatch = 128
	manifestSweepBatch = 64
	chunkSweepBatch    = 128
	// Rows one deletion transaction may cascade, counted before it opens.
	sweepTxChildRows = 16384
)

// sweepLimits bound one admission; the cursor carries the rest to the next one.
type sweepLimits struct {
	candidates int64
	rows       int64
}

var defaultSweepLimits = sweepLimits{candidates: 8192, rows: 16 * sweepTxChildRows}

// SweepReport counts one admission's observed work.
type SweepReport struct {
	// Examined counts candidates read; Retained are those still referenced or pinned.
	Examined, Retained int64
	HeadsExpired       int64
	SnapshotsDeleted   int64
	ManifestsDeleted   int64
	ChunksDeleted      int64
	StagingRowsDeleted int64
	// ChildRowsDeleted counts the rows deleted objects cascaded.
	ChildRowsDeleted int64
	Transactions     int64
	// MaxTxChildRows is the largest cascade one transaction carried.
	MaxTxChildRows int64
	// OversizedDeferred counts unreachable objects whose cascade exceeds the
	// transaction bound. They stay intact and are counted again each cycle.
	OversizedDeferred int64
	// CycleComplete is false when the admission's limits left work for the next.
	CycleComplete bool
}

type sweepPhase int

const (
	sweepHeads sweepPhase = iota
	sweepSnapshots
	sweepManifests
	sweepChunks
	sweepStaging
)

// sweepCursor resumes past retained candidates. It lives in memory: a restart
// begins a new cycle, and every deletion rechecks reachability regardless.
type sweepCursor struct {
	phase                 sweepPhase
	afterCreated, afterID string
	after                 string
	afterBuild, afterPath string
}

// Sweep runs one bounded admission: it removes expired snapshots, then the
// manifests and chunks nothing names or pins any more, then staging an earlier
// process left behind. An incomplete report resumes on the next call; concurrent
// sweeps defer to the active collector.
func (s *Store) Sweep(ctx context.Context, createdBefore time.Time) (SweepReport, error) {
	if s == nil {
		return SweepReport{CycleComplete: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return SweepReport{}, err
	}
	if !s.sweepMu.TryLock() {
		return SweepReport{}, ErrSweepInProgress
	}
	defer s.sweepMu.Unlock()
	if err := db.AdmitMaintenance(ctx, s.db); err != nil {
		return SweepReport{}, err
	}
	limits := s.sweepLimits
	if limits == (sweepLimits{}) {
		limits = defaultSweepLimits
	}
	collector := snapshotCollector{db: s.db, queries: s.queries, pins: s.pins, cursor: &s.sweepCursor, limits: limits}
	err := collector.sweep(ctx, db.FormatTime(createdBefore.UTC()))
	if collector.report.OversizedDeferred > 0 || collector.report.ChildRowsDeleted > 0 {
		slog.InfoContext(ctx, "source snapshot collection progress",
			"examined", collector.report.Examined, "retained", collector.report.Retained,
			"snapshots_deleted", collector.report.SnapshotsDeleted, "manifests_deleted", collector.report.ManifestsDeleted,
			"chunks_deleted", collector.report.ChunksDeleted, "child_rows_deleted", collector.report.ChildRowsDeleted,
			"oversized_deferred", collector.report.OversizedDeferred, "cycle_complete", collector.report.CycleComplete)
	}
	return collector.report, err
}

type snapshotCollector struct {
	db      db.Handle
	queries *db.Queries
	pins    *pinSet
	cursor  *sweepCursor
	limits  sweepLimits
	report  SweepReport
}

func (s *snapshotCollector) spent() bool {
	r := &s.report
	rows := r.HeadsExpired + r.StagingRowsDeleted + r.ChildRowsDeleted +
		r.SnapshotsDeleted + r.ManifestsDeleted + r.ChunksDeleted
	return r.Examined >= s.limits.candidates || rows >= s.limits.rows
}

func (s *snapshotCollector) sweep(ctx context.Context, cutoff string) error {
	phases := []func(context.Context, string) (bool, error){
		sweepHeads:     s.sweepExpiredHeads,
		sweepSnapshots: s.sweepSnapshots,
		sweepManifests: s.sweepManifests,
		sweepChunks:    s.sweepChunks,
		sweepStaging:   s.sweepStaleBuilds,
	}
	for int(s.cursor.phase) < len(phases) {
		done, err := phases[s.cursor.phase](ctx, cutoff)
		if err != nil || !done {
			return err
		}
		*s.cursor = sweepCursor{phase: s.cursor.phase + 1}
	}
	*s.cursor = sweepCursor{}
	s.report.CycleComplete = true
	return nil
}

func (s *snapshotCollector) sweepExpiredHeads(ctx context.Context, cutoff string) (bool, error) {
	for !s.spent() {
		removed, err := s.queries.DeleteExpiredSourceSnapshotHeads(ctx, db.DeleteExpiredSourceSnapshotHeadsParams{
			CreatedBefore: cutoff, BatchLimit: snapshotSweepBatch,
		})
		if err != nil {
			return false, err
		}
		s.report.HeadsExpired += removed
		if removed < snapshotSweepBatch {
			return true, nil
		}
	}
	return false, nil
}

func (s *snapshotCollector) sweepManifests(ctx context.Context, _ string) (bool, error) {
	return s.sweepCascade(ctx, cascadeSweep{
		page: func() ([]sweepCandidate, error) {
			rows, err := s.queries.ListSourceManifestSweepCandidates(ctx, db.ListSourceManifestSweepCandidatesParams{
				After: s.cursor.after, BatchLimit: manifestSweepBatch,
			})
			candidates := make([]sweepCandidate, len(rows))
			for i, row := range rows {
				candidates[i] = sweepCandidate{id: row.ID, referenced: row.Referenced != 0}
			}
			return candidates, err
		},
		advance: func(c sweepCandidate) { s.cursor.after = c.id },
		children: func(c sweepCandidate) (int64, error) {
			return s.queries.CountSourceManifestCascadeRows(ctx, db.CountSourceManifestCascadeRowsParams{ID: c.id, ChildLimit: sweepTxChildRows + 1})
		},
		pinned: func(manifestPinned, _ func(string) bool) func(string) bool { return manifestPinned },
		remove: func(ctx context.Context, q *db.Queries, c sweepCandidate) (int64, error) {
			return q.DeleteUnreferencedSourceManifest(ctx, c.id)
		},
		deleted: &s.report.ManifestsDeleted,
	})
}

func (s *snapshotCollector) sweepChunks(ctx context.Context, _ string) (bool, error) {
	return s.sweepCascade(ctx, cascadeSweep{
		page: func() ([]sweepCandidate, error) {
			rows, err := s.queries.ListSourceChunkSweepCandidates(ctx, db.ListSourceChunkSweepCandidatesParams{
				After: s.cursor.after, BatchLimit: chunkSweepBatch,
			})
			candidates := make([]sweepCandidate, len(rows))
			for i, row := range rows {
				candidates[i] = sweepCandidate{id: row.ID, referenced: row.Referenced != 0}
			}
			return candidates, err
		},
		advance: func(c sweepCandidate) { s.cursor.after = c.id },
		children: func(c sweepCandidate) (int64, error) {
			return s.queries.CountSourceChunkCascadeRows(ctx, db.CountSourceChunkCascadeRowsParams{ID: c.id, ChildLimit: sweepTxChildRows + 1})
		},
		pinned: func(_, chunkPinned func(string) bool) func(string) bool { return chunkPinned },
		remove: func(ctx context.Context, q *db.Queries, c sweepCandidate) (int64, error) {
			return q.DeleteUnreferencedSourceManifestChunk(ctx, c.id)
		},
		deleted: &s.report.ChunksDeleted,
	})
}

func (s *snapshotCollector) sweepStaleBuilds(ctx context.Context, _ string) (bool, error) {
	for !s.spent() {
		rows, err := s.queries.ListSourceStagingSweepCandidates(ctx, db.ListSourceStagingSweepCandidatesParams{
			AfterBuild: s.cursor.afterBuild, AfterPath: s.cursor.afterPath, BatchLimit: stagingPage,
		})
		if err != nil {
			return false, err
		}
		if len(rows) == 0 {
			return true, nil
		}
		removed := int64(0)
		if err := s.inTx(ctx, func(q *db.Queries) error {
			for _, row := range rows {
				if strings.HasPrefix(row.BuildID, buildPrefix+"-") {
					continue
				}
				if err := q.DeleteSourceManifestStagingPath(ctx, db.DeleteSourceManifestStagingPathParams(row)); err != nil {
					return err
				}
				removed++
			}
			return nil
		}); err != nil {
			return false, err
		}
		s.report.StagingRowsDeleted += removed
		s.report.Transactions++
		last := rows[len(rows)-1]
		s.cursor.afterBuild, s.cursor.afterPath = last.BuildID, last.Path
	}
	return false, nil
}
