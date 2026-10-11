package survey

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/summarize"
)

func (g *summarizeGatherer) stampWork(res *summarize.Result) {
	res.Orchestration.Curator.DirectoryEntriesRead = g.trees.directoryEntries
	res.Orchestration.Curator.DirectoriesOpened = g.trees.directoriesOpened
	for _, r := range g.trees.treeReaders {
		res.Orchestration.Curator.MetadataRowsRead += r.RowsRead
	}
	res.Orchestration.Curator.SourceFilesRead = g.sources.sourceFiles
	res.Orchestration.Curator.SourceBytesRead = g.sources.sourceReadBytes
	if len(g.sources.skippedPaths) > 0 {
		res.Coverage.Complete = false
		res.Pack.Gaps = append(res.Pack.Gaps, fmt.Sprintf("Source detail omitted %d unreadable, non-text or budget-limited paths; this pack does not establish their contents.", len(g.sources.skippedPaths)))
	}
	if g.sources.sourceLimited {
		res.Pack.Gaps = append(res.Pack.Gaps, "Source detail reached the read budget; summarize a narrower path for more detail.")
	}
}

var _ summarize.Gatherer = (*summarizeGatherer)(nil)

func (g *summarizeGatherer) Gather(ctx context.Context, req summarize.Request) (summarize.GatherResult, error) {
	g.trees.treeRequest = req
	g.sources.task = req.Task
	mode := summarize.ResolveMode(strings.TrimSpace(req.Content) != "", req.HasRepoKeys())
	res := summarize.GatherResult{Mode: mode}
	g.patterns.patternFilesTotal = 0
	g.patterns.patternNextPath = ""
	g.patterns.patternCursorFound = false

	var nestedPruned int
	g.nested.nestedPruneCount = &nestedPruned
	g.nested.nestedPruneSeen = nil
	var faninPasses int
	g.relations.faninGrepPasses = &faninPasses
	defer func() {
		g.nested.nestedPruneCount = nil
		g.nested.nestedPruneSeen = nil
		g.relations.faninGrepPasses = nil
	}()

	if req.Content != "" {
		outline := fileoutline.AnalyzeText(ctx, inlineOutlineHint(req.Content), []byte(req.Content))
		g.sources.noteParseFailure("inline", "source", outline.ParseFailure)
		if len(outline.Symbols) > 0 || (outline.Parses != nil && *outline.Parses) {
			sc := summarize.StructureCandidate{
				RelPath:   "inline",
				Kind:      summarize.StructureKindInline,
				Head:      headLines(req.Content, g.caps.Gather.FileHeadLines),
				StartLine: 1,
				LineCount: outline.TotalLines,
			}
			if sc.LineCount <= 0 {
				sc.LineCount = strings.Count(req.Content, "\n") + 1
			}
			lines := strings.Split(req.Content, "\n")
			for _, sym := range outline.Symbols {
				sc.Symbols = append(sc.Symbols, summarize.StructureSymbol{
					Kind: sym.Kind, Name: sym.Name, Line: sym.Line,
					Signature: summarize.SignatureLine(lines, sym.Line), Doc: summarize.LeadingComment(lines, sym.Line),
				})
			}
			sc = summarize.FocusSource(ctx, g.rerank, req.Task, sc, req.Content, g.caps.Gather.FileHeadLines)
			sc.ContentHash = structureContentHashPattern(sc, "")
			res.Structure = append(res.Structure, sc)
			res.Bytes += len(req.Content)
		} else {
			for _, c := range chunkInline(req.Content, g.caps.Gather.InlineChunkLines) {
				res.Candidates = append(res.Candidates, c)
				res.Bytes += len(c.Body)
			}
		}
	}

	stats := summarize.GatherStats{Mode: mode, HasPattern: req.Pattern != ""}
	if req.HasRepoKeys() {
		repo, err := g.gatherRepo(ctx, req)
		if err != nil {
			return summarize.GatherResult{}, err
		}
		res.Structure = append(res.Structure, repo.structure...)
		res.Bytes += repo.bytes
		res.MatchCount = repo.matchCount
		res.SampleMatches = repo.sampleMatches
		stats.PathIsFile = repo.pathIsFile
		stats.PathIsDir = repo.pathIsDir
		if req.Pattern == "" {
			target := req.Path
			if target == "" && len(req.Paths) == 1 {
				target = req.Paths[0]
			}
			if target != "" {
				res.Subtree = g.trees.buildSubtreeForTarget(ctx, target)
				res.Importance, err = g.relations.computeSubtreeImportance(ctx, target, res.Subtree, res.Structure)
				if err != nil {
					return summarize.GatherResult{}, fmt.Errorf("summarize fan-in leads: %w", err)
				}
			} else if len(req.Paths) > 1 {
				res.Subtree = g.trees.buildSubtreeForTargets(ctx, req.Paths)
				res.Importance, err = g.relations.computeSubtreeImportance(ctx, ".", res.Subtree, res.Structure)
				if err != nil {
					return summarize.GatherResult{}, fmt.Errorf("summarize fan-in leads: %w", err)
				}
			}
		}
	}

	if len(res.Structure) > 0 {
		stats.UseStructure = true
	}
	if stats.UseStructure {
		stats.Candidates = len(res.Structure)
	} else {
		stats.Candidates = len(res.Candidates)
	}
	stats.NestedReposPruned = nestedPruned
	stats.FaninGrepPasses = faninPasses
	res.Stats = stats
	fit, fitErr := g.relations.gatherFitEdges(ctx, req, res.Structure, stats)
	if fitErr != nil {
		return summarize.GatherResult{}, fmt.Errorf("summarize reference leads: %w", fitErr)
	}
	res.Fit = fit
	res.NextActions = g.patterns.patternInspectionActions(ctx, req, nil)
	res.CatalogRevision = g.trees.catalogRevision
	res.MatchingFilesObserved = g.patterns.patternFilesTotal
	res.CursorFound = g.patterns.patternCursorFound
	res.NextCursorPath = g.patterns.patternNextPath
	res.CatalogState = string(g.trees.treeState)
	res.CatalogRefreshing = g.trees.treeRefreshing
	if res.Subtree != nil && res.Subtree.LoadChildren != nil && req.CursorPosition != "" {
		res.CursorFound = true
	}
	if err := ctx.Err(); err != nil {
		return summarize.GatherResult{}, err
	}
	if g.trees.treeErr != nil {
		return summarize.GatherResult{}, g.trees.treeErr
	}
	return res, nil
}

func chunkInline(content string, chunkLines int) []summarize.Candidate {
	if chunkLines <= 0 {
		chunkLines = 200
	}
	lines := strings.Split(content, "\n")
	var cands []summarize.Candidate
	multi := len(lines) > chunkLines
	for i := 0; i < len(lines); i += chunkLines {
		end := i + chunkLines
		if end > len(lines) {
			end = len(lines)
		}
		body := strings.Join(lines[i:end], "\n")
		rel := "inline"
		if multi {
			rel = fmt.Sprintf("inline#%d", i/chunkLines+1)
		}
		cands = append(cands, summarize.Candidate{
			RelPath:     rel,
			Kind:        summarize.KindInline,
			ContentHash: summarize.HashString(body),
			Body:        body,
			StartLine:   i + 1,
		})
	}
	return cands
}
