package repomap

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/lycaon/lycaon/internal/bundledhint"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// buildShallowRootMap maps immediate children without parsing.
func buildShallowRootMap(
	ctx context.Context,
	snap *Snapshot,
	walkRoot, scopeRel string,
	opts Options,
) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(walkRoot)
	if err != nil {
		return nil, err
	}
	children := make([]*Node, 0, len(entries))
	filesScanned := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := e.Name()
		if name == "" || name == "." || name == ".." {
			continue
		}
		if sandbox.ShouldSkipDirBaseName(name) {
			continue
		}
		rel := name
		if scopeRel != "" && scopeRel != "." {
			rel = filepath.ToSlash(filepath.Join(scopeRel, name))
		}
		if opts.PathIncluded != nil && !opts.PathIncluded(rel, e.IsDir()) {
			continue
		}
		childAbs := filepath.Join(walkRoot, name)
		if e.IsDir() && opts.PruneNestedVCS && gitrepo.IsRoot(childAbs) {
			if opts.OnNestedRepoPruned != nil {
				opts.OnNestedRepoPruned(childAbs)
			}
			continue
		}
		filesScanned++
		if e.IsDir() {
			children = append(children, &Node{
				Path:   rel,
				Type:   "dir",
				Files:  shallowImmediateCount(ctx, childAbs, rel, opts),
				ZoomIn: true,
			})
			continue
		}
		var size int64
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
		}
		children = append(children, &Node{
			Path:  rel,
			Type:  "file",
			Bytes: size,
			Files: 1,
		})
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].Path < children[j].Path
	})
	snap.View = "map"
	snap.FilesScanned = filesScanned
	snap.SourceFiles = filesScanned
	snap.Tree = &Node{
		Path:     scopeRel,
		Type:     "dir",
		Files:    filesScanned,
		Children: children,
		ZoomIn:   true,
	}
	snap.Diagnostics = shallowMapDiagnostics(ctx, filesScanned)
	return snap, nil
}

// shallowImmediateCount counts direct visible entries.
func shallowImmediateCount(ctx context.Context, abs, parentRel string, opts Options) int {
	if err := ctx.Err(); err != nil {
		return 1
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return 1
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if name == "" || name == "." || name == ".." || sandbox.ShouldSkipDirBaseName(name) {
			continue
		}
		rel := name
		if parentRel != "" && parentRel != "." {
			rel = filepath.ToSlash(filepath.Join(parentRel, name))
		}
		if opts.PathIncluded != nil && !opts.PathIncluded(rel, e.IsDir()) {
			continue
		}
		if e.IsDir() && opts.PruneNestedVCS {
			if gitrepo.IsRoot(filepath.Join(abs, name)) {
				continue
			}
		}
		n++
	}
	if n == 0 {
		return 1
	}
	return n
}

// shallowMapDiagnostics states emptiness outright, or the drill-down affordance.
func shallowMapDiagnostics(ctx context.Context, entryCount int) *Diagnostics {
	if entryCount == 0 {
		return &Diagnostics{
			Hint:     bundledhint.Message(ctx, noFilesHintCode, nil),
			HintCode: noFilesHintCode,
		}
	}
	return &Diagnostics{
		Hint: "Shallow directory map of " + strconv.Itoa(entryCount) +
			" top-level entries. Scope to a listed directory for detail.",
	}
}

