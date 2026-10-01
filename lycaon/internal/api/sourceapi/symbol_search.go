package sourceapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
)

// symbolProjectWorkers bounds projects searched at once on one symbol leg.
const symbolProjectWorkers = 4

// SymbolExecutor is search federation's symbol leg. In each project, content
// discovery nominates files, their cached outlines confirm declarations, and
// names rank by how they match, the pipeline Go to definition shares.
type SymbolExecutor struct {
	registry project.Registry
}

// NewSymbolExecutor returns the symbol leg over the project registry.
func NewSymbolExecutor(registry project.Registry) *SymbolExecutor {
	return &SymbolExecutor{registry: registry}
}

func (e *SymbolExecutor) Source() string { return search.ExecutorSymbol }

// symbolProject is one project's roots on the leg, in plan order.
type symbolProject struct {
	id      string
	rootIDs []string
}

// symbolProjectResult is one project's answer.
type symbolProjectResult struct {
	hits          []search.Hit
	limited       bool
	incomplete    bool
	filesOutlined int
	err           error
}

// Run searches each project's roots concurrently under the leg's wall clock
// and keeps projects in plan order, so the origin project leads.
func (e *SymbolExecutor) Run(ctx context.Context, leg search.PlanLeg) (search.ExecutorReport, error) {
	if leg.Symbol == nil {
		return search.ExecutorReport{}, fmt.Errorf("symbol leg missing")
	}
	if e.registry == nil {
		return search.ExecutorReport{}, fmt.Errorf("project registry unavailable")
	}
	filter, err := search.CompileSymbolFilter(leg.Symbol)
	if err != nil {
		return search.ExecutorReport{}, err
	}
	projects := symbolProjects(leg.Symbol.Roots)
	legCtx, cancel := context.WithTimeout(ctx, leg.Symbol.Budget.Wall())
	defer cancel()

	results := make([]symbolProjectResult, len(projects))
	sem := make(chan struct{}, symbolProjectWorkers)
	var wg sync.WaitGroup
	for i, p := range projects {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = e.searchProject(legCtx, leg.Symbol, filter, p)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return search.ExecutorReport{}, err
	}

	report := search.ExecutorReport{Symbol: search.SymbolLegReport{Projects: len(projects)}}
	limited, incomplete := 0, 0
	for i, result := range results {
		if result.err != nil {
			report.Issues = append(report.Issues, search.Issue{
				Executor: search.ExecutorSymbol,
				Reason:   search.IssueExecutorError,
				Message:  fmt.Sprintf("project %s: %v", projects[i].id, result.err),
			})
			continue
		}
		report.Hits = append(report.Hits, result.hits...)
		report.Symbol.FilesOutlined += result.filesOutlined
		if result.limited {
			limited++
		}
		if result.incomplete {
			incomplete++
		}
	}
	report.Symbol.Declarations = len(report.Hits)
	if limited > 0 {
		report.Issues = append(report.Issues, search.Issue{
			Executor: search.ExecutorSymbol, Reason: search.IssueResultLimit, Limit: leg.Symbol.Cap, Count: limited,
		})
	}
	if incomplete > 0 {
		report.Issues = append(report.Issues, search.Issue{
			Executor: search.ExecutorSymbol, Reason: search.IssueSymbolBudget, Count: incomplete,
		})
	}
	return report, nil
}

func (e *SymbolExecutor) searchProject(ctx context.Context, leg *search.SymbolPlanLeg, filter search.SymbolFilter, target symbolProject) symbolProjectResult {
	p, err := e.registry.Get(ctx, target.id)
	if err != nil {
		return symbolProjectResult{err: err}
	}
	started := time.Now()
	result, err := project.SearchProjectSourceSymbols(ctx, p, project.SourceSymbolSearchRequest{
		Query:         leg.Name,
		RootIDs:       target.rootIDs,
		Limit:         leg.Cap,
		CaseSensitive: leg.Flags.CaseSensitive,
		Exact:         leg.Flags.WholeWord,
		ExcludeDirs:   leg.ExcludeDirs,
	}, declarationSearchIn(leg.DiscoveryScope(), leg.Flags.Include, leg.Flags.Exclude))
	if err != nil {
		return symbolProjectResult{err: err}
	}
	logSymbolSearchDone(target.id, result, started)
	out := symbolProjectResult{limited: result.Limited, incomplete: result.Incomplete}
	for _, pass := range result.Passes {
		out.filesOutlined += pass.Files
	}
	for _, match := range result.Symbols {
		if !filter.Admits(match.Path, match.Name) {
			continue
		}
		highlights := make([]search.TextRange, 0, len(match.Highlights))
		for _, span := range match.Highlights {
			highlights = append(highlights, search.TextRange{Start: span.Start, End: span.End})
		}
		out.hits = append(out.hits, search.NewSymbolHit(search.SymbolDeclaration{
			ProjectID:  target.id,
			RootID:     match.RootID,
			Path:       match.Path,
			Line:       match.Line,
			Name:       match.Name,
			Kind:       string(match.Kind),
			Highlights: highlights,
			Signature:  match.Signature,
			MatchRank:  match.MatchRank(),
		}, len(out.hits)))
	}
	return out
}

// symbolProjects groups the leg's roots by project, keeping first appearance
// order.
func symbolProjects(roots []search.CodeRoot) []symbolProject {
	var out []symbolProject
	index := map[string]int{}
	for _, root := range roots {
		id := strings.TrimSpace(root.ProjectID)
		if id == "" {
			continue
		}
		at, seen := index[id]
		if !seen {
			at = len(out)
			index[id] = at
			out = append(out, symbolProject{id: id})
		}
		out[at].rootIDs = append(out[at].rootIDs, root.RootID)
	}
	return out
}

// logSymbolSearchDone is the one line per project symbol search that says
// where its time went.
func logSymbolSearchDone(projectID string, result project.SourceSymbolSearchResult, started time.Time) {
	passes := make([]string, 0, len(result.Passes))
	for _, pass := range result.Passes {
		passes = append(passes, fmt.Sprintf("%s:hits=%d,files=%d,partial=%t,discover_ms=%d,outline_ms=%d",
			pass.Match, pass.Hits, pass.Files, pass.Partial, pass.Discovery.Milliseconds(), pass.Outline.Milliseconds()))
	}
	observability.LogLatency("search", "symbol search done", started,
		"project_id", projectID,
		"symbols", len(result.Symbols),
		"limited", result.Limited,
		"incomplete", result.Incomplete,
		"passes", strings.Join(passes, " "),
	)
}
