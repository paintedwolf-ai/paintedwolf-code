package repomap

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// tagsSnapshot fills a "tags" view from already-parsed files: a flat definition
// list, paginated when it overflows the byte budget.
func tagsSnapshot(ctx context.Context, snap *Snapshot, files []scannedFile, opts Options, maxBytes int, skip SkipStats) *Snapshot {
	var all []Tag
	for _, f := range files {
		all = append(all, f.tags...)
	}
	sortTags(all)
	snap.View = "tags"
	snap.TotalDefs = len(all)

	if len(all) == 0 {
		snap.Tags = []Tag{}
		snap.Diagnostics = emptyDiagnostics(ctx, snap)
		snap.Diagnostics.SkipReasons = skip
		return snap
	}
	assembleTagsView(snap, all, opts.Offset, maxBytes)
	orderTagsForTask(ctx, opts, snap.Tags)
	snap.Diagnostics = tagsDiagnostics(snap, skip)
	return snap
}

func assembleTagsView(snap *Snapshot, all []Tag, offset, maxBytes int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(all) {
		offset = len(all)
	}
	snap.Offset = offset
	packed, truncated := packTags(all[offset:], maxBytes)
	if packed == nil {
		packed = []Tag{}
	}
	snap.Tags = packed
	snap.Truncated = truncated
	if truncated && len(packed) > 0 {
		next := offset + len(packed)
		snap.NextOffset = &next
	} else if truncated && len(packed) == 0 && offset < len(all) {
		next := offset + 1
		snap.NextOffset = &next
	}
}

// packTags greedily takes definitions until the byte budget is spent, always
// including at least one. Reports whether any were left out.
func packTags(tags []Tag, maxBytes int) (out []Tag, truncated bool) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	approx := 0
	for _, tag := range tags {
		est := tagBytes(tag)
		if len(out) > 0 && approx+est > maxBytes {
			return out, true
		}
		approx += est
		out = append(out, tag)
	}
	return out, false
}

// tagsOverflowBudget reports whether the full tag list cannot be packed within
// maxBytes — i.e. a flat symbol view at this scope would truncate.
func tagsOverflowBudget(all []Tag, maxBytes int) bool {
	_, truncated := packTags(all, maxBytes)
	return truncated
}

func assembleMapView(snap *Snapshot, cands []candidate, scopeRel string, depth, maxBytes int) {
	snap.View = "map"
	root := buildStructTree(cands, scopeRel)
	budget := maxBytes
	snap.Tree = renderNode(root, 0, depth, &budget)
}

// aggNode is a directory or file in the structural rollup, carrying the source
// file count, byte size, and languages of its whole subtree.
type aggNode struct {
	path     string
	isFile   bool
	files    int
	bytes    int64
	langs    map[string]bool
	children map[string]*aggNode
}

func newAgg(path string) *aggNode {
	return &aggNode{path: path, langs: map[string]bool{}, children: map[string]*aggNode{}}
}

func (a *aggNode) add(size int64, lang string) {
	a.files++
	a.bytes += size
	if lang != "" {
		a.langs[lang] = true
	}
}

func buildStructTree(cands []candidate, scopeRel string) *aggNode {
	root := newAgg(scopeRel)
	for _, c := range cands {
		segs, paths := segmentsUnderScope(c.rel, scopeRel)
		if len(segs) == 0 {
			continue
		}
		root.add(c.size, c.lang)
		cur := root
		for i, seg := range segs {
			child := cur.children[seg]
			if child == nil {
				child = newAgg(paths[i])
				child.isFile = i == len(segs)-1
				cur.children[seg] = child
			}
			child.add(c.size, c.lang)
			cur = child
		}
	}
	return root
}

// segmentsUnderScope returns the path segments of rel below scopeRel, paired
// with their full repo-relative paths.
func segmentsUnderScope(rel, scopeRel string) (segs []string, paths []string) {
	full := strings.Split(rel, "/")
	start := 0
	if scopeRel != "." && scopeRel != "" {
		start = strings.Count(scopeRel, "/") + 1
	}
	if start > len(full) {
		return nil, nil
	}
	for i := start; i < len(full); i++ {
		segs = append(segs, full[i])
		paths = append(paths, strings.Join(full[:i+1], "/"))
	}
	return segs, paths
}

// renderNode emits a directory/file rollup, expanding the densest children
// first (by source file count) until the byte budget — or depth cap, if depth
// > 0 — is reached. Children that could not be expanded are listed as shallow
// rollups flagged ZoomIn.
func renderNode(a *aggNode, depth, maxDepth int, budget *int) *Node {
	n := rollup(a)
	*budget -= nodeCost(a)
	if a.isFile || len(a.children) == 0 {
		return n
	}
	if maxDepth > 0 && depth >= maxDepth {
		n.ZoomIn = true
		return n
	}
	for _, child := range sortedAggChildren(a.children) {
		if *budget <= 0 {
			r := rollup(child)
			if !child.isFile && len(child.children) > 0 {
				r.ZoomIn = true
			}
			n.Children = append(n.Children, r)
			continue
		}
		n.Children = append(n.Children, renderNode(child, depth+1, maxDepth, budget))
	}
	return n
}

func rollup(a *aggNode) *Node {
	n := &Node{Path: a.path, Bytes: a.bytes, Langs: sortedSet(a.langs)}
	if a.isFile {
		n.Type = "file"
	} else {
		n.Type = "dir"
		n.Files = a.files
	}
	return n
}

func sortedAggChildren(m map[string]*aggNode) []*aggNode {
	out := make([]*aggNode, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].files != out[j].files {
			return out[i].files > out[j].files
		}
		if out[i].isFile != out[j].isFile {
			return !out[i].isFile // directories before files at equal weight
		}
		return out[i].path < out[j].path
	})
	return out
}

func nodeCost(a *aggNode) int {
	cost := len(a.path) + 56
	for l := range a.langs {
		cost += len(l) + 4
	}
	return cost
}

func emptyDiagnostics(ctx context.Context, snap *Snapshot) *Diagnostics {
	// FilesScanned == 0: the walk saw no in-scope files, hidden included.
	code := noFilesHintCode
	if snap.FilesScanned > 0 {
		code = emptyRepoMapHintCode
	}
	return &Diagnostics{
		Hint:     envelopeHint(ctx, code),
		HintCode: code,
	}
}

func tagsDiagnostics(snap *Snapshot, skip SkipStats) *Diagnostics {
	if !snap.Truncated && skip == (SkipStats{}) {
		return nil
	}
	d := &Diagnostics{SkipReasons: skip}
	if snap.Truncated && snap.NextOffset != nil {
		d.Hint = "More definitions than fit the budget — page with offset=" +
			strconv.Itoa(*snap.NextOffset) + ", or scope path to a subdirectory."
	}
	return d
}

func mapDiagnostics(snap *Snapshot, skip SkipStats) *Diagnostics {
	return &Diagnostics{
		SkipReasons: skip,
		Hint: "Directory map covers " + strconv.Itoa(snap.SourceFiles) +
			" files. Scope to a listed path or zoom_in node for symbols.",
	}
}

func sortedSet(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
