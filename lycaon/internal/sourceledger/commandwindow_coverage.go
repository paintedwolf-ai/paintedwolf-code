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

// Keep a count for boundaries beyond the display limit.
const maxCoverageBoundaries = 64

// CoverageBoundary names an unobserved subtree relative to the covered folder.
type CoverageBoundary struct {
	Path   string                         `json:"path"`
	Reason api.SourceWindowBoundaryReason `json:"reason"`
}

// FolderCoverage is how much of one folder a window compared.
type FolderCoverage struct {
	RootID string
	// Path is empty for an entire attached root and relative to RootID otherwise.
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

// Intentional omissions belong to another observation or fall outside source history.
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

// Coverage lists folders whose comparison has a verdict.
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

// NoteMissedLaunch marks coverage incomplete for a process that ran without a window.
func (o *OpenCommandWindow) NoteMissedLaunch() {
	if o == nil || o.window == nil {
		return
	}
	o.window.mu.Lock()
	o.window.missedLaunch = true
	o.window.mu.Unlock()
	o.window.outside.noteMissed()
}

// MarkUnconfined replaces outside folders with one host-wide observation gap.
func (o *OpenCommandWindow) MarkUnconfined() {
	if o == nil || o.window == nil {
		return
	}
	o.window.outside.markUnconfined()
}
