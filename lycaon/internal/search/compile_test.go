package search

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testCompileContext() CompileContext {
	return CompileContext{
		OriginProjectID: "proj-origin",
		ResolveProjectBySlug: func(slug string) (string, error) {
			if slug == "demo" {
				return "proj-demo", nil
			}
			return "", fmt.Errorf("not found")
		},
		RootsForProject: func(projectID string) ([]CodeRoot, error) {
			return []CodeRoot{{RootID: "root-" + projectID, Path: "/roots/" + projectID}}, nil
		},
		AttachedProjectIDs: func() ([]string, error) {
			return []string{"proj-origin", "proj-demo"}, nil
		},
	}
}

func TestCompileGlobalDefaultNoProjectPredicate(t *testing.T) {
	plan, err := CompileQuery("kind:web", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Scope != ScopeGlobal {
		t.Fatalf("scope = %q", plan.Scope)
	}
	if plan.ScopeProjectArgIdx != -1 {
		t.Fatalf("scope arg idx = %d, want -1", plan.ScopeProjectArgIdx)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if idx := strings.Index(plan.Store.SQL, "WHERE"); idx >= 0 {
		end := strings.Index(plan.Store.SQL, "ORDER BY")
		if end < 0 {
			end = len(plan.Store.SQL)
		}
		where := plan.Store.SQL[idx:end]
		if strings.Contains(where, "e.project_id = ?") {
			t.Fatalf("store WHERE should not filter project_id globally: %s", where)
		}
	}
}

func TestCompileProjectCurrentNarrowsScope(t *testing.T) {
	plan, err := CompileQuery("project:current kind:web", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Scope != ScopeCurrent {
		t.Fatalf("scope = %q", plan.Scope)
	}
	if plan.ScopeProjectArgIdx < 0 {
		t.Fatal("expected scoped project arg index")
	}
	if plan.Store.Args[plan.ScopeProjectArgIdx] != "proj-origin" {
		t.Fatalf("scoped arg = %v", plan.Store.Args[plan.ScopeProjectArgIdx])
	}
}

func TestCompileProjectScopeAloneEnablesStoreLeg(t *testing.T) {
	plan, err := CompileQuery("project:current", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg for project:current alone")
	}
	if plan.Code != nil {
		t.Fatal("project:current alone should not fan out to code leg")
	}
	if !strings.Contains(plan.Store.SQL, "e.project_id = ?") {
		t.Fatalf("missing project scope predicate: %s", plan.Store.SQL)
	}
}

func TestCompileProjectScopeWithKindCodeWalksLiveTree(t *testing.T) {
	plan, err := CompileQuery("kind:code project:current", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil || !plan.Code.Lines {
		t.Fatalf("code leg = %+v", plan.Code)
	}
	if plan.Store != nil {
		t.Fatal("kind:code should not include store leg")
	}
}

func TestCompileProjectSlugNarrowsScope(t *testing.T) {
	plan, err := CompileQuery("project:demo finding", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Scope != ScopeSlug || plan.Interpretation.Slug != "demo" {
		t.Fatalf("scope = %q slug=%q", plan.Scope, plan.Interpretation.Slug)
	}
	if plan.Store.Args[plan.ScopeProjectArgIdx] != "proj-demo" {
		t.Fatalf("scoped arg = %v", plan.Store.Args[plan.ScopeProjectArgIdx])
	}
}

func TestCompileDefaultExcludesFindings(t *testing.T) {
	plan, err := CompileQuery("auth", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if !strings.Contains(plan.Store.SQL, "e.hit_kind != ?") {
		t.Fatalf("expected default finding exclusion: %s", plan.Store.SQL)
	}
	found := false
	for _, arg := range plan.Store.Args {
		if arg == HitKindFinding {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected HitKindFinding bound in args: %v", plan.Store.Args)
	}
}

func TestCompileDefaultExcludesDrafts(t *testing.T) {
	plan, err := CompileQuery("auth", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil || !strings.Contains(plan.Store.SQL, "COALESCE(e.role, '') != ?") {
		t.Fatalf("default store SQL must exclude drafts: %+v", plan.Store)
	}
	found := false
	for _, arg := range plan.Store.Args {
		if arg == "draft" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default store args must bind draft exclusion: %v", plan.Store.Args)
	}
}

func TestCompileRoleDraftOptsIn(t *testing.T) {
	plan, err := CompileQuery("role:draft auth", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("role:draft must route to store")
	}
	if strings.Contains(plan.Store.SQL, "COALESCE(e.role, '') != ?") {
		t.Fatalf("role:draft must remove the default draft exclusion: %s", plan.Store.SQL)
	}
}

func TestCompileKindFindingOptsIn(t *testing.T) {
	plan, err := CompileQuery("kind:finding auth", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if strings.Contains(plan.Store.SQL, "e.hit_kind != ?") {
		t.Fatalf("kind:finding must not apply default exclusion: %s", plan.Store.SQL)
	}
	if !strings.Contains(plan.Store.SQL, "e.hit_kind = ?") {
		t.Fatalf("expected kind:finding predicate: %s", plan.Store.SQL)
	}
}

func TestCompileKindScanOptsIn(t *testing.T) {
	plan, err := CompileQuery("kind:scan", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if strings.Contains(plan.Store.SQL, "e.hit_kind != ?") {
		t.Fatalf("kind:scan must not apply default exclusion: %s", plan.Store.SQL)
	}
}

func TestCompileOriginBiasInOrderNotWhere(t *testing.T) {
	plan, err := CompileQuery("auth", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if !strings.Contains(plan.Store.SQL, "ORDER BY (e.project_id = ?) DESC") {
		t.Fatalf("missing origin ranking ORDER BY: %s", plan.Store.SQL)
	}
	if !strings.Contains(plan.Store.SQL, "score DESC") {
		t.Fatalf("free-text must order by BM25 score: %s", plan.Store.SQL)
	}
	if count := strings.Count(plan.Store.SQL, "e.project_id = ?"); count != 1 {
		t.Fatalf("global query should only bind origin ranking, found %d project predicates: %s", count, plan.Store.SQL)
	}
	if plan.OriginRankArgIdx < 0 || plan.Store.Args[plan.OriginRankArgIdx] != "proj-origin" {
		t.Fatalf("origin rank arg missing: idx=%d args=%v", plan.OriginRankArgIdx, plan.Store.Args)
	}
}

func TestCompileFreeTextEmitsBM25Score(t *testing.T) {
	plan, err := CompileQuery("password leak", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	sql := plan.Store.SQL
	if !strings.Contains(sql, "-bm25(evidence_fts)") || !strings.Contains(sql, "-bm25(messages_fts)") {
		t.Fatalf("expected negated bm25 on both FTS arms: %s", sql)
	}
	if !strings.Contains(sql, "COALESCE(s.score, 0.0) AS score") {
		t.Fatalf("expected score column: %s", sql)
	}
	if strings.Contains(sql, "e.rowid IN (SELECT rowid FROM evidence_fts") {
		t.Fatalf("FTS filter-only IN shape should be replaced by scored join: %s", sql)
	}
	matchCount := 0
	for _, arg := range plan.Store.Args {
		if s, ok := arg.(string); ok && strings.Contains(s, `"password"`) {
			matchCount++
		}
	}
	if matchCount != 2 {
		t.Fatalf("expected two MATCH bindings, got %d in args %v", matchCount, plan.Store.Args)
	}
}

func TestCompileFilterOnlyUsesZeroScore(t *testing.T) {
	plan, err := CompileQuery("kind:web", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	sql := plan.Store.SQL
	if !strings.Contains(sql, "0.0 AS score") {
		t.Fatalf("filter-only must select 0.0 AS score: %s", sql)
	}
	if strings.Contains(sql, "bm25") {
		t.Fatalf("filter-only must not use bm25: %s", sql)
	}
	if strings.Contains(sql, "score DESC") {
		t.Fatalf("filter-only must not order by score: %s", sql)
	}
}

func TestCompileKindCodeFansOutToCodeExecutor(t *testing.T) {
	plan, err := CompileQuery("kind:code", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil {
		t.Fatal("expected code leg")
	}
	if plan.Store != nil {
		t.Fatal("kind:code should not include store leg")
	}
	if len(plan.Executors) != 1 || plan.Executors[0] != ExecutorCode {
		t.Fatalf("executors = %v", plan.Executors)
	}
}

func TestCompileRoutesFileAndLineArmsOfTheCodeLeg(t *testing.T) {
	// Free text means "find this anywhere" — content lines and file names both.
	free, err := CompileQuery("crossbar", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery(free): %v", err)
	}
	if free.Code == nil || !free.Code.Lines || !free.Code.Files {
		t.Fatalf("free text must arm both code arms: %+v", free.Code)
	}

	// kind:file is the navigation lane: paths only, still the code executor.
	files, err := CompileQuery("kind:file toolbar", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery(kind:file): %v", err)
	}
	if files.Code == nil || !files.Code.Files || files.Code.Lines {
		t.Fatalf("kind:file must arm files only: %+v", files.Code)
	}

	// kind:code selects content matches only.
	code, err := CompileQuery("kind:code toolbar", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery(kind:code): %v", err)
	}
	if code.Code == nil || !code.Code.Lines || code.Code.Files {
		t.Fatalf("kind:code must arm lines only: %+v", code.Code)
	}
}

func TestCompileCarriesDependencyExcludesToTheFileArm(t *testing.T) {
	ctx := testCompileContext()
	ctx.DependencyPathPatterns = []string{"node_modules", "dist"}
	plan, err := CompileQuery("crossbar", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if got := plan.Code.FileExcludeDirs; len(got) != 2 || got[0] != "node_modules" {
		t.Fatalf("exclude dirs = %v, want the injected catalog list", got)
	}
}

func TestCompileCodeRootsCarryProjectScope(t *testing.T) {
	// Each code root carries its project scope.
	scoped, err := CompileQuery("kind:code project:demo", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery(scoped): %v", err)
	}
	if got := scoped.Code.PathRoots; len(got) != 1 || got[0].ProjectID != "proj-demo" || got[0].RootID != "root-proj-demo" {
		t.Fatalf("scoped roots = %+v, want one root for proj-demo", got)
	}

	global, err := CompileQuery("kind:code", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery(global): %v", err)
	}
	if len(global.Code.PathRoots) != 2 {
		t.Fatalf("global roots = %+v, want one per attached project", global.Code.PathRoots)
	}
	for _, root := range global.Code.PathRoots {
		if root.ProjectID == "" || root.RootID != "root-"+root.ProjectID {
			t.Fatalf("global root %q has no project scope", root.Path)
		}
	}
	if global.Code.PathRoots[0].ProjectID != "proj-origin" {
		t.Fatalf("origin roots must lead: %+v", global.Code.PathRoots)
	}
}

func TestCompileInteractiveBudgetCaps(t *testing.T) {
	ctx := testCompileContext()
	ctx.Budget = BudgetInteractive
	plan, err := CompileQuery("toolbar", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil || plan.Code.Cap != InteractiveCodeMaxHits+1 || plan.Code.FileCap != InteractiveFileMaxHits+1 {
		t.Fatalf("interactive code caps = %+v", plan.Code)
	}
	if plan.Store == nil || plan.Store.Cap != InteractiveStoreMaxHits+1 {
		t.Fatalf("interactive store cap = %+v", plan.Store)
	}
}

func TestCompileOriginFirstWhenAttachedOrderDiffers(t *testing.T) {
	ctx := testCompileContext()
	ctx.AttachedProjectIDs = func() ([]string, error) {
		return []string{"proj-demo", "proj-origin"}, nil
	}
	plan, err := CompileQuery("kind:code", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil || len(plan.Code.PathRoots) != 2 || plan.Code.PathRoots[0].ProjectID != "proj-origin" {
		t.Fatalf("origin-first roots = %+v", plan.Code.PathRoots)
	}
}

func TestCompileKindFilterUsesHitKindColumn(t *testing.T) {
	// Hit categories live in hit_kind; kind stores evidence subtypes.
	plan, err := CompileQuery("kind:evidence", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery(kind:evidence): %v", err)
	}
	if plan.Store == nil {
		t.Fatal("kind:evidence: expected store leg")
	}
	if !strings.Contains(plan.Store.SQL, "e.hit_kind = ?") {
		t.Fatalf("kind:evidence: predicate must target e.hit_kind: %s", plan.Store.SQL)
	}
	if strings.Contains(plan.Store.SQL, "e.kind = ?") {
		t.Fatalf("kind:evidence: predicate must not target e.kind: %s", plan.Store.SQL)
	}
}

func TestCompileFreeTextFansOutStoreAndCode(t *testing.T) {
	plan, err := CompileQuery("password leak", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil || plan.Code == nil {
		t.Fatal("expected store and code legs for free text")
	}
	if len(plan.Executors) != 2 {
		t.Fatalf("executors = %v", plan.Executors)
	}
}

func TestCompileRefFilter(t *testing.T) {
	plan, err := CompileQuery(`ref:call-7 session:sess-a kind:tool source:tool shape:raw`, testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if !strings.Contains(plan.Store.SQL, "e.source_ref = ?") {
		t.Fatalf("SQL missing source_ref predicate: %s", plan.Store.SQL)
	}
}

func TestCompileInjectionSafety(t *testing.T) {
	payloads := []string{
		"OR 1=1",
		";DROP TABLE evidence_index",
		"percent%underscore_",
	}
	for _, payload := range payloads {
		plan, err := CompileQuery(fmt.Sprintf("session:%q", payload), testCompileContext())
		if err != nil {
			t.Fatalf("CompileQuery(session:%q): %v", payload, err)
		}
		if plan.Store == nil {
			t.Fatalf("expected store leg for %q", payload)
		}
		if strings.Contains(plan.Store.SQL, payload) {
			t.Fatalf("payload %q leaked into SQL: %s", payload, plan.Store.SQL)
		}
		found := false
		for _, arg := range plan.Store.Args {
			if s, ok := arg.(string); ok && s == payload {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("payload %q not bound as query arg: %v", payload, plan.Store.Args)
		}
	}
}

func TestCompileGoldenGlobalWebFilter(t *testing.T) {
	plan, err := CompileQuery("kind:web cowrie", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Interpretation.Scope != ScopeGlobal {
		t.Fatalf("interp scope = %q", plan.Interpretation.Scope)
	}
	if len(plan.Interpretation.FTSTerms) != 1 || plan.Interpretation.FTSTerms[0] != "cowrie" {
		t.Fatalf("fts terms = %v", plan.Interpretation.FTSTerms)
	}
}

func TestCompileRejectsQueriesWithoutSearchableTerms(t *testing.T) {
	for _, query := range []string{"[", "{}", "!?+", "[] {}"} {
		_, err := CompileQuery(query, testCompileContext())
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Kind != ParseErrSyntax {
			t.Fatalf("CompileQuery(%q) error = %v, want syntax ParseError", query, err)
		}
	}
}

func TestCompileRejectsEmptyBooleanNodes(t *testing.T) {
	for _, ast := range []Node{AndExpr{}, OrExpr{}} {
		_, err := compileSearchAST(ast, testCompileContext())
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Kind != ParseErrSyntax {
			t.Fatalf("compileSearchAST(%T) error = %v, want syntax ParseError", ast, err)
		}
	}
}

func TestCompileOrQueryStaysGlobal(t *testing.T) {
	plan, err := CompileQuery("alpha OR beta", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Scope != ScopeGlobal {
		t.Fatalf("scope = %q want %q", plan.Scope, ScopeGlobal)
	}
	if plan.ScopeProjectArgIdx != -1 {
		t.Fatalf("scope arg idx = %d, want -1", plan.ScopeProjectArgIdx)
	}
	// Each text term gets its own cross-source candidate table; the OR lives
	// in the SQL predicate, not inside one FTS MATCH.
	if !strings.Contains(plan.Store.SQL, "text_match_1") ||
		!strings.Contains(plan.Store.SQL, " OR EXISTS") {
		t.Fatalf("store SQL should OR two text tables: %s", plan.Store.SQL)
	}
	if _, ok := plan.Code.Query.(OrExpr); !ok {
		t.Fatalf("code query = %#v, want OrExpr", plan.Code.Query)
	}
}

func TestCompileRejectsProjectScopeInsideOr(t *testing.T) {
	_, err := CompileQuery("project:current OR alpha", testCompileContext())
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Field != "project" {
		t.Fatalf("error = %v, want project parse error", err)
	}
}

func TestCompileRoutesNegatedStoreFilterToStore(t *testing.T) {
	plan, err := CompileQuery("NOT kind:web", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("negated store filter must retain the store executor")
	}
	if !strings.Contains(plan.Store.SQL, "NOT ((e.hit_kind = ?))") {
		t.Fatalf("store SQL does not preserve negation: %s", plan.Store.SQL)
	}
}

func TestReplacePatternRejectsBooleanAmbiguity(t *testing.T) {
	plan, err := CompileQuery("alpha OR beta", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	_, err = replacementPattern(plan.Code.Query)
	var matchErr *MatchError
	if !errors.As(err, &matchErr) {
		t.Fatalf("error = %v, want MatchError", err)
	}
}

func TestReplacePatternRequiresExactlyOneTextExpression(t *testing.T) {
	for _, query := range []string{"alpha beta", "kind:code"} {
		plan, err := CompileQuery(query, testCompileContext())
		if err != nil {
			t.Fatalf("CompileQuery(%q): %v", query, err)
		}
		_, err = replacementPattern(plan.Code.Query)
		var matchErr *MatchError
		if !errors.As(err, &matchErr) {
			t.Fatalf("replacementPattern(%q) error = %v, want MatchError", query, err)
		}
	}
}

func TestReplacePatternAcceptsOneTextExpressionWithFilters(t *testing.T) {
	plan, err := CompileQuery(`kind:code "alpha beta"`, testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	pattern, err := replacementPattern(plan.Code.Query)
	if err != nil {
		t.Fatalf("ReplacePattern: %v", err)
	}
	if pattern.Text != "alpha beta" || !pattern.Phrase {
		t.Fatalf("pattern = %+v", pattern)
	}
}

func TestCompilePathFilterEscapesLikeMetacharacters(t *testing.T) {
	plan, err := CompileQuery("path:internal/search/store_executor.go", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if !strings.Contains(plan.Store.SQL, `ESCAPE '\'`) {
		t.Fatalf("path LIKE needs an ESCAPE clause: %s", plan.Store.SQL)
	}
	if plan.Code != nil {
		for _, root := range plan.Code.PathRoots {
			if strings.Contains(root.Path, `\`) {
				t.Fatalf("code-leg path root must not carry LIKE escapes: %q", root.Path)
			}
		}
	}
}

func TestCompileToolFilterBindsToolName(t *testing.T) {
	plan, err := CompileQuery("tool:command", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	found := false
	for _, arg := range plan.Store.Args {
		if s, ok := arg.(string); ok && s == "command" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool name not bound: %v", plan.Store.Args)
	}
	if strings.Contains(plan.Store.SQL, "hit_kind = ?") {
		t.Fatalf("tool filter must match the handle column, not hit_kind: %s", plan.Store.SQL)
	}
}

func TestCompileNegatedFreeTextExcludesOnBothLegs(t *testing.T) {
	plan, err := CompileQuery("alpha NOT beta", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Store == nil || plan.Code == nil {
		t.Fatalf("plan legs = store %v code %v, want both", plan.Store != nil, plan.Code != nil)
	}
	if !strings.Contains(plan.Store.SQL, "NOT (EXISTS") {
		t.Fatalf("store SQL does not exclude the negated term: %s", plan.Store.SQL)
	}
	// Only the positive term is scored and highlighted.
	if got := plan.Interpretation.FTSTerms; len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("fts terms = %v, want [alpha]", got)
	}
	if got := queryTextTerms(plan.Code.Query); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("code terms = %v, want [alpha]", got)
	}
	matcher, err := compileCodeQuery(plan.Code.Query, MatchFlags{})
	if err != nil {
		t.Fatalf("compileCodeQuery: %v", err)
	}
	if !matcher.matches(codeCandidate{kind: HitKindCode, text: "alpha only"}) {
		t.Fatal("line with alpha and no beta must match")
	}
	if matcher.matches(codeCandidate{kind: HitKindCode, text: "alpha and beta"}) {
		t.Fatal("line with beta must be excluded")
	}
}

func TestCompileNegatedFreeTextUnderFlagsLeavesExclusionToTheRowPass(t *testing.T) {
	ctx := testCompileContext()
	ctx.Flags = MatchFlags{CaseSensitive: true}
	plan, err := CompileQuery("alpha NOT beta", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	// FTS folds case, so SQL must not drop rows the flag would keep.
	if strings.Contains(plan.Store.SQL, "NOT (EXISTS") {
		t.Fatalf("store SQL excludes on folded FTS candidates: %s", plan.Store.SQL)
	}
	if !strings.Contains(plan.Store.SQL, "NOT (0)") {
		t.Fatalf("store SQL should admit every candidate for the negated term: %s", plan.Store.SQL)
	}
	post := plan.Store.Post
	if post == nil || post.expr == nil {
		t.Fatal("flagged negated text needs a per-row pass")
	}
	if !post.keep("", []string{"alpha Beta"}, nil) {
		t.Fatal("row with alpha and only capitalised Beta must be kept under case-sensitive NOT beta")
	}
	if post.keep("", []string{"alpha beta"}, nil) {
		t.Fatal("row with lowercase beta must be dropped")
	}
}

func TestCompileExplicitDependencyRequestCoversCodeFilesAndSymbols(t *testing.T) {
	ctx := testCompileContext()
	ctx.DependencyPathPatterns = []string{"node_modules", "dist"}
	ctx.IncludeDependencies = true
	plan, err := CompileQuery("SharedName", ctx)
	if err != nil {
		t.Fatalf("compile dependency search: %v", err)
	}
	if !plan.Code.IncludeDependencies || !plan.Symbol.IncludeDependencies || len(plan.Code.FileExcludeDirs) != 0 || len(plan.Code.LineExcludeDirs) != 0 || len(plan.Symbol.ExcludeDirs) != 0 || plan.Interpretation.DependencyTreesExcluded {
		t.Fatalf("explicit dependency request retained exclusions: %+v", plan)
	}
}
