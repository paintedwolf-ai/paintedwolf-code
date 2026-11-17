package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// OutsideRootSpec is one write root beyond the project that a window compares
// before and after its command.
type OutsideRootSpec struct {
	// Path is the folder's canonical host path.
	Path string
	// Omit names folders under Path that other observation covers or source
	// history never records.
	Omit []string
}

type outsideSnapshotPort interface {
	Ensure(context.Context, sourcesnapshot.Request) (sourcesnapshot.Snapshot, error)
	OutsideScope(root string, omit []string) *sourcescope.Scope
}

// outsideFolder is one outside write root of a window. Its verdict is set once,
// when its baseline fails or its settlement ends.
type outsideFolder struct {
	spec    OutsideRootSpec
	rel     string
	start   string
	verdict *FolderCoverage
}

// outsideObservation is a window's before-and-after comparison of the command's
// write roots beyond the project. Those roots have no watcher, so they are
// compared only when they join the window and when it closes.
type outsideObservation struct {
	// rootID is the project's outside namespace; empty means none is observed.
	rootID string

	mu         sync.Mutex
	folders    []*outsideFolder
	unconfined bool
	missed     bool
}

func (o *outsideObservation) markUnconfined() {
	o.mu.Lock()
	o.unconfined = true
	o.mu.Unlock()
}

func (o *outsideObservation) noteMissed() {
	o.mu.Lock()
	o.missed = true
	o.mu.Unlock()
}

// decided lists every folder whose verdict is known. An unconfined command
// could write anywhere beyond the project, so one entry for all of it replaces
// the folders; a missed launch leaves every folder without a baseline.
func (o *outsideObservation) decided() []FolderCoverage {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rootID == "" {
		return nil
	}
	if o.unconfined {
		return []FolderCoverage{notObserved(o.rootID, "", projectroot.OutsidePath, api.SourceWindowGapUnconfined)}
	}
	var out []FolderCoverage
	for _, folder := range o.folders {
		switch {
		case o.missed:
			out = append(out, notObserved(o.rootID, folder.rel, folder.spec.Path, api.SourceWindowGapBaselineIncomplete))
		case folder.verdict != nil:
			out = append(out, *folder.verdict)
		}
	}
	return out
}

// join adds the specs no folder already covers and returns the new folders. A
// new folder holding earlier ones omits them, so their baselines still stand.
func (o *outsideObservation) join(specs []OutsideRootSpec) []*outsideFolder {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rootID == "" {
		return nil
	}
	var added []*outsideFolder
	for _, spec := range specs {
		spec.Path = cleanRootPath(spec.Path)
		rel, ok := projectroot.OutsideRel(spec.Path)
		if !ok || o.covers(spec.Path) {
			continue
		}
		for _, folder := range o.folders {
			if confine.PathAtOrUnder(folder.spec.Path, spec.Path) {
				spec.Omit = append(spec.Omit, folder.spec.Path)
			}
		}
		folder := &outsideFolder{spec: spec, rel: rel}
		o.folders = append(o.folders, folder)
		added = append(added, folder)
	}
	return added
}

// covers reports whether path lies in a folder already observed. Caller holds mu.
func (o *outsideObservation) covers(path string) bool {
	for _, folder := range o.folders {
		if confine.PathAtOrUnder(path, folder.spec.Path) {
			return true
		}
	}
	return false
}

func (o *outsideObservation) settle(folder *outsideFolder, verdict FolderCoverage) {
	o.mu.Lock()
	folder.verdict = &verdict
	o.mu.Unlock()
}

// pending lists the folders that have a baseline and no verdict yet, or none
// when the command ran unconfined.
func (o *outsideObservation) pending() []*outsideFolder {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.unconfined {
		return nil
	}
	var out []*outsideFolder
	for _, folder := range o.folders {
		if folder.verdict == nil && folder.start != "" {
			out = append(out, folder)
		}
	}
	return out
}

func (s *Commands) outsideNamespace(ctx context.Context, projectID string) (string, error) {
	id, err := s.queries.GetProjectOutsideRootID(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// snapshotOutside captures one outside folder under the outside scope.
func (s *Commands) snapshotOutside(ctx context.Context, spec OutsideRootSpec) (sourcesnapshot.Snapshot, error) {
	return s.outsideSnapshots.Ensure(ctx, sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: spec.Path}},
		Scope:     s.outsideSnapshots.OutsideScope(spec.Path, spec.Omit),
		Unwatched: true,
	})
}

// baselineOutside records the starting state of the folders specs add to the
// window and returns their snapshots. A folder whose baseline fails is not
// observed; the command still runs.
func (s *Commands) baselineOutside(ctx context.Context, w *openCommandWindow, specs []OutsideRootSpec) []string {
	added := w.outside.join(specs)
	if len(added) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.observationBudget/2)
	defer cancel()
	var starts []string
	for _, folder := range added {
		snapshot, err := s.snapshotOutside(ctx, folder.spec)
		if err != nil {
			slog.InfoContext(ctx, "command window outside baseline", "window_id", w.id, "folder", folder.spec.Path, "err", err)
			w.outside.settle(folder, notObserved(w.outside.rootID, folder.rel, folder.spec.Path,
				gapReason(err, api.SourceWindowGapBaselineIncomplete, api.SourceWindowGapIoError)))
			continue
		}
		w.outside.mu.Lock()
		folder.start = snapshot.ID
		w.outside.mu.Unlock()
		starts = append(starts, snapshot.ID)
	}
	return starts
}

// AddOutsideRoots joins write roots a later launch of the window's program may
// write, recording their baselines before it starts.
func (o *OpenCommandWindow) AddOutsideRoots(ctx context.Context, specs []OutsideRootSpec) {
	if o == nil || o.store == nil {
		return
	}
	starts := o.store.baselineOutside(ctx, o.window, specs)
	if len(starts) == 0 {
		return
	}
	if err := o.store.retention.Retain(ctx, o.window.id, o.window.retained, starts...); err != nil {
		slog.WarnContext(ctx, "command window outside retention", "window_id", o.window.id, "err", err)
	}
}

// settleOutside compares each outside folder with its baseline and records what
// changed against the window. Each folder settles on its own, so one failing
// keeps the others' changes; the verdicts are stored before the row ends.
func (s *Commands) settleOutside(ctx context.Context, w *openCommandWindow) {
	if w.outside.rootID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, windowSettlePassBudget)
	defer cancel()
	for _, folder := range w.outside.pending() {
		w.outside.settle(folder, s.compareOutside(ctx, w, folder))
	}
	if err := s.storeWindowCoverage(context.WithoutCancel(ctx), w.id, w.outside.decided()); err != nil {
		slog.WarnContext(ctx, "command window outside coverage", "window_id", w.id, "err", err)
	}
}

func (s *Commands) compareOutside(ctx context.Context, w *openCommandWindow, folder *outsideFolder) FolderCoverage {
	gap := func(err error, otherwise api.SourceWindowGapReason) FolderCoverage {
		slog.InfoContext(ctx, "command window outside settlement", "window_id", w.id, "folder", folder.spec.Path, "err", err)
		return notObserved(w.outside.rootID, folder.rel, folder.spec.Path, gapReason(err, api.SourceWindowGapTimeBudget, otherwise))
	}
	if err := ctx.Err(); err != nil {
		return gap(err, api.SourceWindowGapCanceled)
	}
	end, err := s.snapshotOutside(ctx, folder.spec)
	if err != nil {
		return gap(err, api.SourceWindowGapIoError)
	}
	if err := s.inventory.recordOutsideChanges(ctx, w, w.outside.rootID, folder.start, end.ID); err != nil {
		return gap(err, api.SourceWindowGapSettlementFailed)
	}
	return observedCoverage(w.outside.rootID, folder.rel, folder.spec.Path, end.Boundaries)
}

// recordOutsideChanges records what differs between two snapshots of an outside
// folder as the window's changes on the outside namespace. A file whose head
// already holds the new state was recorded by the tool that wrote it; a tracked
// file reconciles from its head, and an untracked one is admitted from the
// snapshots, as a window admits untracked files under an attached root.
func (s *Inventory) recordOutsideChanges(ctx context.Context, w *openCommandWindow, rootID, startID, endID string) error {
	cause := observationCause{batchID: newID(), window: w}
	now := time.Now().UTC()
	var admitted []RecordInput
	err := s.snapshots.Diff(ctx, startID, endID, func(change sourcesnapshot.Change) error {
		rel, ok := projectroot.OutsideRel(filepath.Join(change.RootPath(), filepath.FromSlash(change.Path())))
		if !ok {
			return nil
		}
		head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
			ProjectID: w.projectID, BranchID: sourcebranch.Trunk.String(), RootID: rootID, Path: rel,
		})
		if errors.Is(err, sql.ErrNoRows) {
			in, err := s.admissionInput(ctx, cause.apply(RecordInput{
				RecordLocation: RecordLocation{RootID: rootID, Path: rel, EntryKind: EntryKindFile},
				ProjectID:      w.projectID, BranchID: sourcebranch.Trunk, TS: now,
			}), change.Before, change.After)
			if err != nil {
				return err
			}
			admitted = append(admitted, in)
			return nil
		}
		if err != nil {
			return err
		}
		return s.reconcileOutsideHead(ctx, w.projectID, head, change.After, cause)
	})
	if err != nil {
		return err
	}
	_, err = s.recordWindowAdmissions(ctx, admitted)
	return err
}

// reconcileOutsideHead records a tracked outside file's drift from its head.
func (s *Inventory) reconcileOutsideHead(ctx context.Context, projectID string, head db.SourceBranchHeads, after *sourcesnapshot.Entry, cause observationCause) error {
	if head.State == "directory" {
		return nil
	}
	var observed *observedFile
	if after == nil {
		if head.State == "absent" {
			return nil
		}
	} else {
		same, err := s.headHoldsEntry(ctx, head, *after)
		if err != nil || same {
			return err
		}
		observed, err = s.observedVersion(ctx, head.RootID, *after)
		if errors.Is(err, errVersionMovedOn) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	_, err := s.recordObservation(ctx, projectID, head, observed, newID(), cause)
	return err
}
