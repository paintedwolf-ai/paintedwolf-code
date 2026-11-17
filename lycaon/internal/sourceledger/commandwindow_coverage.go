package sourceledger

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// maxCoverageBoundaries bounds the boundaries one folder's coverage lists; the
// rest are counted.
const maxCoverageBoundaries = 64

// CoverageBoundary is one folder a partial observation did not enter, relative
// to the covered folder.
type CoverageBoundary struct {
	Path   string                         `json:"path"`
	Reason api.SourceWindowBoundaryReason `json:"reason"`
}

// FolderCoverage is how much of one folder a window compared.
type FolderCoverage struct {
	RootID string
	// Path is the folder within RootID: empty for an attached root's tree, the
	// outside-namespace path of an outside write root.
	Path string
	// Folder is the folder's host path.
	Folder            string
	State             api.SourceWindowCoverage
	Reason            api.SourceWindowGapReason
	Boundaries        []CoverageBoundary
	OmittedBoundaries int
}

func notObserved(rootID, path, folder string, reason api.SourceWindowGapReason) FolderCoverage {
	return FolderCoverage{RootID: rootID, Path: path, Folder: folder, State: api.SourceWindowCoverageNotObserved, Reason: reason}
}

// gapReason classifies what stopped an observation. expired is the reason a
// deadline stands for at that point.
func gapReason(err error, expired, otherwise api.SourceWindowGapReason) api.SourceWindowGapReason {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return expired
	case errors.Is(err, context.Canceled):
		return api.SourceWindowGapCanceled
	default:
		return otherwise
	}
}

// observedCoverage is complete unless a boundary left part of the folder out.
// Folders the window omitted on purpose are covered elsewhere or never recorded.
func observedCoverage(rootID, path, folder string, boundaries []sourcesnapshot.Boundary) FolderCoverage {
	out := FolderCoverage{RootID: rootID, Path: path, Folder: folder, State: api.SourceWindowCoverageComplete}
	for _, boundary := range boundaries {
		if boundary.Reason == "scope" && boundary.Detail == sourcescope.ReasonOmitted {
			continue
		}
		out.State = api.SourceWindowCoveragePartial
		if len(out.Boundaries) == maxCoverageBoundaries {
			out.OmittedBoundaries++
			continue
		}
		out.Boundaries = append(out.Boundaries, CoverageBoundary{Path: boundary.Path, Reason: api.SourceWindowBoundaryReason(boundary.Reason)})
	}
	return out
}

// attachedCoverage states the window's attached roots once its final pass has
// run; passErr is that pass's failure.
func (w *openCommandWindow) attachedCoverage(passErr error) []FolderCoverage {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]FolderCoverage, 0, len(w.roots))
	for _, root := range w.roots {
		switch {
		case w.missedLaunch:
			out = append(out, notObserved(root.ID, "", root.Path, api.SourceWindowGapBaselineIncomplete))
		case passErr != nil:
			out = append(out, notObserved(root.ID, "", root.Path, gapReason(passErr, api.SourceWindowGapTimeBudget, api.SourceWindowGapSettlementFailed)))
		default:
			var boundaries []sourcesnapshot.Boundary
			for _, boundary := range w.attachedBoundaries {
				if cleanRootPath(boundary.RootPath) == cleanRootPath(root.Path) {
					boundaries = append(boundaries, boundary)
				}
			}
			out = append(out, observedCoverage(root.ID, "", root.Path, boundaries))
		}
	}
	return out
}

func writeWindowCoverage(ctx context.Context, q *db.Queries, windowID string, coverage []FolderCoverage) error {
	for _, folder := range coverage {
		boundaries := folder.Boundaries
		if boundaries == nil {
			boundaries = []CoverageBoundary{}
		}
		raw, err := json.Marshal(boundaries)
		if err != nil {
			return err
		}
		if err := q.UpsertSourceCommandWindowCoverage(ctx, db.UpsertSourceCommandWindowCoverageParams{
			WindowID: windowID, RootID: folder.RootID, Path: folder.Path,
			Coverage: string(folder.State), Reason: string(folder.Reason),
			BoundariesJson: string(raw), OmittedBoundaries: int64(folder.OmittedBoundaries),
		}); err != nil {
			return err
		}
	}
	return nil
}

// storeWindowCoverage writes coverage decided before the window's row ends.
func (s *Commands) storeWindowCoverage(ctx context.Context, windowID string, coverage []FolderCoverage) error {
	if len(coverage) == 0 {
		return nil
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeWindowCoverage(ctx, s.queries.WithTx(tx), windowID, coverage); err != nil {
		return err
	}
	return tx.Commit()
}

// windowCoverage reads the stored coverage of windows, keyed by window id.
func (s *Commands) windowCoverage(ctx context.Context, idsJSON string) (map[string][]FolderCoverage, error) {
	rows, err := s.queries.ListSourceCommandWindowCoverage(ctx, idsJSON)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]FolderCoverage)
	for _, row := range rows {
		folder := FolderCoverage{
			RootID: row.RootID, Path: row.Path,
			State: api.SourceWindowCoverage(row.Coverage), Reason: api.SourceWindowGapReason(row.Reason),
			OmittedBoundaries: int(row.OmittedBoundaries),
		}
		if err := json.Unmarshal([]byte(row.BoundariesJson), &folder.Boundaries); err != nil {
			return nil, err
		}
		out[row.WindowID] = append(out[row.WindowID], folder)
	}
	return out, nil
}

// Coverage is what the window has decided so far, one entry per folder; a
// folder still being observed is absent.
func (o *OpenCommandWindow) Coverage() []FolderCoverage {
	if o == nil || o.window == nil {
		return nil
	}
	w := o.window
	w.mu.Lock()
	out := append([]FolderCoverage(nil), w.coverage...)
	// A missed launch decides the attached roots before settlement does.
	missed := w.missedLaunch && w.coverage == nil
	w.mu.Unlock()
	if missed {
		out = w.attachedCoverage(nil)
	}
	out = append(out, w.outside.decided()...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}

// NoteMissedLaunch records that a process of the window's program ran while
// the window was not watching, so no folder can be stated complete.
func (o *OpenCommandWindow) NoteMissedLaunch() {
	if o == nil || o.window == nil {
		return
	}
	o.window.mu.Lock()
	o.window.missedLaunch = true
	o.window.mu.Unlock()
	o.window.outside.noteMissed()
}

// MarkUnconfined records that a process of the window ran without confinement:
// it could write anywhere beyond the project, so nothing there is observed.
func (o *OpenCommandWindow) MarkUnconfined() {
	if o == nil || o.window == nil {
		return
	}
	o.window.outside.markUnconfined()
}
