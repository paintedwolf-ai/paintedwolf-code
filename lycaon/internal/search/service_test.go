package search

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

type fixedExecutor struct {
	hits    []Hit
	partial bool
	err     error
}

func (e fixedExecutor) Source() string { return ExecutorStore }

func (e fixedExecutor) Run(context.Context, PlanLeg) (ExecutorReport, error) {
	return ExecutorReport{Hits: append([]Hit(nil), e.hits...), Limited: e.partial}, e.err
}

func TestRouterOriginRanksFirst(t *testing.T) {
	origin := "proj-a"
	other := "proj-b"
	hits := []Hit{
		{HitKind: HitKindWeb, Source: SourceTool, ProjectID: other, TS: "2026-06-25T12:00:00Z", Score: 1},
		{HitKind: HitKindWeb, Source: SourceTool, ProjectID: origin, TS: "2026-06-25T11:00:00Z", Score: 1},
	}
	rankHits(hits, origin)
	if hits[0].ProjectID != origin {
		t.Fatalf("first hit project = %q, want %q", hits[0].ProjectID, origin)
	}
}

func TestRouterScoreBreaksTiesWithinOrigin(t *testing.T) {
	origin := "proj-a"
	hits := []Hit{
		{HitKind: HitKindWeb, Source: SourceTool, ProjectID: origin, TS: "2026-06-25T12:00:00Z", Score: 1.2},
		{HitKind: HitKindWeb, Source: SourceTool, ProjectID: origin, TS: "2026-06-25T11:00:00Z", Score: 4.5},
	}
	rankHits(hits, origin)
	if hits[0].Score != 4.5 {
		t.Fatalf("first score = %v, want 4.5", hits[0].Score)
	}
}

func TestSubstringMatcherTerms(t *testing.T) {
	if !newSubstringMatcher([]string{"needle"}, false).matches("Needle here") {
		t.Fatal("expected case-folded match")
	}
	if newSubstringMatcher([]string{"missing"}, false).matches("needle here") {
		t.Fatal("expected no match")
	}
	// A quoted phrase matches as one contiguous substring.
	if !newSubstringMatcher([]string{"quick brown"}, true).matches("the quick brown fox") {
		t.Fatal("expected phrase match")
	}
	if newSubstringMatcher([]string{"brown quick"}, true).matches("the quick brown fox") {
		t.Fatal("phrase must not match out of order")
	}
}

func TestCodeHitScoreBasenameBeatsLineOnly(t *testing.T) {
	terms := []string{"auth"}
	base := codeHitScore("pkg/auth.go", "func check() {}", terms)
	line := codeHitScore("pkg/other.go", "call auth helper", terms)
	if base <= line {
		t.Fatalf("basename score %v should beat line-only %v", base, line)
	}
	if base > codeScoreCap {
		t.Fatalf("basename score %v exceeds cap %v", base, codeScoreCap)
	}
}

func TestRouterBoundsGenerationAndReportsLowerBound(t *testing.T) {
	hits := []Hit{
		{ID: "a", ProjectID: "p", Score: 4},
		{ID: "b", ProjectID: "p", Score: 3},
		{ID: "c", ProjectID: "p", Score: 2},
		{ID: "d", ProjectID: "p", Score: 1},
	}
	router := NewRouter(fixedExecutor{hits: hits, partial: true}, nil, nil)
	result, err := router.Execute(context.Background(), &RoutedPlan{
		Store: &StorePlanLeg{Cap: 4},
	}, 3)
	if err != nil {
		testutil.FailErr(t, "execute limited generation", err)
	}
	if len(result.Hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(result.Hits))
	}
	if result.Status != ResultStatusLimited || result.Exhaustive || result.CountRelation != CountRelationLowerBound {
		t.Fatalf("completion = (%q, %v, %q)", result.Status, result.Exhaustive, result.CountRelation)
	}
	if len(result.Issues) != 1 || result.Issues[0].Executor != ExecutorStore || result.Issues[0].Limit != 3 {
		t.Fatalf("issues = %+v", result.Issues)
	}
}

func TestRouterRemovesExecutorProbeHit(t *testing.T) {
	hits := []Hit{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "probe"}}
	router := NewRouter(fixedExecutor{hits: hits, partial: true}, nil, nil)
	result, err := router.Execute(context.Background(), &RoutedPlan{
		Store: &StorePlanLeg{Cap: 4},
	}, 10)
	if err != nil {
		testutil.FailErr(t, "execute probed generation", err)
	}
	if len(result.Hits) != 3 || len(result.Issues) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if issue := result.Issues[0]; issue.Executor != ExecutorStore || issue.Limit != 3 {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestRouterExecutorErrorTakesPartialPrecedence(t *testing.T) {
	router := NewRouter(fixedExecutor{
		hits:    []Hit{{ID: "a"}},
		partial: true,
		err:     errors.New("index unavailable"),
	}, nil, nil)
	result, err := router.Execute(context.Background(), &RoutedPlan{
		Store: &StorePlanLeg{Cap: 1},
	}, 10)
	if err != nil {
		testutil.FailErr(t, "execute partial generation", err)
	}
	if result.Status != ResultStatusPartial || result.Exhaustive || result.CountRelation != CountRelationLowerBound {
		t.Fatalf("completion = (%q, %v, %q)", result.Status, result.Exhaustive, result.CountRelation)
	}
}

func TestStableHitIDIsDeterministicAndDomainSeparated(t *testing.T) {
	a := stableHitID("code", "p", "root", "main.go", "10")
	b := stableHitID("code", "p", "root", "main.go", "10")
	c := stableHitID("file", "p", "root", "main.go", "10")
	d := stableHitID("code", "p", "root", " main.go", "10")
	if a != b {
		t.Fatalf("same identity produced %q and %q", a, b)
	}
	if a == c {
		t.Fatalf("different hit domains produced the same id %q", a)
	}
	if a == d {
		t.Fatalf("distinct path coordinates produced the same id %q", a)
	}
	if _, err := uuid.Parse(a); err != nil {
		testutil.FailErr(t, "parse stable hit id", err)
	}
}

func TestExecutorResultLimitCombinesDualArmCaps(t *testing.T) {
	if got := executorResultLimit(PlanLeg{Code: &CodePlanLeg{
		Lines: true, Files: true, Cap: 17, FileCap: 9,
	}}); got != 24 {
		t.Fatalf("dual-arm limit = %d, want 24", got)
	}
	if got := executorResultLimit(PlanLeg{Code: &CodePlanLeg{
		Files: true, FileCap: 9,
	}}); got != 8 {
		t.Fatalf("file-only limit = %d, want 8", got)
	}
}
