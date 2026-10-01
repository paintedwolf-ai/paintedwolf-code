package survey

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// sourceInventory is one scope of a catalog generation. Entries reach callers
// through walk, addressed relative to the project root.
type sourceInventory struct {
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
}

// walk visits the scope depth-first. A skipped directory costs one visit, so
// pruning keeps a walk proportional to what it admits.
func (inv sourceInventory) walk(ctx context.Context, visit func(sourcecatalog.Entry) sourcecatalog.WalkStep) error {
	if !inv.rebased {
		return inv.snapshot.Walk(ctx, inv.catalogRoot, inv.catalogDir, visit)
	}
	return inv.snapshot.Walk(ctx, inv.catalogRoot, inv.catalogDir, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		entry.RootID = inv.rootID
		entry.Path = path.Join(inv.base, entry.Path)
		entry.Parent = normalizeCatalogPath(path.Dir(entry.Path))
		entry.Depth = catalogPathDepth(entry.Path)
		return visit(entry)
	})
}

// depth is an entry's depth below the scope; the scope's children are depth 1.
func (inv sourceInventory) depth(entry sourcecatalog.Entry) int {
	return entry.Depth - catalogPathDepth(inv.base)
}

func sourceInventoryForScope(
	ctx context.Context,
	catalog *sourcecatalog.Catalog,
	projectID string,
	root projectroot.RootRef,
	fullRoot string,
) (sourceInventory, error) {
	base := projectroot.ScopeRel(root, fullRoot)
	svc := catalogOrProcess(catalog)
	whole := sourceInventory{catalogRoot: root.ID, catalogDir: base, rootID: root.ID, base: base}
	if base != "." {
		// Reuse ready parent snapshot to avoid re-crawling subtrees.
		current := svc.Current(ctx, projectID, []sourcecatalog.Root{{ID: root.ID, Path: root.Path}})
		if current.State == sourcecatalog.StateReady {
			return reportedInventory(ctx, whole, current, current.Refreshing), nil
		}
		// Scoped generations avoid full-root reconciliation when parent is cold.
		scoped := sourceInventory{catalogRoot: root.ID + "\x00" + base, catalogDir: ".", rootID: root.ID, base: base, rebased: true}
		return snapshotInventory(ctx, svc, projectID, sourcecatalog.Root{ID: scoped.catalogRoot, Path: fullRoot, Within: root.Path}, scoped)
	}
	return snapshotInventory(ctx, svc, projectID, sourcecatalog.Root{ID: root.ID, Path: root.Path}, whole)
}

func snapshotInventory(
	ctx context.Context,
	catalog *sourcecatalog.Catalog,
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
			return sourceInventory{}, safecmd.Reject("SURVEY_INVENTORY_WARMING", map[string]any{
				"timeout_ms": safecmd.SurveyCatalogJoin.Milliseconds(),
			})
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
