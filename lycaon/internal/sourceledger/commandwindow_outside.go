package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// OutsideRootSpec identifies a command write root and any excluded subtrees.
type OutsideRootSpec struct {
	// Path is the folder's canonical host path.
	Path string
	// Omit holds subtrees covered elsewhere or excluded from source history.
	Omit []string
}

type outsideSnapshotPort interface {
	Ensure(context.Context, sourcesnapshot.Request) (sourcesnapshot.Snapshot, error)
	OutsideScope(root string, omit []string) *sourcescope.Scope
}

type outsideFolder struct {
	spec     OutsideRootSpec
	rel      string
	start    sourcesnapshot.Snapshot
	identity os.FileInfo
	verdict  *FolderCoverage
}

// outsideObservation compares write roots beyond the project without binding watchers.
type outsideObservation struct {
	// rootID is the project's outside namespace; empty means none is observed.
	rootID string
	joins  chan struct{}

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

// An unconfined launch replaces all outside coverage with one host-wide gap.
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

// New parent folders omit previously joined subtrees to preserve their baselines.
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

func (o *outsideObservation) pending() []*outsideFolder {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.unconfined || o.missed {
		return nil
	}
	var out []*outsideFolder
	for _, folder := range o.folders {
		if folder.verdict == nil && folder.start.ID != "" {
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

func (s *Commands) snapshotOutside(ctx context.Context, spec OutsideRootSpec, identity os.FileInfo) (sourcesnapshot.Snapshot, error) {
	before, err := outsideFolderIdentity(spec.Path, identity)
	if err != nil {
		return sourcesnapshot.Snapshot{}, err
	}
	snapshot, err := s.outsideSnapshots.Ensure(ctx, sourcesnapshot.Request{
		Roots:     []sourcesnapshot.Root{{Path: spec.Path}},
		Scope:     s.outsideSnapshots.OutsideScope(spec.Path, spec.Omit),
		Unwatched: true,
	})
	if err == nil {
		_, err = outsideFolderIdentity(spec.Path, before)
	}
	return snapshot, err
}

// A replacement folder does not inherit the original observation.
func outsideFolderIdentity(folder string, want os.FileInfo) (os.FileInfo, error) {
	info, err := os.Lstat(folder)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || fspath.CanonicalPath(folder) != folder || (want != nil && !os.SameFile(want, info)) {
		return nil, &os.PathError{Op: "observe", Path: folder, Err: os.ErrInvalid}
	}
	return info, nil
}

// A failed baseline leaves a coverage gap and lets the command proceed.
func (s *Commands) baselineOutside(ctx context.Context, w *openCommandWindow, specs []OutsideRootSpec) []string {
	added := w.outside.join(specs)
	if len(added) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.observationBudget/2)
	defer cancel()
	var starts []string
	for _, folder := range added {
		identity, err := outsideFolderIdentity(folder.spec.Path, nil)
		var snapshot sourcesnapshot.Snapshot
		if err == nil {
			snapshot, err = s.snapshotOutside(ctx, folder.spec, identity)
		}
		if err != nil {
			slog.InfoContext(ctx, "command window outside baseline", "window_id", w.id, "folder", folder.spec.Path, "err", err)
			w.outside.settle(folder, notObserved(w.outside.rootID, folder.rel, folder.spec.Path,
				gapReason(err, api.SourceWindowGapBaselineIncomplete, api.SourceWindowGapIoError)))
			continue
		}
		w.outside.mu.Lock()
		folder.start, folder.identity = snapshot, identity
		w.outside.mu.Unlock()
		starts = append(starts, snapshot.ID)
	}
	return starts
}

// AddOutsideRoots captures how new write roots looked before a launch.
func (o *OpenCommandWindow) AddOutsideRoots(ctx context.Context, specs []OutsideRootSpec) error {
	if o == nil || o.store == nil || len(specs) == 0 || o.window.outside.rootID == "" {
		return nil
	}
	// Concurrent launches wait until a joining root has a baseline and its before-images.
	joins := o.window.outside.joins
	select {
	case joins <- struct{}{}:
		defer func() { <-joins }()
	case <-ctx.Done():
		return ctx.Err()
	}
	starts := o.store.baselineOutside(ctx, o.window, specs)
	if len(starts) == 0 {
		return nil
	}
	if err := o.store.retention.Retain(ctx, o.window.id, o.window.retained, starts...); err != nil {
		slog.WarnContext(ctx, "command window outside retention", "window_id", o.window.id, "err", err)
	}
	return ctx.Err()
}

// Each folder settles independently so a failure leaves other observations intact.
func (s *Commands) settleOutside(ctx context.Context, w *openCommandWindow) {
	if w.outside.rootID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, windowSettlePassBudget)
	defer cancel()
	for _, folder := range w.outside.pending() {
		w.outside.settle(folder, s.compareOutside(ctx, w, folder))
	}
	coverage := w.outside.decided()
	if len(coverage) == 0 {
		return
	}
	publish := context.WithoutCancel(ctx)
	if err := s.storeWindowCoverage(publish, w.id, coverage); err != nil {
		slog.WarnContext(ctx, "command window outside coverage", "window_id", w.id, "err", err)
		return
	}
	if err := sourcefeed.EmitProjectSignal(publish, w.projectID, rootRefsOf(w.roots)); err != nil {
		slog.WarnContext(ctx, "command window outside publication", "window_id", w.id, "err", err)
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
	end, err := s.snapshotOutside(ctx, folder.spec, folder.identity)
	if err != nil {
		return gap(err, api.SourceWindowGapIoError)
	}
	comparison := outsideComparison{
		start: folder.start, end: end,
		beforeGaps: boundaryPaths(folder.start.Boundaries), afterGaps: boundaryPaths(end.Boundaries),
	}
	if err := s.inventory.recordOutsideChanges(ctx, w, w.outside.rootID, comparison); err != nil {
		return gap(err, api.SourceWindowGapSettlementFailed)
	}
	return observedCoverage(w.outside.rootID, folder.rel, folder.spec.Path, comparison.boundaries())
}

// Tracked files reconcile from their ledger head. New identities record both snapshot states.
func (s *Inventory) recordOutsideChanges(ctx context.Context, w *openCommandWindow, rootID string, comparison outsideComparison) error {
	cause := observationCause{batchID: newID(), window: w}
	now := time.Now().UTC()
	var admitted []RecordInput
	err := s.snapshots.Diff(ctx, comparison.start.ID, comparison.end.ID, func(change sourcesnapshot.Change) error {
		if !comparison.observed(change) {
			return nil
		}
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
		return s.recordOutsideHead(ctx, w.projectID, head, change, cause)
	})
	if err != nil {
		return err
	}
	_, err = s.recordWindowAdmissions(ctx, admitted)
	return err
}

// The command's baseline can be newer than the last retained head.
func (s *Inventory) recordOutsideHead(ctx context.Context, projectID string, head db.SourceBranchHeads, change sourcesnapshot.Change, cause observationCause) error {
	if head.State == "directory" {
		return nil
	}
	if change.After == nil {
		if head.State == "absent" {
			return nil
		}
	} else {
		same, err := s.headHoldsEntry(ctx, head, *change.After)
		if err != nil || same {
			return err
		}
	}
	input := cause.apply(RecordInput{
		RecordLocation: RecordLocation{RootID: head.RootID, Path: head.Path, EntryKind: EntryKindFile},
		ProjectID:      projectID, BranchID: sourcebranch.ID(head.BranchID), FileID: head.FileID, OperationID: newID(),
	})
	input, err := s.admissionInput(ctx, input, change.Before, change.After)
	if err != nil || (change.After != nil && input.AfterSHA256 == "") {
		return err
	}
	recording, err := s.writer.prepare(ctx, []RecordInput{input})
	if err != nil {
		return err
	}
	_, err = s.commitObservation(ctx, &preparedObservation{projectID: projectID, head: head, recording: recording})
	return err
}
