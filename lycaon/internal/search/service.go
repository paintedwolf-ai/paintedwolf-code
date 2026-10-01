package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/observability"
)

// Service compiles DSL queries and runs the federation router.
type Service struct {
	router *Router
}

// NewService constructs a search service over the local store projection, the
// live code leg, whose hits rerank through the decision engine, and the symbol
// leg, which finds declarations by name.
func NewService(db queryDatabase, rerank decide.Reranker, symbols Executor) *Service {
	return &Service{router: NewRouter(NewStoreExecutor(db), NewCodeExecutor(rerank), symbols)}
}

// ExportOutcome is the merged export query result before serialization.
type ExportOutcome struct {
	Result    *Result
	Plan      *RoutedPlan
	Truncated bool
}

// Export compiles and executes a query for export with the export row cap.
func (s *Service) Export(ctx context.Context, query string, compileCtx CompileContext) (*ExportOutcome, error) {
	if s == nil || s.router == nil {
		return nil, fmt.Errorf("search service unavailable")
	}
	query = strings.TrimSpace(query)
	plan, err := CompileQuery(query, compileCtx)
	if err != nil {
		return nil, err
	}
	plan.OriginProjectID = strings.TrimSpace(compileCtx.OriginProjectID)
	if plan.Store != nil {
		plan.Store.Cap = ExportExecutorProbeHits
	}
	if plan.Code != nil {
		plan.Code.Cap = ExportExecutorProbeHits
		plan.Code.FileCap = ExportExecutorProbeHits
	}
	if plan.Symbol != nil {
		plan.Symbol.Cap = SymbolLegCap
	}
	result, err := s.router.Execute(ctx, plan, ExportMaxHits)
	if err != nil {
		return nil, err
	}
	truncated := exportTruncated(result)
	return &ExportOutcome{
		Result:    result,
		Plan:      plan,
		Truncated: truncated,
	}, nil
}

func exportTruncated(result *Result) bool {
	if result == nil {
		return false
	}
	return !result.Exhaustive
}

// Search compiles and executes a global query.
func (s *Service) Search(ctx context.Context, query string, compileCtx CompileContext) (*Result, error) {
	if s == nil || s.router == nil {
		return nil, fmt.Errorf("search service unavailable")
	}
	query = strings.TrimSpace(query)
	plan, err := CompileQuery(query, compileCtx)
	if err != nil {
		return nil, err
	}
	plan.OriginProjectID = strings.TrimSpace(compileCtx.OriginProjectID)
	started := time.Now()
	result, err := s.router.Execute(ctx, plan, SearchDisplayMaxHits)
	if err != nil {
		return nil, err
	}
	logSearchDone(plan, compileCtx.Budget, result, started)
	return result, nil
}

// logSearchDone is the one line per search that says where its time went.
func logSearchDone(plan *RoutedPlan, budget SearchBudget, result *Result, started time.Time) {
	code := result.Telemetry.Code
	symbol := result.Telemetry.Symbol
	observability.LogLatency("search", "search done", started,
		"scope", string(plan.Scope),
		"budget", string(budget.normalize()),
		"status", string(result.Status),
		"hits", len(result.Hits),
		"issues", len(result.Issues),
		"store_ms", result.Telemetry.StoreDuration.Milliseconds(),
		"code_ms", result.Telemetry.CodeDuration.Milliseconds(),
		"code_timed_out", result.Telemetry.CodeTimedOut,
		"code_roots", code.Roots,
		"code_refreshing_roots", code.RefreshingRoots,
		"code_incomplete_roots", code.IncompleteRoots,
		"code_unobserved_dirs", code.UnobservedDirs,
		"code_failed_dirs", code.FailedDirs,
		"code_refresh_failed_roots", code.RefreshFailedRoots,
		"code_warming_roots", code.WarmingRoots,
		"code_index_warming_roots", code.IndexWarmingRoots,
		"code_files", code.FilesListed,
		"code_candidates", code.ContentCandidates,
		"code_opened", code.FilesOpened,
		"code_prefilter_skipped", code.PrefilterSkipped,
		"code_index", code.IndexUsed,
		"code_generation_ms", code.GenerationWait.Milliseconds(),
		"code_index_ms", code.IndexWait.Milliseconds(),
		"symbol_ms", result.Telemetry.SymbolDuration.Milliseconds(),
		"symbol_projects", symbol.Projects,
		"symbol_files", symbol.FilesOutlined,
		"symbol_declarations", symbol.Declarations,
	)
}
