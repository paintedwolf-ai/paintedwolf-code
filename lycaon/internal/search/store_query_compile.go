package search

import (
	"fmt"
	"strings"
)

type storeQueryBuilder struct {
	filters     []compiledStoreFilter
	filterIndex int
	ctes        []string
	cteArgs     []any
	probes      []string
	probeArgs   []any
	whereArgs   []any
	textTables  []string
	flags       MatchFlags
	// postFilter enables per-row regex, case, and whole-word checks.
	postFilter bool
}

func (st *compileState) buildStoreLeg() (*StorePlanLeg, error) {
	flags := st.ctx.Flags
	paths, err := compilePathGlobs(flags)
	if err != nil {
		return nil, err
	}
	builder := &storeQueryBuilder{
		filters:    st.storeFilters,
		flags:      flags,
		postFilter: st.hasText && (st.rowPassText || flags.Regex || flags.CaseSensitive || flags.WholeWord),
	}
	expression, postExpr, err := builder.compileNode(st.ast, false)
	if err != nil {
		return nil, err
	}
	if builder.filterIndex != len(builder.filters) {
		return nil, fmt.Errorf("store filter compilation drift: used %d of %d", builder.filterIndex, len(builder.filters))
	}

	where := st.storeWhere(builder, expression)
	sqlText := builder.selectSQL()
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	args := append(append(append([]any{}, builder.cteArgs...), builder.probeArgs...), builder.whereArgs...)
	if origin := strings.TrimSpace(st.ctx.OriginProjectID); origin != "" {
		st.originRankArgIdx = len(args)
		args = append(args, origin)
		sqlText += " ORDER BY (e.project_id = ?) DESC"
		if len(builder.textTables) > 0 {
			sqlText += ", score DESC"
		}
		sqlText += ", e.ts DESC"
	} else if len(builder.textTables) > 0 {
		st.originRankArgIdx = -1
		sqlText += " ORDER BY score DESC, e.ts DESC"
	} else {
		st.originRankArgIdx = -1
		sqlText += " ORDER BY e.ts DESC"
	}

	leg := &StorePlanLeg{
		SQL:         sqlText,
		Args:        args,
		Cap:         st.ctx.Budget.storeCap(),
		PostScanCap: st.ctx.Budget.storePostScanCap(),
	}
	if builder.postFilter || len(flags.Include) > 0 || len(flags.Exclude) > 0 {
		leg.Post = &StorePostFilter{
			expr:      postExpr,
			numProbes: len(builder.probes),
			paths:     paths,
		}
	}
	return leg, nil
}

func (st *compileState) storeWhere(builder *storeQueryBuilder, expression string) []string {
	where := make([]string, 0, 4)
	if expression != "1" {
		where = append(where, expression)
	}
	if !st.includeFindings {
		where = append(where, "e.hit_kind != ?")
		builder.whereArgs = append(builder.whereArgs, HitKindFinding)
	}
	if !st.includeDrafts {
		where = append(where, "COALESCE(e.role, '') != ?")
		builder.whereArgs = append(builder.whereArgs, "draft")
	}
	if !st.ctx.IncludeTombstoned {
		where = append(where, "e.tombstoned = 0")
	}
	if project := strings.TrimSpace(st.ctx.ProjectScope); project != "" {
		where = append(where, "e.project_id = ?")
		builder.whereArgs = append(builder.whereArgs, project)
	}
	if sessions := st.ctx.SessionScope; len(sessions) > 0 {
		where = append(where, "e.session_id IN ("+bindPlaceholders(len(sessions))+")")
		for _, id := range sessions {
			builder.whereArgs = append(builder.whereArgs, id)
		}
	}
	if root := strings.TrimSpace(st.ctx.RootSessionScope); root != "" {
		where = append(where, "e.root_session_id = ?")
		builder.whereArgs = append(builder.whereArgs, root)
	}
	if st.scope != ScopeGlobal {
		where = append(where, "e.project_id = ?")
		builder.whereArgs = append(builder.whereArgs, st.scopeProjectID)
		st.scopeProjectArgIdx = len(builder.cteArgs) + len(builder.probeArgs) + len(builder.whereArgs) - 1
	} else {
		st.scopeProjectArgIdx = -1
	}
	return where
}

// compileNode emits a node's SQL predicate and, under match flags, its
// per-row expression. negated is the polarity under the enclosing NOTs.
func (b *storeQueryBuilder) compileNode(n Node, negated bool) (string, storePostExpr, error) {
	switch v := n.(type) {
	case AndExpr:
		if len(v.Exprs) == 0 {
			return "", nil, fmt.Errorf("invalid empty AND expression")
		}
		parts, posts, err := b.compileChildren(v.Exprs, negated)
		if err != nil {
			return "", nil, err
		}
		return "(" + strings.Join(parts, " AND ") + ")", b.post(postAndExpr{children: posts}), nil
	case OrExpr:
		if len(v.Exprs) == 0 {
			return "", nil, fmt.Errorf("invalid empty OR expression")
		}
		parts, posts, err := b.compileChildren(v.Exprs, negated)
		if err != nil {
			return "", nil, err
		}
		if len(parts) == 0 {
			return "0", b.post(postOrExpr{}), nil
		}
		return "(" + strings.Join(parts, " OR ") + ")", b.post(postOrExpr{children: posts}), nil
	case NotExpr:
		part, post, err := b.compileNode(v.Expr, !negated)
		if err != nil {
			return "", nil, err
		}
		if b.postFilter {
			post = postNotExpr{child: post}
		}
		return "NOT (" + part + ")", post, nil
	case FilterExpr:
		return b.compileFilterNode(v)
	case TextExpr:
		return b.compileTextNode(v, negated)
	default:
		return "", nil, fmt.Errorf("invalid store query expression %T", n)
	}
}

func (b *storeQueryBuilder) compileFilterNode(v FilterExpr) (string, storePostExpr, error) {
	if strings.EqualFold(strings.TrimSpace(v.Field), "project") {
		return "1", b.post(postTrueExpr{}), nil
	}
	filter, err := b.nextFilter()
	if err != nil {
		return "", nil, err
	}
	b.whereArgs = append(b.whereArgs, filter.args...)
	var post storePostExpr
	if b.postFilter {
		// SELECT probes preserve SQL filter results for post-filtering.
		post = postProbeExpr{index: len(b.probes)}
		b.probes = append(b.probes, filter.predicate)
		b.probeArgs = append(b.probeArgs, filter.args...)
	}
	return "(" + filter.predicate + ")", post, nil
}

func (b *storeQueryBuilder) compileTextNode(v TextExpr, negated bool) (string, storePostExpr, error) {
	if !b.postFilter {
		return b.addTextTable(buildFTSMatch(v.Text, v.Phrase)), nil, nil
	}
	matcher, err := compileSearchTermMatcher(v.Text, v.Phrase, b.flags)
	if err != nil {
		return "", nil, ensureMatchError(err)
	}
	if negated {
		// FTS folds case, so an SQL exclusion would drop rows the flags keep;
		// false under the enclosing NOT admits every row for the row matcher.
		return "0", postTextExpr{matcher: matcher}, nil
	}
	if b.flags.Regex || !ftsIndexable(v.Text) {
		// Regex and index-less terms evaluate scoped rows directly.
		return "1", postTextExpr{matcher: matcher}, nil
	}
	// FTS supplies candidates for case and whole-word checks.
	return b.addTextTable(buildFTSMatch(v.Text, v.Phrase)), postTextExpr{matcher: matcher}, nil
}

// post omits the evaluation tree for flagless plans.
func (b *storeQueryBuilder) post(expr storePostExpr) storePostExpr {
	if !b.postFilter {
		return nil
	}
	return expr
}

func (b *storeQueryBuilder) compileChildren(children []Node, negated bool) ([]string, []storePostExpr, error) {
	parts := make([]string, 0, len(children))
	posts := make([]storePostExpr, 0, len(children))
	for _, child := range children {
		part, post, err := b.compileNode(child, negated)
		if err != nil {
			return nil, nil, err
		}
		parts = append(parts, part)
		posts = append(posts, post)
	}
	return parts, posts, nil
}

func (b *storeQueryBuilder) nextFilter() (compiledStoreFilter, error) {
	if b.filterIndex >= len(b.filters) {
		return compiledStoreFilter{}, fmt.Errorf("store filter compilation drift at index %d", b.filterIndex)
	}
	filter := b.filters[b.filterIndex]
	b.filterIndex++
	return filter, nil
}

func (b *storeQueryBuilder) addTextTable(match string) string {
	name := fmt.Sprintf("text_match_%d", len(b.textTables))
	b.textTables = append(b.textTables, name)
	b.ctes = append(b.ctes, name+` AS (
		SELECT rowid AS eid, -bm25(evidence_fts) AS score
		FROM evidence_fts WHERE evidence_fts MATCH ?
		UNION ALL
		SELECT e2.rowid AS eid, -bm25(messages_fts) AS score
		FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		JOIN evidence_index e2 ON e2.message_id = m.id
		WHERE messages_fts MATCH ?
	)`)
	b.cteArgs = append(b.cteArgs, match, match)
	return "EXISTS (SELECT 1 FROM " + name + " WHERE " + name + ".eid = e.rowid)"
}

func (b *storeQueryBuilder) probeCols() string {
	var sb strings.Builder
	for i, pred := range b.probes {
		fmt.Fprintf(&sb, ", (%s) AS probe_%d", pred, i)
	}
	return sb.String()
}

func (b *storeQueryBuilder) selectSQL() string {
	messageContent := "'' AS message_content"
	messageJoin := ""
	if b.postFilter {
		messageContent = "COALESCE(search_message.content, '') AS message_content"
		messageJoin = "\n\tLEFT JOIN messages search_message ON search_message.id = e.message_id"
	}
	selectCols := storeSelectCols + ", " + messageContent
	if len(b.textTables) == 0 {
		return `SELECT ` + selectCols + `, 0.0 AS score` + b.probeCols() + `
			FROM evidence_index e` + storeSessionJoin + messageJoin
	}
	unionParts := make([]string, 0, len(b.textTables))
	for _, name := range b.textTables {
		unionParts = append(unionParts, "SELECT eid, score FROM "+name)
	}
	scores := `search_scores AS (
		SELECT eid, MAX(score) AS score FROM (
			` + strings.Join(unionParts, "\n\t\t\tUNION ALL\n\t\t\t") + `
		) GROUP BY eid
	)`
	ctes := append(append([]string{}, b.ctes...), scores)
	return `WITH ` + strings.Join(ctes, ",\n") + `
	SELECT ` + selectCols + `, COALESCE(s.score, 0.0) AS score` + b.probeCols() + `
	FROM evidence_index e` + storeSessionJoin + messageJoin + `
	LEFT JOIN search_scores s ON s.eid = e.rowid`
}

// bindPlaceholders renders n bound value slots.
func bindPlaceholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
