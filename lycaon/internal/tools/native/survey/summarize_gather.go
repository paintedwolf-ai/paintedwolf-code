package survey

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tsparse"
	"golang.org/x/mod/modfile"
)

type summarizeGatherer struct {
	parseFailures     []tsparse.FileFailure
	parseFailureCount int
	skippedPaths      map[string]bool
	nonTextPaths      map[string]bool
	treeViews         map[string]summaryTreeLookup
	truncatedPaths    map[string]bool
	readReservations  map[string]int64
	sourceFiles       int
	sourceBytes       int64
	sourceReadBytes   int64
	readMu            sync.Mutex
	sourceLimited     bool
	treeReaders       []*sourcecatalog.SummaryReader
	treeErr           error
	treeRequest       summarize.Request
	treeState         sourcecatalog.State
	treeRefreshing    bool
	treeNodes         int
	directoryEntries  int
	directoriesOpened int
	indexWaitUsed     bool
	boundary          *sandbox.Boundary
	// reads resolves and opens every file the gather reads.
	reads             *projectpaths.ReadSession
	caps              summarize.Caps
	tctx              tools.ToolContext
	catalog           *sourcecatalog.Catalog
	rerank            decide.Reranker
	// nestedPruneCount spans all scans in one gather.
	nestedPruneCount *int
	nestedPruneSeen  map[string]struct{}
	// faninGrepPasses counts shared grep passes.
	faninGrepPasses *int
	// Gather and Outline share these call-local memos.
	memoMu             sync.Mutex
	structureByPath    map[string]structureCacheEntry
	bytesByAbs         map[string][]byte
	goImportByPkgDir   map[string]string // path.Dir(rel) → module import path ("" = miss)
	goModByAbs         map[string]*modfile.File
	subtreeByPath      map[string]*summarize.SubtreeNode
	catalogRevision    uint64
	patternFilesTotal  int
	patternNextPath    string
	patternCursorFound bool
}

var _ summarize.Gatherer = (*summarizeGatherer)(nil)

var _ summarize.OutlineProvider = (*summarizeGatherer)(nil)

func (g *summarizeGatherer) Gather(ctx context.Context, req summarize.Request) (summarize.GatherResult, error) {
	g.treeRequest = req
	mode := summarize.ResolveMode(strings.TrimSpace(req.Content) != "", req.HasRepoKeys())
	res := summarize.GatherResult{Mode: mode}
	g.patternFilesTotal = 0
	g.patternNextPath = ""
	g.patternCursorFound = false

	var nestedPruned int
	g.nestedPruneCount = &nestedPruned
	g.nestedPruneSeen = nil
	var faninPasses int
	g.faninGrepPasses = &faninPasses
	defer func() {
		g.nestedPruneCount = nil
		g.nestedPruneSeen = nil
		g.faninGrepPasses = nil
	}()

	if req.Content != "" {
		outline := fileoutline.AnalyzeText(ctx, inlineOutlineHint(req.Content), []byte(req.Content))
		g.noteParseFailure("inline", "source", outline.ParseFailure)
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
				res.Subtree = g.buildSubtreeForTarget(ctx, target)
				res.Importance, err = g.computeSubtreeImportance(ctx, target, res.Subtree, res.Structure)
				if err != nil {
					return summarize.GatherResult{}, fmt.Errorf("summarize fan-in leads: %w", err)
				}
			} else if len(req.Paths) > 1 {
				res.Subtree = g.buildSubtreeForTargets(ctx, req.Paths)
				res.Importance, err = g.computeSubtreeImportance(ctx, ".", res.Subtree, res.Structure)
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
	fit, fitErr := g.gatherFitEdges(ctx, req, res.Structure, stats)
	if fitErr != nil {
		return summarize.GatherResult{}, fmt.Errorf("summarize reference leads: %w", fitErr)
	}
	res.Fit = fit
	res.NextActions = g.patternInspectionActions(ctx, req, nil)
	res.CatalogRevision = g.catalogRevision
	res.MatchingFilesObserved = g.patternFilesTotal
	res.CursorFound = g.patternCursorFound
	res.NextCursorPath = g.patternNextPath
	res.CatalogState = string(g.treeState)
	res.CatalogRefreshing = g.treeRefreshing
	if res.Subtree != nil && res.Subtree.LoadChildren != nil && req.CursorPosition != "" {
		res.CursorFound = true
	}
	if err := ctx.Err(); err != nil {
		return summarize.GatherResult{}, err
	}
	if g.treeErr != nil {
		return summarize.GatherResult{}, g.treeErr
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
