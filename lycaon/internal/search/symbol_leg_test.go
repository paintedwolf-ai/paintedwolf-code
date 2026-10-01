package search

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func compileSymbolPlan(t *testing.T, query string, flags MatchFlags, budget SearchBudget) *RoutedPlan {
	t.Helper()
	ctx := testCompileContext()
	ctx.Flags = flags
	ctx.Budget = budget
	ctx.DependencyPathPatterns = []string{"node_modules"}
	plan, err := CompileQuery(query, ctx)
	testutil.FailErr(t, "compile "+query, err)
	return plan
}

func TestCompileAddsTheSymbolArmForOneLiteralTerm(t *testing.T) {
	plan := compileSymbolPlan(t, "ParseConfig", MatchFlags{}, BudgetComplete)
	if plan.Symbol == nil || plan.Symbol.Name != "ParseConfig" {
		t.Fatalf("symbol leg = %+v, want one naming ParseConfig", plan.Symbol)
	}
	if plan.Code == nil || !plan.Code.Lines || !plan.Code.Files || plan.Store == nil {
		t.Fatalf("plan = %+v, want the code and store legs alongside", plan)
	}
	if !reflect.DeepEqual(plan.Executors, []string{ExecutorStore, ExecutorCode, ExecutorSymbol}) {
		t.Fatalf("executors = %v", plan.Executors)
	}
	roots := plan.Symbol.Roots
	if len(roots) != 2 || roots[0].ProjectID != "proj-origin" || roots[1].ProjectID != "proj-demo" {
		t.Fatalf("symbol roots = %+v, want every attached project, origin first", roots)
	}
	if plan.Symbol.Cap != SymbolLegCap || !reflect.DeepEqual(plan.Symbol.ExcludeDirs, []string{"node_modules"}) {
		t.Fatalf("symbol cap %d excludes %v", plan.Symbol.Cap, plan.Symbol.ExcludeDirs)
	}
	if interactive := compileSymbolPlan(t, "ParseConfig", MatchFlags{}, BudgetInteractive); interactive.Symbol.Cap != InteractiveSymbolMaxHits {
		t.Fatalf("interactive symbol cap = %d, want %d", interactive.Symbol.Cap, InteractiveSymbolMaxHits)
	}
	if scoped := compileSymbolPlan(t, "project:demo Resolve", MatchFlags{}, BudgetComplete); len(scoped.Symbol.Roots) != 1 || scoped.Symbol.Roots[0].ProjectID != "proj-demo" {
		t.Fatalf("scoped symbol roots = %+v", scoped.Symbol.Roots)
	}
}

func TestCompileLeavesOutTheSymbolArmWhenNoNameCanMatch(t *testing.T) {
	for _, tc := range []struct {
		query string
		flags MatchFlags
	}{
		{query: "parse config"},
		{query: `"parse config"`},
		{query: "P"},
		{query: "Parse.*", flags: MatchFlags{Regex: true}},
		{query: "kind:code ParseConfig"},
		{query: "kind:file ParseConfig"},
		{query: "kind:message ParseConfig"},
		{query: "NOT ParseConfig"},
	} {
		if plan := compileSymbolPlan(t, tc.query, tc.flags, BudgetComplete); plan.Symbol != nil {
			t.Errorf("%q: symbol leg = %+v, want none", tc.query, plan.Symbol)
		}
	}
}

func TestCompileKindSymbolRunsOnlyTheSymbolArm(t *testing.T) {
	plan := compileSymbolPlan(t, "kind:symbol parsecfg", MatchFlags{}, BudgetComplete)
	if plan.Symbol == nil || plan.Code != nil || plan.Store != nil {
		t.Fatalf("plan store %v code %v symbol %v, want the symbol leg alone", plan.Store != nil, plan.Code != nil, plan.Symbol != nil)
	}
	either := compileSymbolPlan(t, "(kind:symbol OR kind:code) parsecfg", MatchFlags{}, BudgetComplete)
	if either.Symbol == nil || either.Code == nil || !either.Code.Lines || either.Code.Files || either.Store != nil {
		t.Fatalf("OR plan = %+v, want the symbol and code-line arms", either)
	}
	negated := compileSymbolPlan(t, "NOT kind:symbol parsecfg", MatchFlags{}, BudgetComplete)
	if negated.Symbol != nil || negated.Code == nil || negated.Store == nil {
		t.Fatalf("negated plan = %+v, want every leg but the symbol arm", negated)
	}
}

func TestSymbolDiscoveryScopeCarriesTopLevelPaths(t *testing.T) {
	plan := compileSymbolPlan(t, "kind:symbol path:internal NOT path:internal/gen Resolve", MatchFlags{}, BudgetComplete)
	scope := plan.Symbol.DiscoveryScope()
	want := []Node{
		FilterExpr{Field: "path", Value: "internal"},
		NotExpr{Expr: FilterExpr{Field: "path", Value: "internal/gen"}},
	}
	if len(scope) != len(want) {
		t.Fatalf("scope = %#v, want %#v", scope, want)
	}
	for i := range want {
		if !samePathFilter(scope[i], want[i]) {
			t.Fatalf("scope[%d] = %#v, want %#v", i, scope[i], want[i])
		}
	}
	if bare := compileSymbolPlan(t, "Resolve", MatchFlags{}, BudgetComplete); len(bare.Symbol.DiscoveryScope()) != 0 {
		t.Fatalf("bare scope = %#v, want none", bare.Symbol.DiscoveryScope())
	}
}

func samePathFilter(got, want Node) bool {
	gotNot, gotNegated := got.(NotExpr)
	wantNot, wantNegated := want.(NotExpr)
	if gotNegated != wantNegated {
		return false
	}
	if gotNegated {
		got, want = gotNot.Expr, wantNot.Expr
	}
	g, gok := got.(FilterExpr)
	w, wok := want.(FilterExpr)
	return gok && wok && g.Field == w.Field && g.Value == w.Value
}

func TestSymbolFilterAppliesTheWholeQuery(t *testing.T) {
	plan := compileSymbolPlan(t, "path:internal NOT test Resolve", MatchFlags{Exclude: []string{"**/gen/**"}}, BudgetComplete)
	filter, err := CompileSymbolFilter(plan.Symbol)
	testutil.FailErr(t, "compile symbol filter", err)
	for _, tc := range []struct {
		path, name string
		want       bool
	}{
		{"internal/config/resolve.go", "ResolveHost", true},
		{"cmd/resolve.go", "ResolveHost", false},
		{"internal/config/resolve.go", "ResolveTest", false},
		{"internal/gen/resolve.go", "ResolveHost", false},
	} {
		if got := filter.Admits(tc.path, tc.name); got != tc.want {
			t.Errorf("Admits(%q, %q) = %v, want %v", tc.path, tc.name, got, tc.want)
		}
	}
	hidden := compileSymbolPlan(t, "path:.github Deploy", MatchFlags{}, BudgetComplete)
	hiddenFilter, err := CompileSymbolFilter(hidden.Symbol)
	testutil.FailErr(t, "compile hidden filter", err)
	if !hiddenFilter.Admits(".github/deploy.go", "Deploy") {
		t.Fatal("a path that names a hidden tree must admit declarations inside it")
	}
}

func TestSymbolHitsRankByMatchThenPosition(t *testing.T) {
	decl := func(name string, rank int) SymbolDeclaration {
		return SymbolDeclaration{ProjectID: "p", RootID: "r", Path: "a.go", Line: 3, Name: name, Kind: "function", MatchRank: rank}
	}
	exact := NewSymbolHit(decl("Cfg", 0), 0)
	laterExact := NewSymbolHit(decl("cfg", 1), 1)
	prefix := NewSymbolHit(decl("CfgLoader", 2), 2)
	substring := NewSymbolHit(decl("xcfgx", 5), 3)
	scores := []float64{exact.Score, laterExact.Score, prefix.Score, substring.Score}
	for i := 1; i < len(scores); i++ {
		if scores[i] >= scores[i-1] {
			t.Fatalf("scores = %v, want strictly descending", scores)
		}
	}
	if exact.Score <= fileScoreExactName || substring.Score <= codeScoreCap {
		t.Fatalf("scores = %v, want exact names above exact file names and every name above content lines", scores)
	}
	if exact.HitKind != HitKindSymbol || exact.Title != "Cfg" || exact.Context != "a.go:3" || exact.SymbolKind != "function" || exact.Source != SourceCode {
		t.Fatalf("hit = %+v", exact)
	}
	again := NewSymbolHit(decl("Cfg", 0), 7)
	if again.ID != exact.ID {
		t.Fatal("hit identity must not depend on rank position")
	}
	if other := NewSymbolHit(SymbolDeclaration{ProjectID: "p", RootID: "r", Path: "a.go", Line: 4, Name: "Cfg", Kind: "function"}, 0); other.ID == exact.ID {
		t.Fatal("declarations on different lines need different identities")
	}
}

func TestRouterRunsTheSymbolLegAndReportsItsBudget(t *testing.T) {
	symbolReport := ExecutorReport{
		Hits:   []Hit{NewSymbolHit(SymbolDeclaration{ProjectID: "p", Path: "a.go", Line: 1, Name: "Cfg", Kind: "function"}, 0)},
		Issues: []Issue{{Executor: ExecutorSymbol, Reason: IssueSymbolBudget, Count: 1}},
		Symbol: SymbolLegReport{Projects: 1, FilesOutlined: 2, Declarations: 1},
	}
	router := NewRouter(nil, nil, fakeExecutor{source: ExecutorSymbol, report: symbolReport})
	result, err := router.Execute(t.Context(), &RoutedPlan{Symbol: &SymbolPlanLeg{Name: "Cfg", Cap: 4}}, 100)
	testutil.FailErr(t, "route symbol leg", err)
	if len(result.Hits) != 1 || result.Hits[0].HitKind != HitKindSymbol {
		t.Fatalf("hits = %+v", result.Hits)
	}
	if result.Status != ResultStatusPartial || result.CountRelation != CountRelationLowerBound {
		t.Fatalf("status %q relation %q, want a partial lower bound", result.Status, result.CountRelation)
	}
	if result.Telemetry.Symbol.FilesOutlined != 2 {
		t.Fatalf("telemetry = %+v", result.Telemetry)
	}
	missing, err := NewRouter(nil, nil, nil).Execute(t.Context(), &RoutedPlan{Symbol: &SymbolPlanLeg{Name: "Cfg"}}, 100)
	testutil.FailErr(t, "route without a symbol executor", err)
	if len(missing.Issues) != 1 || missing.Issues[0].Reason != IssueExecutorError || missing.Issues[0].Executor != ExecutorSymbol {
		t.Fatalf("issues = %+v, want an unavailable symbol executor", missing.Issues)
	}
}
