package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/search"
)

// sourceSearchPages continues a source search after one index entry inside
// the index generation that answered the first page.
var sourceSearchPages = pagecursor.For[project.SourceIndexEntry]("source_search")

var sourceSearchLimit = httpio.MustPageLimit(50, 1, 200)

func sourceSearchScope(projectID, rootID, query string) string {
	return pagecursor.Scope(projectID, rootID, query)
}

func encodeSourceSearchCursor(scope string, generation uint64, after project.SourceIndexEntry) (string, error) {
	return sourceSearchPages.EncodeAt(scope, generation, after)
}

// decodeSourceSearchCursor opens a continuation for the index generation the
// request reads; a cursor from any other generation is pagecursor.ErrExpired.
func decodeSourceSearchCursor(raw, scope string, generation uint64) (*project.SourceIndexEntry, error) {
	if raw == "" {
		return nil, nil
	}
	_, after, err := sourceSearchPages.DecodeAt(raw, scope, pagecursor.Current(generation))
	if err != nil {
		return nil, err
	}
	if after.RootID == "" || after.Path == "" {
		return nil, pagecursor.ErrInvalid
	}
	return &after, nil
}

// SearchDependencyPatterns loads the scanners path-exclude catalog once.
var SearchDependencyPatterns = sync.OnceValue(func() []string {
	cfg, err := rules.LoadPathExcludes()
	if err != nil {
		return nil
	}
	return cfg.Patterns()
})

// searchDeclarations runs declaration discovery over whole roots.
var searchDeclarations = declarationSearchIn(nil, nil, nil)

// declarationSearchIn runs declaration-discovery passes on the live code
// executor, each pattern ANDed with scope and bounded by the include and
// exclude globs, so discovery sees the files the query's paths name.
func declarationSearchIn(scope []search.Node, include, exclude []string) project.DeclarationSearch {
	progress := map[project.DeclarationMatch]*search.CodeProgress{}
	return func(ctx context.Context, query project.DeclarationSearchQuery) ([]project.DeclarationSearchHit, project.DeclarationCoverage, error) {
		hitCap := query.HitCap
		if hitCap <= 0 {
			hitCap = project.DefinitionSearchHitCap
		}
		flags := search.MatchFlags{Include: include, Exclude: exclude}
		switch query.Match {
		case project.DeclarationMatchWholeWord:
			flags.WholeWord = true
		case project.DeclarationMatchSubstring:
		case project.DeclarationMatchRegexp:
			flags.Regex = true
			flags.CaseSensitive = true
		default:
			return nil, project.DeclarationCoverage{}, fmt.Errorf("unknown declaration match %d", query.Match)
		}
		roots := make([]search.CodeRoot, 0, len(query.Roots))
		for _, root := range query.Roots {
			roots = append(roots, search.CodeRoot{ProjectID: query.ProjectID, RootID: root.ID, Path: root.Path})
		}
		// A phrase keeps the pattern literal: no term split, no wildcard.
		var pattern search.Node = search.TextExpr{Text: query.Pattern, Phrase: true}
		if len(scope) > 0 {
			pattern = search.AndExpr{Exprs: append([]search.Node{pattern}, scope...)}
		}
		probeCap := hitCap + 1
		var cursor *search.CodeProgress
		if query.Continue {
			if progress[query.Match] == nil {
				progress[query.Match] = &search.CodeProgress{}
			}
			cursor = progress[query.Match]
			probeCap = hitCap
		}
		var checkpoint search.CodeProgress
		if cursor != nil {
			checkpoint = *cursor
		}
		// A declaration lookup is a literal pattern with no task to rank against.
		report, err := search.NewCodeExecutor(decide.Reranker{}).Run(ctx, search.PlanLeg{
			Executor: search.ExecutorCode,
			Cap:      probeCap,
			Code: &search.CodePlanLeg{
				Query:           pattern,
				Candidates:      query.Continue,
				Progress:        cursor,
				PathRoots:       roots,
				Cap:             probeCap,
				Lines:           true,
				LineExcludeDirs: query.ExcludeDirs,
				Flags:           flags,
				Wall:            query.Wall,
			},
		})
		if err != nil {
			if !query.Continue || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
				if cursor != nil {
					*cursor = checkpoint
				}
				return nil, project.DeclarationCoverage{}, err
			}
			report.TimedOut = true
		}
		out := make([]project.DeclarationSearchHit, 0, len(report.Hits))
		for _, h := range report.Hits {
			if h.HitKind != search.HitKindCode {
				continue
			}
			out = append(out, project.DeclarationSearchHit{RootID: h.RootID, Path: h.Path, Snippet: strings.Clone(h.Snippet)})
		}
		coverage := project.DeclarationCoverage{Limited: report.Limited || len(out) > hitCap}
		for _, issue := range report.CoverageIssues(search.ExecutorCode) {
			coverage.Gaps = append(coverage.Gaps, project.DeclarationGap{Reason: project.DeclarationGapReason(issue.Reason), Count: issue.Count, Limit: issue.Limit, Message: issue.Message})
		}
		if len(out) > hitCap {
			out = out[:hitCap]
		}
		return out, coverage, nil
	}
}
