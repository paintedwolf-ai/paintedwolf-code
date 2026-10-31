package survey

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

type sourceInventoryCatalog interface {
	Current(context.Context, string, []sourcecatalog.Root) sourcecatalog.Snapshot
	ObserveWithin(context.Context, string, []sourcecatalog.Root, time.Duration) (sourcecatalog.Snapshot, bool, error)
	BoundaryPath(context.Context, string, string, bool) string
	ObserveDependencyScope(context.Context, sourcecatalog.Root) (sourcecatalog.Snapshot, error)
}

// sourceInventory is one scope of a catalog generation. Entries reach callers
// through walk, addressed relative to the project root.
type sourceInventory struct {
	catalog  sourceInventoryCatalog
	rootPath string
	snapshot sourcecatalog.Snapshot
	// catalogRoot and catalogDir address the scope inside snapshot.
	catalogRoot string
	catalogDir  string
	rootID      string
	// base is the scope relative to the project root.
	base string
	// rebased marks a scoped generation whose paths start at base.
	rebased  bool
	revision uint64
	// stale marks the last complete generation served while a newer one builds.
	stale bool
	// live is used when no complete generation can answer this scope yet.
	live bool
}

// walk visits the scope depth-first. A skipped directory costs one visit, so
// pruning keeps a walk proportional to what it admits.
func (inv sourceInventory) walk(ctx context.Context, visit func(sourcecatalog.Entry) sourcecatalog.WalkStep) error {
	if inv.live {
		err := inv.walkLive(ctx, inv.base, visit)
		if errors.Is(err, errInventoryStop) {
			return nil
		}
		return err
	}
	if inv.stale || inv.snapshot.RootBoundary || !repochange.Coverage(inv.rootPath).Complete {
		err := inv.walkMerged(ctx, inv.base, visit)
		if errors.Is(err, errInventoryStop) {
			return nil
		}
		return err
	}
	var expansionErr error
	stopped := false
	err := inv.snapshot.Walk(ctx, inv.catalogRoot, inv.catalogDir, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		if inv.rebased {
			entry.RootID = inv.rootID
			entry.Path = path.Join(inv.base, entry.Path)
			entry.Parent = normalizeCatalogPath(path.Dir(entry.Path))
			entry.Depth = catalogPathDepth(entry.Path)
		}
		action := visit(entry)
		if action == sourcecatalog.WalkContinue && entry.Boundary {
			expansionErr = inv.walkLive(ctx, entry.Path, visit)
			if expansionErr != nil {
				return sourcecatalog.WalkStop
			}
			return sourcecatalog.WalkSkip
		}
		if action != sourcecatalog.WalkContinue || !entry.IsDir || entry.IsSymlink || inv.catalog == nil || inv.catalog.BoundaryPath(ctx, inv.rootPath, entry.Path, true) == "" {
			return action
		}
		// Tool walks expand only lazy directories their own selection admits.
		lazyRoot := sourcecatalog.Root{ID: inv.rootID + "\x00" + entry.Path, Path: filepath.Join(inv.rootPath, filepath.FromSlash(entry.Path)), Within: inv.rootPath}
		snapshot, openErr := inv.catalog.ObserveDependencyScope(ctx, lazyRoot)
		if openErr != nil {
			expansionErr = openErr
			return sourcecatalog.WalkStop
		}
		expansionErr = snapshot.Walk(ctx, lazyRoot.ID, ".", func(child sourcecatalog.Entry) sourcecatalog.WalkStep {
			child.RootID = inv.rootID
			child.Path = path.Join(entry.Path, child.Path)
			child.Parent = normalizeCatalogPath(path.Dir(child.Path))
			child.Depth = catalogPathDepth(child.Path)
			step := visit(child)
			stopped = step == sourcecatalog.WalkStop
			return step
		})
		if expansionErr != nil || stopped {
			return sourcecatalog.WalkStop
		}
		return sourcecatalog.WalkSkip
	})
	err = errors.Join(err, expansionErr)
	if errors.Is(err, errInventoryStop) {
		return nil
	}
	return err
}

var errInventoryStop = errors.New("inventory walk stopped")

// walkMerged uses catalog entries for known paths and live directory listings for
// paths the generation cannot prove complete. The live listing also removes
// deleted indexed entries before a query can present them.
func (inv sourceInventory) walkMerged(ctx context.Context, dir string, visit func(sourcecatalog.Entry) sourcecatalog.WalkStep) error {
	full := filepath.Join(inv.rootPath, filepath.FromSlash(dir))
	live, err := sandbox.SurveyReadDir(full, dir, sandbox.SurveyOptions{IncludeHidden: true})
	if err != nil {
		return err
	}
	catalogDir := dir
	if inv.rebased {
		if dir == inv.base {
			catalogDir = "."
		} else {
			catalogDir = catalogRelativePath(dir, inv.base)
		}
	}
	indexed, _ := inv.snapshot.Listing(inv.catalogRoot, catalogDir)
	byName := make(map[string]sourcecatalog.Entry, len(indexed))
	for _, entry := range indexed {
		byName[entry.Name] = entry
	}
	for _, child := range live {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := child.DirEntry.Info()
		if err != nil {
			return err
		}
		rel := child.Rel
		entry, indexed := byName[child.DirEntry.Name()]
		if inv.rebased && indexed {
			entry.Path = path.Join(inv.base, entry.Path)
			entry.Parent = normalizeCatalogPath(path.Dir(entry.Path))
			entry.Depth = catalogPathDepth(entry.Path)
		}
		if !indexed || entry.Size != info.Size() || entry.Mode != uint32(info.Mode()) || !entry.Modified.Equal(info.ModTime().UTC()) {
			entry = sourcecatalog.Entry{RootID: inv.rootID, Path: rel, Parent: normalizeCatalogPath(path.Dir(rel)), Name: child.DirEntry.Name(),
				Depth: catalogPathDepth(rel), IsDir: child.IsDir, IsSymlink: info.Mode()&os.ModeSymlink != 0,
				Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
			if entry.IsDir {
				entry.IsVCSRoot = gitrepo.IsRoot(child.Abs)
			}
		}
		switch visit(entry) {
		case sourcecatalog.WalkStop:
			return errInventoryStop
		case sourcecatalog.WalkSkip:
			continue
		case sourcecatalog.WalkContinue:
		}
		if entry.IsDir && !entry.IsSymlink {
			if err := inv.walkMerged(ctx, rel, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (inv sourceInventory) walkLive(ctx context.Context, base string, visit func(sourcecatalog.Entry) sourcecatalog.WalkStep) error {
	full := filepath.Join(inv.rootPath, filepath.FromSlash(base))
	stopped := false
	err := sandbox.SurveyWalk(ctx, full, sandbox.SurveyOptions{IncludeHidden: true}, func(item sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		info, err := item.DirEntry.Info()
		if err != nil {
			return sandbox.SurveyContinue, err
		}
		rel := path.Join(base, item.Rel)
		entry := sourcecatalog.Entry{RootID: inv.rootID, Path: rel, Parent: normalizeCatalogPath(path.Dir(rel)), Name: path.Base(rel),
			Depth: catalogPathDepth(rel), IsDir: item.IsDir, IsSymlink: item.IsSymlink,
			Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
		if item.IsDir {
			entry.IsVCSRoot = gitrepo.IsRoot(item.Abs)
		}
		switch visit(entry) {
		case sourcecatalog.WalkStop:
			stopped = true
			return sandbox.SurveyStop, nil
		case sourcecatalog.WalkSkip:
			return sandbox.SurveySkipDir, nil
		default:
			return sandbox.SurveyContinue, nil
		}
	})
	if stopped {
		return errInventoryStop
	}
	return err
}

// depth is an entry's depth below the scope; the scope's children are depth 1.
func (inv sourceInventory) depth(entry sourcecatalog.Entry) int {
	return entry.Depth - catalogPathDepth(inv.base)
}

func sourceInventoryForScope(
	ctx context.Context,
	catalog sourceInventoryCatalog,
	projectID string,
	root projectroot.RootRef,
	fullRoot string,
) (sourceInventory, error) {
	base := projectroot.ScopeRel(root, fullRoot)
	svc := catalog
	if svc == nil {
		svc = sourcecatalog.Process()
	}
	whole := sourceInventory{catalog: svc, rootPath: root.Path, catalogRoot: root.ID, catalogDir: base, rootID: root.ID, base: base}
	// Scratch writes have no project watcher; explicitly scoped trees need a fresh walk.
	if root.ID == projectroot.VirtualScratchLabel || (base != "." && svc.BoundaryPath(ctx, root.Path, base, true) != "") {
		scoped := sourceInventory{catalog: svc, rootPath: root.Path, catalogRoot: root.ID + "\x00" + base, catalogDir: ".", rootID: root.ID, base: base, rebased: true}
		snapshot, err := svc.ObserveDependencyScope(ctx, sourcecatalog.Root{ID: scoped.catalogRoot, Path: fullRoot, Within: root.Path})
		if err != nil {
			return sourceInventory{}, err
		}
		return reportedInventory(ctx, scoped, snapshot, false), nil
	}
	if base != "." {
		// Reuse ready parent snapshot to avoid re-crawling subtrees.
		current := svc.Current(ctx, projectID, []sourcecatalog.Root{{ID: root.ID, Path: root.Path}})
		if current.State == sourcecatalog.StateReady {
			return reportedInventory(ctx, whole, current, current.Refreshing), nil
		}
		// Scoped generations avoid full-root reconciliation when parent is cold.
		scoped := sourceInventory{catalog: svc, rootPath: root.Path, catalogRoot: root.ID + "\x00" + base, catalogDir: ".", rootID: root.ID, base: base, rebased: true}
		return snapshotInventory(ctx, svc, projectID, sourcecatalog.Root{ID: scoped.catalogRoot, Path: fullRoot, Within: root.Path}, scoped)
	}
	return snapshotInventory(ctx, svc, projectID, sourcecatalog.Root{ID: root.ID, Path: root.Path}, whole)
}

func snapshotInventory(
	ctx context.Context,
	catalog sourceInventoryCatalog,
	projectID string,
	root sourcecatalog.Root,
	scope sourceInventory,
) (sourceInventory, error) {
	roots := []sourcecatalog.Root{root}
	snapshot, fresh, err := catalog.ObserveWithin(ctx, projectID, roots, safecmd.SurveyCatalogJoin)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			// Serve the last complete generation as stale under continuous write churn.
			current := catalog.Current(ctx, projectID, roots)
			if current.State == sourcecatalog.StateReady {
				return reportedInventory(ctx, scope, current, true), nil
			}
			scope.live = true
			return scope, nil
		}
		return sourceInventory{}, err
	}
	if snapshot.State != sourcecatalog.StateReady {
		current := catalog.Current(ctx, projectID, roots)
		if current.State == sourcecatalog.StateReady {
			return reportedInventory(ctx, scope, current, true), nil
		}
		return sourceInventory{}, fmt.Errorf("source catalog state %q", snapshot.State)
	}
	return reportedInventory(ctx, scope, snapshot, !fresh), nil
}

func reportedInventory(ctx context.Context, scope sourceInventory, snapshot sourcecatalog.Snapshot, stale bool) sourceInventory {
	scope.snapshot = snapshot
	scope.revision = snapshot.Revision
	scope.stale = stale
	reportInventory(ctx, scope)
	return scope
}

func catalogOrProcess(catalog *sourcecatalog.Catalog) *sourcecatalog.Catalog {
	if catalog == nil {
		return sourcecatalog.Process()
	}
	return catalog
}

func catalogPathDepth(rel string) int {
	rel = normalizeCatalogPath(rel)
	if rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func normalizeCatalogPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" || value == "." {
		return "."
	}
	return strings.TrimPrefix(path.Clean("/"+value), "/")
}

func catalogRelativePath(entryPath, base string) string {
	base = normalizeCatalogPath(base)
	if base == "." {
		return entryPath
	}
	return strings.TrimPrefix(entryPath, base+"/")
}

type catalogFileInfo struct{ entry sourcecatalog.Entry }

func (i catalogFileInfo) Name() string       { return i.entry.Name }
func (i catalogFileInfo) Size() int64        { return i.entry.Size }
func (i catalogFileInfo) Mode() fs.FileMode  { return fs.FileMode(i.entry.Mode) }
func (i catalogFileInfo) ModTime() time.Time { return i.entry.Modified }
func (i catalogFileInfo) IsDir() bool        { return i.entry.IsDir }
func (i catalogFileInfo) Sys() any           { return nil }
