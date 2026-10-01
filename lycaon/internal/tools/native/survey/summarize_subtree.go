package survey

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func (g *summarizeGatherer) buildSubtreeForTarget(ctx context.Context, target string) *summarize.SubtreeNode {
	if g == nil || strings.TrimSpace(target) == "" {
		return nil
	}
	g.memoMu.Lock()
	if cached := g.subtreeByPath[target]; cached != nil {
		g.memoMu.Unlock()
		return summarize.CloneSubtree(cached)
	}
	g.memoMu.Unlock()
	resolved, err := projectpaths.ResolveRead(ctx, g.boundary, g.tctx, target)
	if err != nil {
		return nil
	}
	info, err := os.Stat(resolved.Abs)
	if err != nil {
		return nil
	}
	maxDepth := g.caps.Gather.MaxListDepth
	var root *summarize.SubtreeNode
	if info.IsDir() {
		root = g.buildSubtreeDir(ctx, resolved, maxDepth)
	} else {
		root = g.buildSubtreeFile(ctx, resolved.DisplayPath, resolved.Abs, maxDepth)
	}
	if root != nil {
		root.CatalogRootID = resolved.Root.ID
		g.memoMu.Lock()
		if g.subtreeByPath == nil {
			g.subtreeByPath = map[string]*summarize.SubtreeNode{}
		}
		g.subtreeByPath[target] = summarize.CloneSubtree(root)
		g.memoMu.Unlock()
	}
	return root
}

func (g *summarizeGatherer) buildSubtreeForTargets(ctx context.Context, targets []string) *summarize.SubtreeNode {
	root := &summarize.SubtreeNode{
		Path: ".", Kind: summarize.SubtreeKindDir,
	}
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		child := g.buildSubtreeForTarget(ctx, target)
		if child == nil || child.Path == "" || seen[child.Path] {
			continue
		}
		seen[child.Path] = true
		root.Children = append(root.Children, child)
		root.ScopePaths = append(root.ScopePaths, target)
	}
	if len(root.Children) == 0 {
		return nil
	}
	sort.Slice(root.Children, func(i, j int) bool { return root.Children[i].Path < root.Children[j].Path })
	kept := root.Children[:0]
	parents := map[string]map[string]bool{}
	for _, child := range root.Children {
		dirs := parents[child.CatalogRootID]
		covered := false
		for p := path.Dir(child.Path); ; p = path.Dir(p) {
			if dirs[p] {
				covered = true
				break
			}
			if path.Dir(p) == p {
				break
			}
		}
		if covered {
			continue
		}
		kept = append(kept, child)
		if child.Kind == summarize.SubtreeKindDir {
			if dirs == nil {
				dirs = map[string]bool{}
				parents[child.CatalogRootID] = dirs
			}
			dirs[child.Path] = true
		}
	}
	root.Children = kept
	summarize.SumMaterialBottomUp(root)
	g.ensureTargetCursorRevision(ctx, root)
	root.Revision = g.catalogRevision
	return root
}

func (g *summarizeGatherer) ensureTargetCursorRevision(_ context.Context, root *summarize.SubtreeNode) {
	if root == nil {
		return
	}
	h := sha256.New()
	for _, child := range root.Children {
		_, _ = fmt.Fprintf(h, "%s:%d:%d:%d\n", child.Path, child.Material.SourceFiles, child.Material.Bytes, child.Revision)
	}
	g.catalogRevision = max(1, binary.BigEndian.Uint64(h.Sum(nil)[:8])>>11)
}

func (g *summarizeGatherer) noteCatalogRevision(revision uint64) {
	if revision > g.catalogRevision {
		g.catalogRevision = revision
	}
}

func (g *summarizeGatherer) buildSubtreeFile(_ context.Context, display, abs string, maxDepth int) *summarize.SubtreeNode {
	var bytes int64
	var revision uint64
	if info, err := os.Stat(abs); err == nil {
		bytes = info.Size()
		revision = uint64(info.ModTime().UnixNano())
	}
	root := summarize.BuildSubtree(summarize.StructuralChild{
		Path: display, Kind: summarize.SubtreeKindFile,
		SourceFiles: 1, Bytes: bytes,
	}, maxDepth)
	root.Revision = revision
	return root
}

func sourceDirMapCandidate(root *summarize.SubtreeNode) summarize.StructureCandidate {
	if root == nil || root.Kind != summarize.SubtreeKindDir {
		return summarize.StructureCandidate{}
	}
	if root.Material.SourceFiles == 0 && !root.UnknownMaterial {
		return summarize.StructureCandidate{}
	}
	children := append([]*summarize.SubtreeNode(nil), root.Children...)
	sort.Slice(children, func(i, j int) bool { return children[i].Path < children[j].Path })
	tags := make([]string, 0, min(len(children), 64)+1)
	if root.UnknownMaterial {
		tags = append(tags, "directory counts pending indexing")
	} else {
		tags = append(tags, fmt.Sprintf("files=%d bytes=%d", root.Material.SourceFiles, root.Material.Bytes))
	}
	for _, child := range children[:min(len(children), 64)] {
		if child.UnknownMaterial {
			tags = append(tags, child.Path+" counts pending indexing")
		} else {
			tags = append(tags, fmt.Sprintf("%s files=%d bytes=%d", child.Path, child.Material.SourceFiles, child.Material.Bytes))
		}
	}
	return summarize.StructureCandidate{
		RelPath: root.Path, Kind: summarize.StructureKindDirMap,
		RollupRows:  tags,
		ContentHash: summarize.HashString(root.Path + "\n" + strings.Join(tags, "\n")),
	}
}
