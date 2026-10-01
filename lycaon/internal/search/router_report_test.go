package search

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

type fakeExecutor struct {
	source string
	report ExecutorReport
	err    error
}

func TestRouterKeepsResultsAndLowerBoundsForCatalogCoverageFailures(t *testing.T) {
	for _, coverage := range []sourcecatalog.IndexCoverage{
		{DiscoveryComplete: true, Refreshing: true},
		{DiscoveryComplete: true, FailedDirectories: 1},
		{DiscoveryComplete: true, Refreshing: true, Error: "refresh failed"},
	} {
		report := ExecutorReport{Hits: []Hit{{ID: "known", HitKind: HitKindCode, ProjectID: "p"}}}
		report.Code.observeCoverage(coverage)
		router := NewRouter(fakeExecutor{source: ExecutorStore}, fakeExecutor{source: ExecutorCode, report: report}, nil)
		result, err := router.Execute(t.Context(), routerPlanBothLegs(), 100)
		testutil.FailErr(t, "route incomplete coverage", err)
		if result.Status != ResultStatusPartial || result.Exhaustive || result.CountRelation != CountRelationLowerBound || len(result.Hits) != 1 || len(result.Issues) != 1 {
			t.Fatalf("coverage=%+v result=%+v", coverage, result)
		}
		if coverage.Error != "" && result.Issues[0].Reason != IssueCatalogRefreshFailed {
			t.Fatalf("failed refresh became transient: %+v", result.Issues)
		}
	}
}

func (f fakeExecutor) Source() string { return f.source }
func (f fakeExecutor) Run(context.Context, PlanLeg) (ExecutorReport, error) {
	return f.report, f.err
}

func routerPlanBothLegs() *RoutedPlan {
	return &RoutedPlan{
		Store: &StorePlanLeg{SQL: "SELECT 1", Cap: 10},
		Code:  &CodePlanLeg{Query: TextExpr{Text: "x"}, Cap: 10, Lines: true},
	}
}

func TestRouterSurfacesSkippedFiles(t *testing.T) {
	router := NewRouter(
		fakeExecutor{source: ExecutorStore},
		fakeExecutor{source: ExecutorCode, report: ExecutorReport{
			Hits:         []Hit{{HitKind: HitKindCode, ProjectID: "p"}},
			SkippedFiles: 3,
		}},
		nil,
	)
	result, err := router.Execute(context.Background(), routerPlanBothLegs(), 100)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var found *Issue
	for i := range result.Issues {
		if result.Issues[i].Reason == IssueFilesSkipped {
			found = &result.Issues[i]
		}
	}
	if found == nil || found.Count != 3 || found.Executor != ExecutorCode {
		t.Fatalf("issues = %+v", result.Issues)
	}
	if result.Status != ResultStatusPartial || result.Exhaustive {
		t.Fatalf("status = %v, exhaustive = %v", result.Status, result.Exhaustive)
	}
}

func TestRouterReportsWarmingRootsAndTimeBudgetAsPartial(t *testing.T) {
	router := NewRouter(
		fakeExecutor{source: ExecutorStore},
		fakeExecutor{source: ExecutorCode, report: ExecutorReport{
			Hits:     []Hit{{HitKind: HitKindCode, ProjectID: "p"}},
			TimedOut: true,
			Code:     CodeLegReport{WarmingRoots: 2},
		}},
		nil,
	)
	result, err := router.Execute(context.Background(), routerPlanBothLegs(), 100)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reasons := map[IssueReason]Issue{}
	for _, issue := range result.Issues {
		reasons[issue.Reason] = issue
	}
	if warming, ok := reasons[IssueCatalogWarming]; !ok || warming.Count != 2 || warming.Executor != ExecutorCode {
		t.Fatalf("issues = %+v, want catalog_warming count 2", result.Issues)
	}
	if _, ok := reasons[IssueTimeBudget]; !ok {
		t.Fatalf("issues = %+v, want time_budget", result.Issues)
	}
	if result.Status != ResultStatusPartial || result.Exhaustive || len(result.Hits) != 1 {
		t.Fatalf("status = %v, exhaustive = %v, hits = %d", result.Status, result.Exhaustive, len(result.Hits))
	}
	if !result.Telemetry.CodeTimedOut || result.Telemetry.Code.WarmingRoots != 2 {
		t.Fatalf("telemetry = %+v", result.Telemetry)
	}
}

// Pending discovery and budget omissions produce distinct coverage issues.
func TestRouterSeparatesIncompleteDiscoveryFromABoundedWalk(t *testing.T) {
	router := NewRouter(
		fakeExecutor{source: ExecutorStore},
		fakeExecutor{source: ExecutorCode, report: ExecutorReport{
			Hits: []Hit{{HitKind: HitKindCode, ProjectID: "p"}},
			Code: CodeLegReport{Roots: 1, IncompleteRoots: 1, UnobservedDirs: 4},
		}},
		nil,
	)
	result, err := router.Execute(context.Background(), routerPlanBothLegs(), 100)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reasons := map[IssueReason]Issue{}
	for _, issue := range result.Issues {
		reasons[issue.Reason] = issue
	}
	if incomplete, ok := reasons[IssueCatalogIncomplete]; !ok || incomplete.Count != 1 {
		t.Fatalf("issues = %+v, want catalog_incomplete count 1", result.Issues)
	}
	if bounded, ok := reasons[IssueCatalogBounded]; !ok || bounded.Count != 4 {
		t.Fatalf("issues = %+v, want catalog_bounded count 4", result.Issues)
	}
	if _, ok := reasons[IssueCatalogWarming]; ok {
		t.Fatalf("issues = %+v, want no catalog_warming", result.Issues)
	}
	if result.Status != ResultStatusPartial || len(result.Hits) != 1 {
		t.Fatalf("status = %v hits = %d", result.Status, len(result.Hits))
	}
}

func TestRouterCarriesExecutorErrorMessage(t *testing.T) {
	router := NewRouter(
		fakeExecutor{source: ExecutorStore, err: errors.New("store exploded")},
		fakeExecutor{source: ExecutorCode},
		nil,
	)
	result, err := router.Execute(context.Background(), routerPlanBothLegs(), 100)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var found *Issue
	for i := range result.Issues {
		if result.Issues[i].Reason == IssueExecutorError {
			found = &result.Issues[i]
		}
	}
	if found == nil || found.Message != "store exploded" {
		t.Fatalf("issues = %+v", result.Issues)
	}
	if result.Status != ResultStatusPartial {
		t.Fatalf("status = %v", result.Status)
	}
}

func TestSearchFacetsCountVerificationState(t *testing.T) {
	verified := true
	unverified := false
	facets, _ := buildFacetsAndHistogram([]Hit{
		{HitKind: HitKindEvidence, Verified: &verified},
		{HitKind: HitKindClaim, Verified: &unverified},
		{HitKind: HitKindCode},
	})

	var values []FacetValue
	for _, facet := range facets {
		if facet.Key == "verified" {
			values = facet.Values
			break
		}
	}
	want := []FacetValue{{Value: "false", Count: 1}, {Value: "true", Count: 1}}
	if len(values) != len(want) {
		t.Fatalf("verified facet = %+v, want %+v", values, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("verified facet = %+v, want %+v", values, want)
		}
	}
}

func TestRouterOnlyCallsIndexWarmingIncompleteCoverage(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		router := NewRouter(fakeExecutor{source: ExecutorStore}, fakeExecutor{source: ExecutorCode, report: ExecutorReport{TimedOut: timedOut, Code: CodeLegReport{IndexWarmingRoots: 1}}}, nil)
		result, err := router.Execute(t.Context(), &RoutedPlan{Code: &CodePlanLeg{Query: TextExpr{Text: "needle"}, Lines: true, Cap: 10}}, 100)
		if err != nil {
			t.Fatalf("execute router: %v", err)
		}
		warming := false
		for _, issue := range result.Issues {
			warming = warming || issue.Reason == IssueIndexWarming
		}
		if warming != timedOut {
			t.Fatalf("timed out=%v issues=%+v", timedOut, result.Issues)
		}
	}
}
