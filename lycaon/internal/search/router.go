package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ResultStatus string

const (
	ResultStatusComplete ResultStatus = "complete"
	ResultStatusLimited  ResultStatus = "limited"
	ResultStatusPartial  ResultStatus = "partial"
)

type CountRelation string

const (
	CountRelationExact      CountRelation = "exact"
	CountRelationLowerBound CountRelation = "lower_bound"
)

type IssueReason string

const (
	IssueResultLimit   IssueReason = "result_limit"
	IssueExecutorError IssueReason = "executor_error"
	IssueFilesSkipped  IssueReason = "files_skipped"
	// IssueTimeBudget: the leg's wall-clock budget ended with files unscanned.
	IssueTimeBudget IssueReason = "time_budget"
	// IssueCatalogWarming counts roots without a readable generation.
	IssueCatalogWarming IssueReason = "catalog_warming"
	// IssueCatalogIncomplete counts roots whose discovery is still running.
	IssueCatalogIncomplete IssueReason = "catalog_incomplete"
	// IssueIndexWarming: a direct scan timed out while its accelerator builds.
	IssueIndexWarming IssueReason = "index_warming"
	// IssueCatalogBounded counts directories excluded by the discovery budget.
	IssueCatalogBounded       IssueReason = "catalog_bounded"
	IssueCatalogFailed        IssueReason = "catalog_failed"
	IssueCatalogRefreshFailed IssueReason = "catalog_refresh_failed"
	IssueCatalogRefreshing    IssueReason = "catalog_refreshing"
	// IssueSymbolPending: a retained frontier can advance on another request.
	IssueSymbolPending IssueReason = "symbol_pending"
	// IssueSymbolBudget: a terminal declaration resource bound stopped discovery.
	IssueSymbolBudget IssueReason = "symbol_budget"
)

// Issue records why a search generation is not exhaustive.
type Issue struct {
	Executor string
	Reason   IssueReason
	Limit    int
	// Count carries the affected-item count for reasons that have one.
	Count int
	// Message carries the underlying failure for executor_error.
	Message string
}

// Result is the merged federation output before wire mapping.
type Result struct {
	Hits           []Hit
	Facets         []Facet
	Histogram      []HistogramBucket
	Issues         []Issue
	Status         ResultStatus
	Exhaustive     bool
	CountRelation  CountRelation
	Interpretation SearchInterpretation
	Telemetry      SearchTelemetry
}

// SearchTelemetry is what one search cost, for the per-request log line.
type SearchTelemetry struct {
	StoreDuration  time.Duration
	CodeDuration   time.Duration
	CodeTimedOut   bool
	Code           CodeLegReport
	SymbolDuration time.Duration
	Symbol         SymbolLegReport
}

// Facet is one facet rail dimension with counted values.
type Facet struct {
	Key    string
	Values []FacetValue
}

// FacetValue is one facet bucket.
type FacetValue struct {
	Value string
	Count int
}

// HistogramBucket counts hits for one histogram key (hit kind).
type HistogramBucket struct {
	Key   string
	Count int
}

// Router fans a routed plan to executors and merges the hit stream.
type Router struct {
	store  Executor
	code   Executor
	symbol Executor
}

// NewRouter wires the store, code, and symbol executors.
func NewRouter(store, code, symbol Executor) *Router {
	return &Router{store: store, code: code, symbol: symbol}
}

// Execute runs all plan legs and merges results with origin-weighted ranking.
func (r *Router) Execute(ctx context.Context, plan *RoutedPlan, maxHits int) (*Result, error) {
	if plan == nil {
		return nil, fmt.Errorf("missing routed plan")
	}
	var (
		mu        sync.Mutex
		hits      []Hit
		issues    []Issue
		matchErr  *MatchError
		telemetry SearchTelemetry
	)
	runLeg := func(exec Executor, leg PlanLeg) {
		if exec == nil {
			mu.Lock()
			issues = append(issues, Issue{
				Executor: leg.Executor,
				Reason:   IssueExecutorError,
				Message:  "executor unavailable",
			})
			mu.Unlock()
			return
		}
		started := time.Now()
		report, err := exec.Run(ctx, leg)
		elapsed := time.Since(started)
		mu.Lock()
		defer mu.Unlock()
		switch leg.Executor {
		case ExecutorStore:
			telemetry.StoreDuration = elapsed
		case ExecutorCode:
			telemetry.CodeDuration = elapsed
			telemetry.CodeTimedOut = report.TimedOut
			telemetry.Code = report.Code
		case ExecutorSymbol:
			telemetry.SymbolDuration = elapsed
			telemetry.Symbol = report.Symbol
		}
		if err != nil {
			var me *MatchError
			if errors.As(err, &me) {
				// Preserve typed pattern errors for the request boundary.
				matchErr = me
				return
			}
			slog.WarnContext(ctx, "search executor failed", "executor", leg.Executor, "err", err)
			issues = append(issues, Issue{
				Executor: leg.Executor,
				Reason:   IssueExecutorError,
				Message:  err.Error(),
			})
		}
		legHits := report.Hits
		if report.Limited {
			legHits = trimExecutorProbeHits(legHits, leg)
			issues = append(issues, Issue{
				Executor: leg.Executor,
				Reason:   IssueResultLimit,
				Limit:    executorResultLimit(leg),
			})
		}
		issues = append(issues, report.CoverageIssues(leg.Executor)...)
		if len(legHits) > 0 {
			hits = append(hits, legHits...)
		}
	}
	var wg sync.WaitGroup
	if plan.Store != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLeg(r.store, PlanLeg{Executor: ExecutorStore, Cap: plan.Store.Cap, Store: plan.Store})
		}()
	}
	if plan.Code != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLeg(r.code, PlanLeg{Executor: ExecutorCode, Cap: plan.Code.Cap, Code: plan.Code})
		}()
	}
	if plan.Symbol != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLeg(r.symbol, PlanLeg{Executor: ExecutorSymbol, Cap: plan.Symbol.Cap, Symbol: plan.Symbol})
		}()
	}
	wg.Wait()
	if matchErr != nil {
		return nil, matchErr
	}

	origin := strings.TrimSpace(plan.OriginProjectID)
	rankHits(hits, origin)
	if maxHits > 0 && len(hits) > maxHits {
		hits = hits[:maxHits]
		issues = withoutLimitIssues(issues)
		issues = append(issues, Issue{Executor: "federation", Reason: IssueResultLimit, Limit: maxHits})
	}
	for i := range hits {
		ProjectHitDisplay(&hits[i])
	}
	facets, histogram := buildFacetsAndHistogram(hits)
	status, exhaustive, relation := searchCompletion(issues)
	return &Result{
		Hits:           hits,
		Facets:         facets,
		Histogram:      histogram,
		Issues:         issues,
		Status:         status,
		Exhaustive:     exhaustive,
		CountRelation:  relation,
		Interpretation: plan.Interpretation,
		Telemetry:      telemetry,
	}, nil
}

func executorResultLimit(leg PlanLeg) int {
	if leg.Store != nil {
		return probeResultLimit(leg.Cap)
	}
	if leg.Code == nil {
		return 0
	}
	switch {
	case leg.Code.Lines && leg.Code.Files:
		return probeResultLimit(leg.Code.Cap) + probeResultLimit(leg.Code.FileCap)
	case leg.Code.Lines && !leg.Code.Files:
		return probeResultLimit(leg.Code.Cap)
	case leg.Code.Files && !leg.Code.Lines:
		return probeResultLimit(leg.Code.FileCap)
	default:
		return 0
	}
}

func probeResultLimit(cap int) int {
	return max(0, cap-1)
}

func trimExecutorProbeHits(hits []Hit, leg PlanLeg) []Hit {
	if leg.Store != nil {
		return hits[:min(len(hits), probeResultLimit(leg.Cap))]
	}
	if leg.Code == nil {
		return hits
	}
	lineLimit := probeResultLimit(leg.Code.Cap)
	fileLimit := probeResultLimit(leg.Code.FileCap)
	lines := 0
	files := 0
	trimmed := make([]Hit, 0, min(len(hits), lineLimit+fileLimit))
	for _, hit := range hits {
		switch hit.HitKind {
		case HitKindCode:
			if lines >= lineLimit {
				continue
			}
			lines++
		case HitKindFile:
			if files >= fileLimit {
				continue
			}
			files++
		}
		trimmed = append(trimmed, hit)
	}
	return trimmed
}

func searchCompletion(issues []Issue) (ResultStatus, bool, CountRelation) {
	status := ResultStatusComplete
	relation := CountRelationExact
	for _, issue := range issues {
		relation = CountRelationLowerBound
		switch issue.Reason {
		case IssueResultLimit:
			if status == ResultStatusComplete {
				status = ResultStatusLimited
			}
		case IssueExecutorError, IssueFilesSkipped, IssueTimeBudget, IssueSymbolBudget,
			IssueCatalogWarming, IssueCatalogIncomplete, IssueCatalogBounded, IssueCatalogFailed, IssueCatalogRefreshFailed, IssueCatalogRefreshing, IssueIndexWarming:
			status = ResultStatusPartial
		}
	}
	return status, len(issues) == 0, relation
}

func withoutLimitIssues(issues []Issue) []Issue {
	kept := issues[:0]
	for _, issue := range issues {
		if issue.Reason != IssueResultLimit {
			kept = append(kept, issue)
		}
	}
	return kept
}

func rankHits(hits []Hit, originProjectID string) {
	originProjectID = strings.TrimSpace(originProjectID)
	sort.SliceStable(hits, func(i, j int) bool {
		oi := originProjectID != "" && hits[i].ProjectID == originProjectID
		oj := originProjectID != "" && hits[j].ProjectID == originProjectID
		if oi != oj {
			return oi
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].TS > hits[j].TS
	})
}

func buildFacetsAndHistogram(hits []Hit) ([]Facet, []HistogramBucket) {
	kindCounts := map[string]int{}
	sourceCounts := map[string]int{}
	trustCounts := map[string]int{}
	verifiedCounts := map[string]int{}
	for _, hit := range hits {
		if k := strings.TrimSpace(hit.HitKind); k != "" {
			kindCounts[k]++
		}
		if s := strings.TrimSpace(hit.Source); s != "" {
			sourceCounts[s]++
		}
		if t := strings.TrimSpace(hit.Trust); t != "" {
			trustCounts[t]++
		}
		if hit.Verified != nil {
			verifiedCounts[strconv.FormatBool(*hit.Verified)]++
		}
	}
	facets := []Facet{
		facetFromCounts("kind", kindCounts),
		facetFromCounts("source", sourceCounts),
		facetFromCounts("trust", trustCounts),
		facetFromCounts("verified", verifiedCounts),
	}
	histogram := make([]HistogramBucket, 0, len(kindCounts))
	for key, count := range kindCounts {
		histogram = append(histogram, HistogramBucket{Key: key, Count: count})
	}
	sort.Slice(histogram, func(i, j int) bool {
		return histogram[i].Key < histogram[j].Key
	})
	return facets, histogram
}

func facetFromCounts(key string, counts map[string]int) Facet {
	values := make([]FacetValue, 0, len(counts))
	for value, count := range counts {
		values = append(values, FacetValue{Value: value, Count: count})
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].Value < values[j].Value
	})
	return Facet{Key: key, Values: values}
}
