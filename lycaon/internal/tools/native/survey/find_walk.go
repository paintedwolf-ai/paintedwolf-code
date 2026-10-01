package survey

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

type findWalkParams struct {
	ctx                context.Context
	tctx               tools.ToolContext
	root               projectroot.RootRef
	fullRoot           string
	maxDepth           int
	maxResults         int
	offset             int
	nameFilter         sandbox.EntryGlob
	entryType          findEntryType
	resp               *findResponse
	skipped            *int
	matchedTotal       *int
	truncated          *bool
	deeperPathsOmitted *bool
	catalog            *sourcecatalog.Catalog
	readFilter         sandbox.ReadFilter
	onMatch            func(findResult)
}

func (t *FindTool) walkFindTree(p findWalkParams) error {
	if strings.TrimSpace(p.tctx.WorkerBranchRoot) != "" {
		return t.walkFindTreeSurvey(p)
	}
	inventory, err := sourceInventoryForScope(p.ctx, p.catalog, p.tctx.ProjectID, p.root, p.fullRoot)
	if err != nil {
		return err
	}
	return inventory.walk(p.ctx, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		if entry.IsDir && p.readFilter != nil && !p.readFilter(entry.Path, true) {
			return sourcecatalog.WalkSkip
		}
		if beyondFindDepth(inventory.depth(entry), p.maxDepth) {
			*p.deeperPathsOmitted = true
			return sourcecatalog.WalkSkip
		}
		if entry.IsDir && p.entryType == findTypeFile {
			return sourcecatalog.WalkContinue
		}
		info := catalogFileInfo{entry: entry}
		if t.matchesEntry(entry.Path, p.nameFilter, p.entryType, info) {
			abs := filepath.Join(p.root.Path, filepath.FromSlash(entry.Path))
			p.collect(makeFindResult(projectpaths.QualifyAbs(p.tctx, p.root, abs), info))
		}
		return sourcecatalog.WalkContinue
	})
}

func (t *FindTool) walkFindTreeSurvey(p findWalkParams) error {
	// Read globs prune directories, not file names.
	admit := func(_, abs string, isDir bool) bool {
		if !isDir {
			return true
		}
		return p.readFilter == nil || p.readFilter(projectroot.ScopeRel(p.root, abs), true)
	}
	return workerBranchOrSurveyWalk(p.ctx, p.tctx, p.fullRoot, sandbox.SurveyOptions{IncludeHidden: true, Admit: admit},
		func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if beyondFindDepth(e.Depth, p.maxDepth) {
				*p.deeperPathsOmitted = true
				if e.IsDir {
					return sandbox.SurveySkipDir, nil
				}
				return sandbox.SurveyContinue, nil
			}
			if e.IsDir && p.entryType == findTypeFile {
				return sandbox.SurveyContinue, nil
			}
			info, err := e.DirEntry.Info()
			if err != nil {
				return sandbox.SurveyContinue, nil
			}
			if t.matchesEntry(projectroot.ScopeRel(p.root, e.Abs), p.nameFilter, p.entryType, info) {
				p.collect(makeFindResult(projectpaths.QualifyAbs(p.tctx, p.root, e.Abs), info))
			}
			return sandbox.SurveyContinue, nil
		})
}

// beyondFindDepth reports an entry past the walk's depth limit; zero is unbounded.
func beyondFindDepth(depth, maxDepth int) bool {
	return maxDepth > 0 && depth > maxDepth
}

// collect counts one match and pages it into the response.
func (p findWalkParams) collect(result findResult) {
	*p.matchedTotal++
	if p.onMatch != nil {
		p.onMatch(result)
		return
	}
	if *p.skipped < p.offset {
		*p.skipped++
		return
	}
	if len(p.resp.Results) >= p.maxResults {
		*p.truncated = true
		return
	}
	p.resp.Results = append(p.resp.Results, result)
}

func parseFindEntryType(args map[string]any) (findEntryType, error) {
	entryType := findTypeAny
	if raw, ok := args["type"].(string); ok {
		switch strings.TrimSpace(strings.ToLower(raw)) {
		case "file":
			entryType = findTypeFile
		case "dir":
			entryType = findTypeDir
		case "any", "":
			entryType = findTypeAny
		default:
			return findTypeAny, safecmd.Reject("FIND_INVALID_TYPE", map[string]any{"type": raw})
		}
	}
	return entryType, nil
}
