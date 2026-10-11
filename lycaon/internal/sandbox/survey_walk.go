package sandbox

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// SurveyAction is the visitor's control signal back to the walk.
type SurveyAction uint8

const (
	// SurveyContinue descends into directories and proceeds to the next entry.
	SurveyContinue SurveyAction = iota
	// SurveySkipDir prunes the current directory's subtree (ignored for files).
	SurveySkipDir
	// SurveyStop ends the whole walk without error.
	SurveyStop
)

// SurveyEntry paths are slash-separated and root-relative; direct children have depth 1.
// Symlinks have IsSymlink true and IsDir false.
type SurveyEntry struct {
	Rel       string
	Abs       string
	Depth     int
	IsDir     bool
	IsSymlink bool
	DirEntry  fs.DirEntry
}

// SurveyOptions tune which entries reach the visitor.
type SurveyOptions struct {
	// Stream visits filesystem-order batches without retaining whole listings.
	// It requires unbounded traversal and does not promise priority/name order.
	Stream bool
	// IncludeHidden keeps dot-prefixed entries.
	IncludeHidden bool
	// IncludeEngineOverlay keeps the project settings overlay directory.
	IncludeEngineOverlay bool
	// IncludeVCSMetadata keeps .git directories.
	IncludeVCSMetadata bool
	// MaxDepth is unlimited when zero.
	MaxDepth int
	// A false Admit result skips the entry and any descendants.
	Admit func(rel, abs string, isDir bool) bool
	// PruneNestedVCS skips descendants containing a .git entry.
	PruneNestedVCS bool
	// OnNestedRepoPruned receives each pruned repository root once.
	OnNestedRepoPruned func(abs string)
	// Budgets bound what the walk reads. Zero fields are unbounded.
	Budgets SurveyBudgets
	// OnBoundary reports scope, budget, and unreadable-directory boundaries.
	OnBoundary func(SurveyBoundary)
	// A nil Scope imposes no additional admission rules.
	Scope SurveyScope
}

// SurveyScope checks directories before opening them; rejected subtrees cost one entry.
type SurveyScope interface {
	// PruneDir returns the reason for excluding a directory.
	PruneDir(rel, abs string) (detail string, prune bool)
	// AdmitFile reports whether rel, a non-directory entry, is surveyed.
	AdmitFile(rel, abs string) bool
}

// SurveyDeferrer orders deferred directories after their siblings without excluding them.
type SurveyDeferrer interface {
	DeferDir(rel, abs string) bool
}

// SurveyLazyTier records readable directory stubs without eager descent.
type SurveyLazyTier interface {
	LazyDir(rel, abs string) (detail string, lazy bool)
}

// Human listings include hidden entries, project overlays, and VCS metadata.
func HumanFilesSurveyOptions() SurveyOptions {
	return SurveyOptions{IncludeHidden: true, IncludeEngineOverlay: true, IncludeVCSMetadata: true}
}

func surveySkipsDir(rel, name string, opts SurveyOptions) bool {
	if !opts.IncludeVCSMetadata && (IsVCSDirBaseName(name) || pathHasDirSegment(rel, vcsDirName)) {
		return true
	}
	if !opts.IncludeEngineOverlay && (IsEngineOverlayBaseName(name) || pathHasDirSegment(rel, settingsoverlay.DirName())) {
		return true
	}
	return false
}

func surveySkipsDirName(name string, opts SurveyOptions) bool {
	if !opts.IncludeVCSMetadata && IsVCSDirBaseName(name) {
		return true
	}
	if !opts.IncludeEngineOverlay && IsEngineOverlayBaseName(name) {
		return true
	}
	return false
}

var errSurveyStop = errors.New("sandbox: survey walk stopped")

// SurveyWalk visits admitted entries below root without following symlinks.
// Entries are ordered by name within each priority group unless Stream is set.
func SurveyWalk(ctx context.Context, root string, opts SurveyOptions, visit func(SurveyEntry) (SurveyAction, error)) error {
	if opts.Stream && opts.Budgets.Bounded() {
		return os.ErrInvalid
	}
	w := &surveyWalker{ctx: ctx, opts: opts, visit: visit, budget: NewWalkBudget(opts.Budgets)}
	if _, err := os.Lstat(root); err != nil {
		return err
	}
	err := w.walkDir(".", root, 0)
	if errors.Is(err, errSurveyStop) || errors.Is(err, errSurveyBudgetSpent) {
		return nil
	}
	return err
}

var errSurveyBudgetSpent = errors.New("sandbox: survey walk budget spent")

type surveyWalker struct {
	ctx    context.Context
	opts   SurveyOptions
	visit  func(SurveyEntry) (SurveyAction, error)
	budget *WalkBudget
}

// subtreeCut unwinds the recursion to the ancestor whose cap fell.
type subtreeCut struct{ depth int }

func (c subtreeCut) Error() string { return "sandbox: survey subtree cap reached" }

func (w *surveyWalker) boundary(rel string, reason BoundaryReason, detail string, entries int) {
	if w.opts.OnBoundary != nil {
		w.opts.OnBoundary(SurveyBoundary{Rel: rel, Reason: reason, Detail: detail, Entries: entries})
	}
}

// walkDir lists one directory and descends into the children that survive.
// depth is the directory's own depth; the root is 0.
func (w *surveyWalker) walkDir(rel, abs string, depth int) error {
	if w.opts.Stream {
		return w.walkStreamingDir(rel, abs, depth)
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	entries, overflow, err := w.list(abs)
	if err != nil {
		if failure := w.readFailure(rel, err); failure != nil {
			return failure
		}
	}
	if overflow {
		w.boundary(rel, BoundaryDirectoryCap, "", len(entries))
		return nil
	}
	dirDepth := w.budget.EnterDir()
	defer w.budget.LeaveDir()
	return w.visitEntries(rel, abs, depth, dirDepth, w.ordered(rel, abs, entries))
}

func (w *surveyWalker) visitEntries(rel, abs string, depth, dirDepth int, entries []fs.DirEntry) error {
	for _, d := range entries {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		if cutDepth, reason := w.budget.Charge(1); cutDepth >= 0 {
			return w.cut(rel, cutDepth, dirDepth, reason)
		}
		name := d.Name()
		childRel := name
		if rel != "." {
			childRel = rel + "/" + name
		}
		childAbs := filepath.Join(abs, name)
		isDir := d.IsDir()
		isSymlink := d.Type()&fs.ModeSymlink != 0
		if isDir {
			if surveySkipsDir(childRel, name, w.opts) {
				continue
			}
			if w.opts.PruneNestedVCS && gitrepo.IsRoot(childAbs) {
				if w.opts.OnNestedRepoPruned != nil {
					w.opts.OnNestedRepoPruned(childAbs)
				}
				continue
			}
			if !w.opts.IncludeHidden && IsHiddenName(name) {
				continue
			}
			if w.opts.Scope != nil {
				if detail, prune := w.opts.Scope.PruneDir(childRel, childAbs); prune {
					w.boundary(childRel, BoundaryScope, detail, 0)
					continue
				}
			}
			if w.opts.Admit != nil && !w.opts.Admit(childRel, childAbs, true) {
				continue
			}
		} else {
			if !w.opts.IncludeHidden && IsHiddenName(name) {
				continue
			}
			if w.opts.Scope != nil && !w.opts.Scope.AdmitFile(childRel, childAbs) {
				continue
			}
			if w.opts.Admit != nil && !w.opts.Admit(childRel, childAbs, false) {
				continue
			}
		}
		childDepth := depth + 1
		action, err := w.visit(SurveyEntry{Rel: childRel, Abs: childAbs, Depth: childDepth, IsDir: isDir, IsSymlink: isSymlink, DirEntry: d})
		if err != nil {
			return err
		}
		switch action {
		case SurveyStop:
			return errSurveyStop
		case SurveySkipDir:
			if isDir {
				continue
			}
		case SurveyContinue:
		}
		if !isDir {
			continue
		}
		if lazyScope, ok := w.opts.Scope.(SurveyLazyTier); ok {
			if detail, lazy := lazyScope.LazyDir(childRel, childAbs); lazy {
				w.boundary(childRel, BoundaryLazy, detail, 0)
				continue
			}
		}
		if w.opts.MaxDepth > 0 && childDepth >= w.opts.MaxDepth {
			continue
		}
		if err := w.walkDir(childRel, childAbs, childDepth); err != nil {
			var cut subtreeCut
			if errors.As(err, &cut) && cut.depth == dirDepth {
				// This subtree is exhausted; traversal continues with its siblings.
				return nil
			}
			return err
		}
	}
	return nil
}

// ordered returns a listing with deferred directories moved after every
// other entry, keeping name order within each group.
func (w *surveyWalker) ordered(rel, abs string, entries []fs.DirEntry) []fs.DirEntry {
	deferrer, ok := w.opts.Scope.(SurveyDeferrer)
	if !ok || w.opts.Scope == nil {
		return entries
	}
	var deferred []fs.DirEntry
	kept := entries[:0]
	for _, d := range entries {
		if d.IsDir() {
			childRel := d.Name()
			if rel != "." {
				childRel = rel + "/" + d.Name()
			}
			if deferrer.DeferDir(childRel, filepath.Join(abs, d.Name())) {
				deferred = append(deferred, d)
				continue
			}
		}
		kept = append(kept, d)
	}
	return append(kept, deferred...)
}

// cut reports the boundary for the directory whose bound fell and unwinds to
// it. Depth 0 is the walk budget: the traversal ends at the root.
func (w *surveyWalker) cut(rel string, cutDepth, dirDepth int, reason BoundaryReason) error {
	if cutDepth == 0 {
		w.boundary(".", reason, "", w.budget.Observed())
		return errSurveyBudgetSpent
	}
	if cutDepth == dirDepth {
		w.boundary(rel, reason, "", w.budget.SubtreeObserved())
		return nil
	}
	return subtreeCut{depth: cutDepth}
}

// list reads one directory in name order, stopping past the listing limit.
// overflow reports that the directory exceeded DirectoryEntries.
func (w *surveyWalker) list(abs string) ([]fs.DirEntry, bool, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	limit := w.budget.ListingLimit()
	entries := make([]fs.DirEntry, 0, 64)
	for {
		if err := w.ctx.Err(); err != nil {
			return nil, false, err
		}
		batch := 256
		if limit > 0 && limit-len(entries) < batch {
			batch = limit - len(entries)
		}
		if batch <= 0 {
			break
		}
		chunk, readErr := f.ReadDir(batch)
		entries = append(entries, chunk...)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return entries, false, readErr
		}
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	if limit > 0 && len(entries) >= limit {
		return entries, true, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, false, nil
}

// SurveyReadDir lists immediate children of dir. Metadata skips use the entry
// base name only; recursive prefix pruning is SurveyWalk's job. relDir is dir's
// repo-relative path ("." for the root).
func SurveyReadDir(dir, relDir string, opts SurveyOptions) ([]SurveyEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	return surveyDirectoryEntries(dir, relDir, entries, opts), nil
}

func surveyDirectoryEntries(dir, relDir string, entries []os.DirEntry, opts SurveyOptions) []SurveyEntry {
	out := make([]SurveyEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !opts.IncludeHidden && IsHiddenName(name) {
			continue
		}
		rel := name
		if relDir != "" && relDir != "." {
			rel = relDir + "/" + name
		}
		if surveySkipsDirName(name, opts) {
			continue
		}
		abs := filepath.Join(dir, name)
		if opts.Admit != nil && !opts.Admit(rel, abs, e.IsDir()) {
			continue
		}
		out = append(out, SurveyEntry{
			Rel:       rel,
			Abs:       abs,
			Depth:     1,
			IsDir:     e.IsDir(),
			IsSymlink: e.Type()&fs.ModeSymlink != 0,
			DirEntry:  e,
		})
	}
	return out
}
