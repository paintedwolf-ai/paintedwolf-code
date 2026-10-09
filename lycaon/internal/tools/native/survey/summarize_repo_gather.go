package survey

import (
	"context"
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type repoGather struct {
	structure     []summarize.StructureCandidate
	pathIsFile    bool
	pathIsDir     bool
	bytes         int
	matchCount    int
	sampleMatches []summarize.PatternMatchSample
}

func (g *summarizeGatherer) gatherRepo(ctx context.Context, req summarize.Request) (repoGather, error) {
	budget := &structureBudget{
		maxFiles: g.caps.Gather.MaxFilesRead,
		maxBytes: g.caps.Gather.MaxBytes,
	}

	if req.Pattern != "" {
		pattern, err := g.gatherPatternRepo(ctx, req, budget)
		return repoGather{
			structure: pattern.structure, pathIsDir: true, bytes: budget.bytes,
			matchCount: pattern.matchCount, sampleMatches: pattern.sampleMatches,
		}, err
	}

	targets := req.Paths
	if len(targets) == 0 && req.Path != "" {
		targets = []string{req.Path}
	}
	multi := len(targets) > 1
	var out repoGather
	var missing []string
	for _, target := range targets {
		resolved, rerr := g.access.reads.Resolve(ctx, target)
		if rerr != nil {
			var reject *tools.ToolReject
			if errors.As(rerr, &reject) {
				return repoGather{}, rerr
			}
			missing = append(missing, target)
			continue
		}
		info, serr := os.Stat(resolved.Abs)
		if serr != nil {
			if os.IsNotExist(serr) {
				missing = append(missing, target)
				continue
			}
			return repoGather{}, serr
		}
		if info.IsDir() {
			out.pathIsDir = true
			if !multi {
				g.gatherStructureDir(ctx, resolved, &out.structure)
			}
			continue
		}
		out.pathIsFile = true
		if budget.full() {
			continue
		}
		if sc, n, ok := g.sources.structureFromAbs(ctx, resolved.Abs, resolved.DisplayPath); ok {
			out.structure = append(out.structure, sc)
			budget.spend(n)
		}
	}
	if multi {
		out.pathIsFile = false
	}
	if len(missing) > 0 {
		return repoGather{}, &tools.ToolReject{
			Code: "SUMMARIZE_PATHS_MISSING",
			Data: map[string]any{"missing_paths": missing},
		}
	}
	out.bytes = budget.bytes
	return out, nil
}

type patternGather struct {
	structure     []summarize.StructureCandidate
	matchCount    int
	sampleMatches []summarize.PatternMatchSample
}

func (g *summarizeGatherer) gatherPatternRepo(
	ctx context.Context,
	req summarize.Request,
	budget *structureBudget,
) (patternGather, error) {
	roots := req.Paths
	if len(roots) == 0 {
		root := req.Path
		if root == "" {
			root = "."
		}
		roots = []string{root}
	}
	pageSize := g.caps.Gather.PatternPageFiles
	if g.caps.Gather.MaxFilesRead > 0 && (pageSize <= 0 || g.caps.Gather.MaxFilesRead < pageSize) {
		pageSize = g.caps.Gather.MaxFilesRead
	}
	probe, err := g.patterns.probePatternPage(ctx, req, roots, pageSize)
	if err != nil {
		return patternGather{}, err
	}
	if probe.MatchCount == 0 && req.Cursor == "" && probe.NextPath == "" && g.trees.treeState != sourcecatalog.StateWarming && !g.sources.sourceLimited {
		return patternGather{}, patternNoMaterialReject(ctx, g.patterns, roots, req.Pattern, probe.SkippedPaths)
	}
	for _, path := range probe.SkippedPaths {
		g.sources.noteSkippedPath(path)
	}
	g.patterns.patternFilesTotal = probe.MatchingFiles
	g.trees.catalogRevision = probe.Revision
	g.patterns.patternNextPath = probe.NextPath
	g.patterns.patternCursorFound = probe.CursorFound
	samples := patternMatchSamples(probe.Matches, g.caps.Gather.PatternMatchSampleMax)
	byPath := map[string][]grepMatch{}
	var paths []string
	for _, match := range probe.Matches {
		if _, ok := byPath[match.Path]; !ok {
			paths = append(paths, match.Path)
		}
		byPath[match.Path] = append(byPath[match.Path], match)
	}
	var structure []summarize.StructureCandidate
	for _, display := range paths {
		if budget.full() {
			break
		}
		sc, n, err := g.patterns.structureFromPathPattern(ctx, display, req.Pattern, byPath[display])
		if errors.Is(err, errSummaryReadBudget) {
			break
		}
		if err != nil {
			return patternGather{}, err
		}
		structure = append(structure, sc)
		budget.spend(n)
	}
	return patternGather{structure: structure, matchCount: probe.MatchCount, sampleMatches: samples}, nil
}

type structureBudget struct {
	maxFiles int
	maxBytes int
	files    int
	bytes    int
}

func (b *structureBudget) full() bool {
	if b.maxFiles > 0 && b.files >= b.maxFiles {
		return true
	}
	if b.maxBytes > 0 && b.bytes >= b.maxBytes {
		return true
	}
	return false
}

func (b *structureBudget) spend(n int) {
	b.files++
	b.bytes += n
}

func (g *summarizeGatherer) gatherStructureDir(ctx context.Context, resolved projectpaths.Resolved, out *[]summarize.StructureCandidate) {
	target := resolved.DisplayPath
	if target == "" {
		target = "."
	}
	root := g.trees.buildSubtreeForTarget(ctx, target)
	if dm := sourceDirMapCandidate(root); dm.RelPath != "" {
		*out = append(*out, dm)
	}
}
