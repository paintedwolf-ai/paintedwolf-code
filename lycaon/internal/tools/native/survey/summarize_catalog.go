package survey

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func (g *summarizeGatherer) closeTrees() {
	for _, r := range g.treeReaders {
		_ = r.Close()
	}
	g.treeReaders = nil
	g.treeViews = nil
}

type summaryTreeLookup struct {
	reader *sourcecatalog.SummaryReader
	status sourcecatalog.TreeStatus
}

func (g *summarizeGatherer) openSummaryTree(ctx context.Context, root sourcecatalog.Root, scope sourcecatalog.TreeScope, wait time.Duration) (*sourcecatalog.SummaryReader, sourcecatalog.TreeStatus, error) {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%t", root.ID, root.Path, scope.Key, scope.PruneNestedVCS)
	if view, ok := g.treeViews[key]; ok {
		return view.reader, view.status, nil
	}
	r, status, err := catalogOrProcess(g.catalog).OpenSummary(ctx, g.tctx.ProjectID, root, scope, wait)
	if err != nil {
		return nil, status, err
	}
	if r != nil {
		status = r.Status
		g.treeReaders = append(g.treeReaders, r)
	}
	if g.treeViews == nil {
		g.treeViews = map[string]summaryTreeLookup{}
	}
	g.treeViews[key] = summaryTreeLookup{reader: r, status: status}
	return r, status, nil
}

func (g *summarizeGatherer) buildSubtreeDir(ctx context.Context, resolved projectpaths.Resolved, maxDepth int) *summarize.SubtreeNode {
	scope, err := g.boundary.CompileReadScope(ctx, resolved.Root.Path, g.tctx.ProfileID())
	if err != nil {
		g.treeErr = err
		return nil
	}
	wait := time.Duration(0)
	if !g.indexWaitUsed {
		wait = time.Duration(g.caps.Gather.IndexWaitMs) * time.Millisecond
		g.indexWaitUsed = true
	}
	r, status, err := g.openSummaryTree(ctx, sourcecatalog.Root{ID: resolved.Root.ID, Path: resolved.Root.Path},
		sourcecatalog.TreeScope{Key: scope.Key, Filter: scope.Filter, PruneNestedVCS: g.caps.Gather.PruneNestedVCS}, wait)
	if err != nil {
		g.treeErr = err
		return nil
	}
	g.treeRefreshing = g.treeRefreshing || status.Refreshing
	if r == nil {
		g.treeState = status.State
		if status.State == sourcecatalog.StateFailed {
			g.treeErr = fmt.Errorf("source catalog: %s", status.Error)
			return nil
		}
		return g.warmingSubtree(ctx, resolved)
	}
	if g.treeState == "" {
		g.treeState = sourcecatalog.StateReady
	}
	base, err := filepath.Rel(resolved.Root.Path, resolved.Abs)
	if err != nil {
		g.treeErr = err
		return nil
	}
	base = filepath.ToSlash(base)
	n, err := r.Node(ctx, base)
	if (errors.Is(err, sql.ErrNoRows) || n.Boundary) && base != "." && g.caps.Gather.PruneNestedVCS {
		return g.nestedTree(ctx, resolved, base, scope, maxDepth)
	}
	if err != nil {
		g.treeErr = err
		return nil
	}
	if g.nestedPruneCount != nil {
		*g.nestedPruneCount += n.Pruned
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%t:%d", resolved.Root.ID, scope.Key, g.caps.Gather.PruneNestedVCS, r.Status.Revision)))
	revision := max(1, binary.BigEndian.Uint64(sum[:8])>>11)
	g.noteCatalogRevision(revision)
	preferred := g.seedDocLinkTargets(ctx, resolved.DisplayPath, g.caps.Pack.SubtreeDoclinkMax)
	for i, p := range preferred {
		if target, e := projectpaths.ResolveRead(ctx, g.boundary, g.tctx, p); e == nil && target.Root.ID == resolved.Root.ID {
			if rel, e := filepath.Rel(resolved.Root.Path, target.Abs); e == nil {
				preferred[i] = filepath.ToSlash(rel)
			}
		}
	}
	view := &summaryTreeView{g: g, reader: r, base: base, display: resolved.DisplayPath, revision: revision, preferred: preferred, maxDepth: maxDepth}
	cursor := ""
	if len(g.treeRequest.Paths) <= 1 {
		cursor = g.treeRequest.CursorPosition
	}
	root := view.node(n, 0, cursor)
	root.LoadChildren(ctx, root)
	return root
}

type summaryTreeView struct {
	g         *summarizeGatherer
	reader    *sourcecatalog.SummaryReader
	base      string
	display   string
	revision  uint64
	preferred []string
	maxDepth  int
}

func (v *summaryTreeView) displayPath(rel string) string {
	if rel == "" {
		return ""
	}
	if rel == v.base {
		return v.display
	}
	return path.Join(v.display, catalogRelativePath(rel, v.base))
}

func (v *summaryTreeView) node(n sourcecatalog.TreeNode, depth int, cursor string) *summarize.SubtreeNode {
	kind := summarize.SubtreeKindFile
	if n.IsDir {
		kind = summarize.SubtreeKindDir
	}
	out := &summarize.SubtreeNode{Path: v.displayPath(n.Path), Kind: kind, Revision: v.revision, ChildCount: n.Children,
		Material: summarize.Material{SourceFiles: n.Files, Bytes: n.Bytes}, Representative: v.displayPath(n.Representative)}
	if kind != summarize.SubtreeKindDir {
		out.RollupOnly = n.IsSymlink || !os.FileMode(n.Mode).IsRegular()
		return out
	}
	if depth >= v.maxDepth {
		out.RollupOnly = true
		return out
	}
	var once sync.Once
	var children []*summarize.SubtreeNode
	var remainder *summarize.SubtreeRemainder
	out.LoadChildren = func(ctx context.Context, target *summarize.SubtreeNode) {
		once.Do(func() {
			limit := min(v.g.caps.Pack.SubtreeFanoutMax, v.g.caps.Gather.MetadataNodes)
			if limit <= 0 || limit > v.g.caps.Gather.MetadataNodes-v.g.treeNodes {
				remainder = &summarize.SubtreeRemainder{Children: n.Children, Material: out.Material}
				return
			}
			page, err := v.reader.Page(ctx, n, cursor, v.g.treeRequest.Task, v.preferred, limit)
			if err != nil {
				v.g.treeErr = err
				return
			}
			v.g.treeNodes += len(page.Nodes)
			for _, child := range page.Nodes {
				children = append(children, v.node(child, depth+1, ""))
			}
			if page.RemainingChildren > 0 {
				remainder = &summarize.SubtreeRemainder{Children: page.RemainingChildren, Material: summarize.Material{SourceFiles: page.RemainingFiles, Bytes: page.RemainingBytes}, Next: page.Next}
			}
		})
		target.Children = children
		target.Remainder = remainder
	}
	return out
}

// warmingSubtree provides bounded live orientation while exact counts are unknown.
func (g *summarizeGatherer) warmingSubtree(ctx context.Context, resolved projectpaths.Resolved) *summarize.SubtreeNode {
	root := &summarize.SubtreeNode{Path: resolved.DisplayPath, Kind: summarize.SubtreeKindDir, UnknownMaterial: true}
	limit := min(g.caps.Pack.SubtreeFanoutMax, g.caps.Gather.MetadataNodes-g.treeNodes, g.caps.Gather.MetadataNodes-g.directoryEntries)
	if limit <= 0 {
		return root
	}
	f, err := g.openRead(ctx, resolved.Abs)
	if err != nil {
		g.treeErr = err
		return nil
	}
	defer func() { _ = f.Close() }()
	seen := map[string]bool{}
	add := func(name string) {
		if len(root.Children) >= limit || seen[name] {
			return
		}
		seen[name] = true
		if child := g.warmingChild(ctx, resolved, name); child != nil {
			root.Children = append(root.Children, child)
			g.treeNodes++
		}
	}
	for _, surface := range docSeedSurfaces {
		if path.Dir(surface) == "." {
			add(surface)
		}
	}
	g.directoriesOpened++
	entries, readErr := f.ReadDir(limit)
	g.directoryEntries += len(entries)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		g.treeErr = readErr
		return nil
	}
	for _, entry := range entries {
		add(entry.Name())
	}

	return root
}

func (g *summarizeGatherer) nestedTree(ctx context.Context, resolved projectpaths.Resolved, base string, scope sandbox.CompiledReadScope, maxDepth int) *summarize.SubtreeNode {
	filter := func(rel string, isDir bool) bool {
		return scope.Filter == nil || scope.Filter(path.Join(base, rel), isDir)
	}
	r, status, err := g.openSummaryTree(ctx, sourcecatalog.Root{ID: resolved.Root.ID + ":" + base, Path: resolved.Abs, Within: resolved.Root.Path}, sourcecatalog.TreeScope{Key: scope.Key, Filter: filter, PruneNestedVCS: true}, 0)
	if err != nil {
		g.treeErr = err
		return nil
	}
	g.treeRefreshing = g.treeRefreshing || status.Refreshing
	if r == nil {
		g.treeState = status.State
		if status.State == sourcecatalog.StateFailed {
			g.treeErr = fmt.Errorf("source catalog: %s", status.Error)
			return nil
		}
		return g.warmingSubtree(ctx, resolved)
	}
	n, err := r.Node(ctx, ".")
	if err != nil {
		g.treeErr = err
		return nil
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%t:%d", resolved.Root.ID, base, scope.Key, g.caps.Gather.PruneNestedVCS, r.Status.Revision)))
	revision := max(1, binary.BigEndian.Uint64(sum[:8])>>11)
	g.noteCatalogRevision(revision)
	view := &summaryTreeView{g: g, reader: r, base: ".", display: resolved.DisplayPath, revision: revision, maxDepth: maxDepth}
	cursor := ""
	if len(g.treeRequest.Paths) <= 1 {
		cursor = g.treeRequest.CursorPosition
	}
	root := view.node(n, 0, cursor)
	root.LoadChildren(ctx, root)
	return root
}

func (g *summarizeGatherer) warmingChild(ctx context.Context, resolved projectpaths.Resolved, name string) *summarize.SubtreeNode {
	display := path.Join(resolved.DisplayPath, name)
	if sandbox.ShouldSkipDir(display, name) {
		return nil
	}
	child, err := projectpaths.ResolveRead(ctx, g.boundary, g.tctx, display)
	if err != nil {
		return nil
	}
	info, err := os.Lstat(child.Abs)
	if err != nil {
		return nil
	}
	if info.IsDir() && g.caps.Gather.PruneNestedVCS && gitrepo.IsRoot(child.Abs) {
		g.noteNestedRepoPruned(child.Abs)
		return nil
	}
	n := &summarize.SubtreeNode{Path: display, Kind: summarize.SubtreeKindFile, Material: summarize.Material{SourceFiles: 1, Bytes: info.Size()}}
	if !info.Mode().IsRegular() {
		n.Material = summarize.Material{}
		n.RollupOnly = true
	}
	if info.IsDir() {
		n.UnknownMaterial = true
		n.Kind = summarize.SubtreeKindDir
		n.Material = summarize.Material{}
		n.RollupOnly = false
		var once sync.Once
		var children []*summarize.SubtreeNode
		n.LoadChildren = func(ctx context.Context, target *summarize.SubtreeNode) {
			once.Do(func() {
				if branch := g.warmingSubtree(ctx, child); branch != nil {
					children = branch.Children
				}
			})
			target.Children = children
		}
	}
	return n
}
