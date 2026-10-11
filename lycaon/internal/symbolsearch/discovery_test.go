package symbolsearch

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
)

// testDeclarationSearch mirrors the API's code-executor discovery over a
// settled catalog. It ignores the wall clock so a loaded test host cannot
// turn a small fixture into a timed-out pass.
func testDeclarationSearch(ctx context.Context, query projectsource.DeclarationSearchQuery) ([]projectsource.DeclarationSearchHit, projectsource.DeclarationCoverage, error) {
	roots := make([]search.CodeRoot, 0, len(query.Roots))
	for _, root := range query.Roots {
		if err := catalogtest.AwaitIndex(ctx, sourcecatalog.Process(), query.ProjectID, sourcecatalog.Root{ID: root.ID, Path: root.Path}); err != nil {
			return nil, projectsource.DeclarationCoverage{}, err
		}
		roots = append(roots, search.CodeRoot{ProjectID: query.ProjectID, RootID: root.ID, Path: root.Path})
	}
	hitCap := query.HitCap
	if hitCap <= 0 {
		hitCap = projectsource.DefinitionSearchHitCap
	}
	var flags search.MatchFlags
	switch query.Match {
	case projectsource.DeclarationMatchWholeWord:
		flags.WholeWord = true
	case projectsource.DeclarationMatchSubstring:
	case projectsource.DeclarationMatchRegexp:
		flags.Regex = true
		flags.CaseSensitive = true
	default:
		return nil, projectsource.DeclarationCoverage{}, fmt.Errorf("unknown declaration match %d", query.Match)
	}
	probeCap := hitCap + 1
	report, err := search.NewCodeExecutor(decide.Reranker{}).Run(ctx, search.PlanLeg{
		Executor: search.ExecutorCode,
		Cap:      probeCap,
		Code: &search.CodePlanLeg{
			Candidates:      query.Continue,
			Query:           search.TextExpr{Text: query.Pattern, Phrase: true},
			PathRoots:       roots,
			Cap:             probeCap,
			Lines:           true,
			LineExcludeDirs: query.ExcludeDirs,
			Flags:           flags,
		},
	})
	if err != nil {
		return nil, projectsource.DeclarationCoverage{}, err
	}
	out := make([]projectsource.DeclarationSearchHit, 0, len(report.Hits))
	for _, h := range report.Hits {
		if h.HitKind != search.HitKindCode {
			continue
		}
		out = append(out, projectsource.DeclarationSearchHit{RootID: h.RootID, Path: h.Path, Snippet: h.Snippet})
	}
	limited := report.Limited || report.TimedOut || report.Code.WarmingRoots > 0 ||
		report.Code.IncompleteRoots > 0 || report.SkippedFiles > 0 || len(out) > hitCap
	if len(out) > hitCap {
		out = out[:hitCap]
	}
	return out, projectsource.DeclarationCoverage{Limited: limited}, nil
}
