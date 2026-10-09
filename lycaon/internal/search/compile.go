package search

import (
	"fmt"
	"strings"
)

type compileState struct {
	ctx CompileContext
	ast Node
	// ftsTerms are the positive free-text terms; hits score and highlight on them.
	ftsTerms []string
	// hasText covers negated terms too; with flags they need a per-row pass.
	hasText bool
	// rowPassText marks a term the FTS index cannot hold; it is applied per row.
	rowPassText        bool
	filters            []InterpretedFilter
	storeFilters       []compiledStoreFilter
	scope              ProjectScopeMode
	scopeSlug          string
	scopeProjectID     string
	scopeProjectArgIdx int
	originRankArgIdx   int
	wantCode           bool
	wantStore          bool
	// Free text selects both code arms; kind filters narrow the selection.
	codeLines bool
	codeFiles bool
	// symbol is the declaration name the symbol arm answers; empty is no arm.
	symbol string
	// Finding filters admit scan findings into search results.
	includeFindings bool
	// role:draft admits private coordinator drafts.
	includeDrafts bool
}

type compiledStoreFilter struct {
	predicate string
	args      []any
}

// CompileQuery parses and compiles a DSL query into a routed plan.
func CompileQuery(query string, ctx CompileContext) (*RoutedPlan, error) {
	ast, err := ParseQuery(query)
	if err != nil {
		return nil, err
	}
	return compileSearchAST(ast, ctx)
}

func compileSearchAST(ast Node, ctx CompileContext) (*RoutedPlan, error) {
	if ast == nil {
		return nil, newParseError(0, ParseErrSyntax, "", "empty query")
	}
	st := &compileState{
		ctx:                ctx,
		ast:                ast,
		scope:              ScopeGlobal,
		scopeProjectArgIdx: -1,
		originRankArgIdx:   -1,
	}
	if err := st.walk(ast, false); err != nil {
		return nil, err
	}
	if err := checkFilterContradictions(ast); err != nil {
		return nil, err
	}
	st.decideExecutors()
	plan := &RoutedPlan{
		OriginProjectID:    strings.TrimSpace(ctx.OriginProjectID),
		Scope:              st.scope,
		ScopeProjectArgIdx: st.scopeProjectArgIdx,
		OriginRankArgIdx:   st.originRankArgIdx,
		Interpretation: SearchInterpretation{
			Scope:    st.scope,
			Slug:     st.scopeSlug,
			FTSTerms: append([]string(nil), st.ftsTerms...),
			Filters:  append([]InterpretedFilter(nil), st.filters...),
		},
	}
	if st.wantStore {
		store, err := st.buildStoreLeg()
		if err != nil {
			return nil, err
		}
		plan.Store = store
		plan.ScopeProjectArgIdx = st.scopeProjectArgIdx
		plan.OriginRankArgIdx = st.originRankArgIdx
		plan.Executors = append(plan.Executors, ExecutorStore)
	}
	if st.wantCode {
		code, err := st.buildCodeLeg()
		if err != nil {
			return nil, err
		}
		plan.Code = code
		plan.Executors = append(plan.Executors, ExecutorCode)
		plan.Interpretation.DependencyTreesExcluded = code.Lines && len(code.LineExcludeDirs) > 0
	}
	if st.symbol != "" {
		symbol, err := st.buildSymbolLeg(st.symbol)
		if err != nil {
			return nil, err
		}
		plan.Symbol = symbol
		plan.Executors = append(plan.Executors, ExecutorSymbol)
		plan.Interpretation.DependencyTreesExcluded = plan.Interpretation.DependencyTreesExcluded || len(symbol.ExcludeDirs) > 0
	}
	return plan, nil
}

func (st *compileState) walk(n Node, negated bool) error {
	switch v := n.(type) {
	case AndExpr:
		if len(v.Exprs) == 0 {
			return newParseError(0, ParseErrSyntax, "", "AND requires an expression")
		}
		for _, child := range v.Exprs {
			if err := st.walk(child, negated); err != nil {
				return err
			}
		}
	case OrExpr:
		if len(v.Exprs) == 0 {
			return newParseError(0, ParseErrSyntax, "", "OR requires an expression")
		}
		if containsProjectScope(v) {
			return newParseError(0, ParseErrInvalidValue, "project", "project scope cannot be used inside OR")
		}
		for _, child := range v.Exprs {
			if err := st.walk(child, negated); err != nil {
				return err
			}
		}
	case NotExpr:
		return st.walk(v.Expr, !negated)
	case FilterExpr:
		return st.compileFilter(v, negated)
	case TextExpr:
		st.hasText = true
		if !ftsIndexable(v.Text) {
			st.rowPassText = true
		}
		if !negated {
			st.ftsTerms = append(st.ftsTerms, v.Text)
		}
		return nil
	default:
		return newParseError(0, ParseErrSyntax, "", "invalid AST node")
	}
	return nil
}

func (st *compileState) compileFilter(f FilterExpr, negated bool) error {
	field := strings.ToLower(strings.TrimSpace(f.Field))
	value := strings.TrimSpace(f.Value)
	st.filters = append(st.filters, InterpretedFilter{Field: field, Value: value, Negated: negated})

	if err := checkFilterVocabulary(field, value, f.Offset); err != nil {
		return err
	}

	if field == "project" {
		if negated {
			return newParseError(f.Offset, ParseErrInvalidValue, field, "project scope cannot be negated")
		}
		return st.applyProjectScope(value, f.Offset)
	}

	if field == "kind" {
		v := strings.ToLower(value)
		if !negated && (v == HitKindFinding || v == scanKind) {
			st.includeFindings = true
		}
	} else if !negated {
		if field == "source" && strings.EqualFold(value, SourceFinding) {
			st.includeFindings = true
		}
		if field == "check" {
			st.includeFindings = true
		}
		if field == "role" && strings.EqualFold(value, "draft") {
			st.includeDrafts = true
		}
	}

	pred, predArgs, err := filterPredicate(field, value, f.Offset)
	if err != nil {
		return err
	}
	st.storeFilters = append(st.storeFilters, compiledStoreFilter{predicate: pred, args: predArgs})
	return nil
}

func (st *compileState) applyProjectScope(value string, offset int) error {
	v := strings.TrimSpace(value)
	if strings.EqualFold(v, ProjectScopeCurrent) {
		projectID := strings.TrimSpace(st.ctx.OriginProjectID)
		if projectID == "" {
			return newParseError(offset, ParseErrInvalidValue, "project", "project:current requires origin_project_id")
		}
		return st.setProjectScope(ScopeCurrent, "", projectID, offset)
	}
	if v == "" {
		return newParseError(offset, ParseErrEmptyValue, "project", "empty project scope")
	}
	if st.ctx.ResolveProjectBySlug == nil {
		return newParseError(offset, ParseErrInvalidValue, "project", "project slug resolution unavailable")
	}
	id, err := st.ctx.ResolveProjectBySlug(v)
	if err != nil {
		return newParseError(offset, ParseErrInvalidValue, "project", err.Error())
	}
	if strings.TrimSpace(id) == "" {
		return newParseError(offset, ParseErrInvalidValue, "project", "unknown project slug")
	}
	return st.setProjectScope(ScopeSlug, v, id, offset)
}

func (st *compileState) setProjectScope(scope ProjectScopeMode, slug, projectID string, offset int) error {
	if st.scope != ScopeGlobal && (st.scope != scope || st.scopeProjectID != projectID) {
		return newParseError(offset, ParseErrInvalidValue, "project", "query contains conflicting project scopes")
	}
	st.scope = scope
	st.scopeSlug = slug
	st.scopeProjectID = projectID
	return nil
}

// checkFilterVocabulary rejects values outside a field's closed vocabulary,
// carried in the shared grammar catalog Den also lints against.
func checkFilterVocabulary(field, value string, offset int) error {
	allowed := dslFieldValues[field]
	if len(allowed) == 0 {
		return nil
	}
	for _, v := range allowed {
		if strings.EqualFold(value, v) {
			return nil
		}
	}
	return newParseError(offset, ParseErrInvalidValue, field,
		fmt.Sprintf("unknown %s value %q — one of %s", field, value, strings.Join(allowed, ", ")))
}

type conjunctiveFilter struct {
	value   string
	negated bool
	offset  int
}

// filterValuesEqual folds case, and folds boolean spellings for the boolean
// fields so verified:true and verified:yes read as the same value.
func filterValuesEqual(field, a, b string) bool {
	if field == "verified" || field == "untrusted" {
		av, aerr := parseVerified(a)
		bv, berr := parseVerified(b)
		if aerr == nil && berr == nil {
			return av == bv
		}
	}
	return strings.EqualFold(a, b)
}

// checkFilterContradictions flags queries that are provably empty: equality
// filters ANDed with a different value, or a filter both required and negated.
// Only pure-AND context counts — alternatives under OR are fine.
func checkFilterContradictions(n Node) error {
	byField := map[string][]conjunctiveFilter{}
	collectConjunctiveFilters(n, false, byField)
	for field, entries := range byField {
		// Single-valued fields come from the shared grammar catalog: two
		// different required values can never match one hit.
		_, equality := dslSingleValuedFields[field]
		var positive *conjunctiveFilter
		for i := range entries {
			e := entries[i]
			if e.negated {
				continue
			}
			if positive != nil && equality && !filterValuesEqual(field, positive.value, e.value) {
				return newParseError(e.offset, ParseErrInvalidValue, field, fmt.Sprintf(
					"%s:%s and %s:%s cannot both match one hit — join alternatives with OR, e.g. (%s:%s OR %s:%s)",
					field, positive.value, field, e.value, field, positive.value, field, e.value))
			}
			if positive == nil {
				positive = &entries[i]
			}
		}
		for _, e := range entries {
			if !e.negated || positive == nil || !filterValuesEqual(field, positive.value, e.value) {
				continue
			}
			return newParseError(e.offset, ParseErrInvalidValue, field, fmt.Sprintf(
				"%s:%s is both required and negated — remove one of the two", field, e.value))
		}
	}
	return nil
}

// collectConjunctiveFilters gathers filters that every hit must satisfy.
// Alternatives are skipped: OR children, and — by De Morgan — AND children
// under negation. OR children under negation are conjunctive (NOT a AND NOT b).
func collectConjunctiveFilters(n Node, negated bool, out map[string][]conjunctiveFilter) {
	switch v := n.(type) {
	case AndExpr:
		if negated {
			return
		}
		for _, child := range v.Exprs {
			collectConjunctiveFilters(child, false, out)
		}
	case OrExpr:
		if !negated {
			return
		}
		for _, child := range v.Exprs {
			collectConjunctiveFilters(child, true, out)
		}
	case NotExpr:
		collectConjunctiveFilters(v.Expr, !negated, out)
	case FilterExpr:
		field := strings.ToLower(strings.TrimSpace(v.Field))
		out[field] = append(out[field], conjunctiveFilter{
			value:   strings.TrimSpace(v.Value),
			negated: negated,
			offset:  v.Offset,
		})
	}
}

func filterPredicate(field, value string, offset int) (string, []any, error) {
	switch field {
	case "kind":
		if strings.EqualFold(value, scanKind) {
			// kind:scan targets the scanner sub-kind on finding rows.
			return "e.kind = ?", []any{scanKind}, nil
		}
		// Facets group by hit_kind; kind stores an internal sub-kind.
		return "e.hit_kind = ?", []any{value}, nil
	case "shape":
		return "e.shape = ?", []any{value}, nil
	case "source":
		return "e.source = ?", []any{value}, nil
	case "path":
		pred, args := pathFilterSQL(value)
		return pred, args, nil
	case "session":
		return "e.session_id = ?", []any{value}, nil
	case "agent":
		return "COALESCE(search_session.agent_type, '') = ?", []any{value}, nil
	case "role":
		return "e.role = ?", []any{value}, nil
	case "handle":
		return "e.handle = ?", []any{value}, nil
	case "leg":
		return "e.leg_id = ?", []any{value}, nil
	case "url":
		return "e.url = ?", []any{value}, nil
	case "verified":
		b, err := parseVerified(value)
		if err != nil {
			return "", nil, newParseError(offset, ParseErrInvalidValue, field, err.Error())
		}
		return "e.verified = ?", []any{b}, nil
	case "untrusted":
		b, err := parseVerified(value)
		if err != nil {
			return "", nil, newParseError(offset, ParseErrInvalidValue, field, err.Error())
		}
		return "e.untrusted = ?", []any{b}, nil
	case "check":
		return "e.check_id = ?", []any{value}, nil
	case "trust":
		return "e.trust = ?", []any{value}, nil
	case "tool":
		if strings.TrimSpace(value) == "" {
			return "e.source = ?", []any{SourceTool}, nil
		}
		return "e.tool = ?", []any{value}, nil
	case "before":
		ts, err := parseTimeBound(value)
		if err != nil {
			return "", nil, newParseError(offset, ParseErrInvalidValue, field, field+": "+err.Error())
		}
		return "e.ts < ?", []any{ts}, nil
	case "after":
		ts, err := parseTimeBound(value)
		if err != nil {
			return "", nil, newParseError(offset, ParseErrInvalidValue, field, field+": "+err.Error())
		}
		return "e.ts >= ?", []any{ts}, nil
	case "run":
		return "e.workflow_run_id = ?", []any{value}, nil
	case "verdict":
		return "e.verdict = ?", []any{value}, nil
	case "ref":
		return "e.source_ref = ?", []any{value}, nil
	default:
		return "", nil, newParseError(offset, ParseErrSyntax, field, "invalid filter field "+field)
	}
}

func (st *compileState) decideExecutors() {
	if st.ctx.StoreOnly {
		st.wantCode = false
		st.wantStore = true
		return
	}
	codeRequested := len(st.ftsTerms) > 0 || containsCodeKind(st.ast)
	st.codeLines = codeRequested && queryCanMatchCandidate(st.ast, HitKindCode)
	st.codeFiles = codeRequested && queryCanMatchCandidate(st.ast, HitKindFile)
	st.wantCode = st.codeLines || st.codeFiles
	if name := st.symbolName(); name != "" && queryCanMatchCandidate(st.ast, HitKindSymbol) {
		st.symbol = name
	}
	st.wantStore = queryCanMatchCandidate(st.ast, ExecutorStore)
}

const storeSelectCols = `e.id, e.project_id, e.hit_kind, e.source, e.session_id, e.source_ref, e.snippet, e.ts,
		e.path, e.url, e.leg_id, e.handle, e.tool, e.trust, e.verified, e.hint_code, e.line,
		COALESCE(search_session.agent_type, ''),
		e.tombstoned, COALESCE(e.untrusted, 0), COALESCE(e.kind, ''), e.message_id`

// storeSessionJoin reads agent_type at query time rather than copying it into
// the projection, so a row follows its session's current role.
const storeSessionJoin = "\n\tLEFT JOIN sessions search_session ON search_session.id = e.session_id"

func (st *compileState) buildCodeLeg() (*CodePlanLeg, error) {
	roots, err := st.codePathRoots()
	if err != nil {
		return nil, err
	}
	lineCap, fileCap := st.ctx.Budget.codeCaps()
	excludes := append([]string(nil), st.ctx.DependencyPathPatterns...)
	if st.ctx.IncludeDependencies {
		excludes = nil
	}
	return &CodePlanLeg{
		IncludeDependencies: st.ctx.IncludeDependencies,
		Query:               st.ast,
		PathRoots:           orderCodeRoots(roots, st.ctx.OriginProjectID),
		Cap:                 lineCap,
		Lines:               st.codeLines,
		Files:               st.codeFiles,
		FileCap:             fileCap,
		FileExcludeDirs:     st.lineExcludeDirs(excludes),
		LineExcludeDirs:     st.lineExcludeDirs(excludes),
		Flags:               st.ctx.Flags,
		Budget:              st.ctx.Budget,
	}, nil
}

// lineExcludeDirs keeps dependency and build trees out of content scans; a
// query naming such a tree (path: filter, include glob) re-admits it.
func (st *compileState) lineExcludeDirs(excludes []string) []string {
	targets := make([]string, 0, len(st.ctx.Flags.Include)+len(st.filters))
	for _, f := range st.filters {
		if f.Field == "path" && !f.Negated {
			targets = append(targets, f.Value)
		}
	}
	targets = append(targets, st.ctx.Flags.Include...)
	if len(targets) == 0 {
		return excludes
	}
	kept := make([]string, 0, len(excludes))
	for _, pattern := range excludes {
		if excludeTargetedBy(pattern, targets) {
			continue
		}
		kept = append(kept, pattern)
	}
	return kept
}

// excludeTargetedBy reports whether an explicit path target reaches into the
// tree the exclude pattern names — the same segment semantics the exclusion
// itself applies (dependencyDirSet), ASCII-case-folded like path: matching.
func excludeTargetedBy(pattern string, targets []string) bool {
	set := dependencyDirSet([]string{asciiLower(pattern)})
	if set.empty() {
		return false
	}
	for _, target := range targets {
		t := asciiLower(strings.Trim(strings.TrimSpace(target), "/"))
		if t == "" {
			continue
		}
		if underDependencyDir(t, set) {
			return true
		}
		// The target may name the tree root itself, without a trailing path.
		for _, prefix := range set.prefixes {
			if t == prefix || strings.HasPrefix(t, prefix+"/") {
				return true
			}
		}
	}
	return false
}

func containsProjectScope(n Node) bool {
	switch v := n.(type) {
	case AndExpr:
		for _, child := range v.Exprs {
			if containsProjectScope(child) {
				return true
			}
		}
	case OrExpr:
		for _, child := range v.Exprs {
			if containsProjectScope(child) {
				return true
			}
		}
	case NotExpr:
		return containsProjectScope(v.Expr)
	case FilterExpr:
		return strings.EqualFold(v.Field, "project")
	}
	return false
}

// isLiveSourceKind reports kinds the live source legs produce rather than the store.
func isLiveSourceKind(kind string) bool {
	return kind == HitKindCode || kind == HitKindFile || kind == HitKindSymbol
}

func containsCodeKind(n Node) bool {
	switch v := n.(type) {
	case AndExpr:
		for _, child := range v.Exprs {
			if containsCodeKind(child) {
				return true
			}
		}
	case OrExpr:
		for _, child := range v.Exprs {
			if containsCodeKind(child) {
				return true
			}
		}
	case NotExpr:
		return containsCodeKind(v.Expr)
	case FilterExpr:
		field := strings.ToLower(strings.TrimSpace(v.Field))
		value := strings.ToLower(strings.TrimSpace(v.Value))
		return field == "kind" && isLiveSourceKind(value)
	}
	return false
}

func (st *compileState) codePathRoots() ([]CodeRoot, error) {
	scoped := st.scope == ScopeCurrent || st.scope == ScopeSlug
	if scoped {
		if st.ctx.RootsForProject == nil {
			return nil, fmt.Errorf("project roots resolver unavailable")
		}
		paths, err := st.ctx.RootsForProject(st.scopeProjectID)
		if err != nil {
			return nil, err
		}
		return codeRoots(st.scopeProjectID, paths), nil
	}
	if st.ctx.AttachedProjectIDs == nil || st.ctx.RootsForProject == nil {
		return nil, nil
	}
	ids, err := st.ctx.AttachedProjectIDs()
	if err != nil {
		return nil, err
	}
	var roots []CodeRoot
	for _, id := range ids {
		paths, err := st.ctx.RootsForProject(id)
		if err != nil {
			return nil, err
		}
		roots = append(roots, codeRoots(id, paths)...)
	}
	return roots, nil
}

func codeRoots(projectID string, attached []CodeRoot) []CodeRoot {
	roots := make([]CodeRoot, 0, len(attached))
	for _, root := range attached {
		root.ProjectID = projectID
		roots = append(roots, root)
	}
	return roots
}
