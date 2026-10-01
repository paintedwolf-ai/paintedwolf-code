package gotreesitter

import "bytes"

type csharpQueryClauseKind uint8

const (
	csharpQueryFromClause csharpQueryClauseKind = iota
	csharpQueryWhereClause
	csharpQueryOrderByClause
	csharpQueryLetClause
	csharpQueryJoinClause
	csharpQueryGroupClause
	csharpQuerySelectClause
)

type csharpQueryClauseSpec struct {
	kind             csharpQueryClauseKind
	start            uint32
	end              uint32
	keyword          [2]uint32
	name             [2]uint32
	sep1             [2]uint32
	sep2             [2]uint32
	sep3             [2]uint32
	value1           [2]uint32
	value2           [2]uint32
	value3           [2]uint32
	extra            [2]uint32
	trailingComments [][2]uint32
}

type csharpQueryAssignmentSpec struct {
	queryStart uint32
	queryEnd   uint32
	semiPos    uint32
	clauses    []csharpQueryClauseSpec
}

func csharpParseQueryExpressionSpec(source []byte, spec csharpQueryAssignmentSpec) (csharpQueryAssignmentSpec, bool) {
	pos := spec.queryStart
	for pos < spec.queryEnd {
		kw, kwPos, ok := csharpFindNextQueryKeyword(source, pos)
		if !ok || kwPos != pos {
			return spec, false
		}
		var clause csharpQueryClauseSpec
		var next uint32
		switch kw {
		case "from":
			clause, next, ok = csharpParseFromQueryClause(source, pos, spec.queryEnd)
		case "where":
			clause, next, ok = csharpParseWhereQueryClause(source, pos, spec.queryEnd)
		case "orderby":
			clause, next, ok = csharpParseOrderByQueryClause(source, pos, spec.queryEnd)
		case "let":
			clause, next, ok = csharpParseLetQueryClause(source, pos, spec.queryEnd)
		case "join":
			clause, next, ok = csharpParseJoinQueryClause(source, pos, spec.queryEnd)
		case "group":
			clause, next, ok = csharpParseGroupQueryClause(source, pos, spec.queryEnd)
		case "select":
			clause, next, ok = csharpParseSelectQueryClause(source, pos, spec.queryEnd)
		default:
			return spec, false
		}
		if !ok {
			return spec, false
		}
		spec.clauses = append(spec.clauses, clause)
		pos = csharpSkipSpaceBytes(source, next)
		if clause.kind == csharpQuerySelectClause {
			if pos != spec.queryEnd {
				return spec, false
			}
			break
		}
	}
	return spec, len(spec.clauses) >= 2
}

func csharpParseFromQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQueryFromClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 4}
	var ok bool
	if clause.name[0], clause.name[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, clause.keyword[1])); !ok {
		return clause, 0, false
	}
	clause.sep1[0] = csharpSkipSpaceBytes(source, clause.name[1])
	clause.sep1[1] = clause.sep1[0] + 2
	if !csharpHasKeywordAt(source, clause.sep1[0], "in") {
		return clause, 0, false
	}
	exprStart := csharpSkipSpaceBytes(source, clause.sep1[1])
	nextKeyword, nextPos, ok := csharpFindNextQueryKeyword(source, exprStart)
	if !ok || nextPos > queryEnd || nextKeyword == "into" {
		nextPos = queryEnd
	}
	valueEnd, comments := csharpTrimQueryValueAndTrailingComments(source, exprStart, nextPos)
	clause.value1 = [2]uint32{exprStart, valueEnd}
	clause.trailingComments = comments
	clause.end = clause.value1[1]
	return clause, nextPos, clause.value1[0] < clause.value1[1]
}

func csharpParseWhereQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQueryWhereClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 5}
	exprStart := csharpSkipSpaceBytes(source, clause.keyword[1])
	nextKeyword, nextPos, ok := csharpFindNextQueryKeyword(source, exprStart)
	if !ok || nextPos > queryEnd || nextKeyword == "into" {
		nextPos = queryEnd
	}
	valueEnd, comments := csharpTrimQueryValueAndTrailingComments(source, exprStart, nextPos)
	clause.value1 = [2]uint32{exprStart, valueEnd}
	clause.trailingComments = comments
	clause.end = clause.value1[1]
	return clause, nextPos, clause.value1[0] < clause.value1[1]
}

func csharpParseOrderByQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQueryOrderByClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 7}
	exprStart := csharpSkipSpaceBytes(source, clause.keyword[1])
	nextKeyword, nextPos, ok := csharpFindNextQueryKeyword(source, exprStart)
	if !ok || nextPos > queryEnd || nextKeyword == "into" {
		nextPos = queryEnd
	}
	clauseEnd, comments := csharpTrimQueryValueAndTrailingComments(source, exprStart, nextPos)
	if dirStart, dirEnd, ok := csharpFindTrailingDirection(source, exprStart, clauseEnd); ok {
		clause.extra = [2]uint32{dirStart, dirEnd}
		clauseEnd = csharpTrimRightSpaceBytes(source, dirStart)
	}
	clause.value1 = [2]uint32{exprStart, clauseEnd}
	clause.trailingComments = comments
	clause.end = clause.extra[1]
	if clause.end == 0 {
		clause.end = clause.value1[1]
	}
	return clause, nextPos, clause.value1[0] < clause.value1[1]
}

func csharpParseLetQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQueryLetClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 3}
	var ok bool
	if clause.name[0], clause.name[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, clause.keyword[1])); !ok {
		return clause, 0, false
	}
	sep := csharpSkipSpaceBytes(source, clause.name[1])
	if sep >= uint32(len(source)) || source[sep] != '=' {
		return clause, 0, false
	}
	clause.sep1 = [2]uint32{sep, sep + 1}
	exprStart := csharpSkipSpaceBytes(source, clause.sep1[1])
	nextKeyword, nextPos, ok := csharpFindNextQueryKeyword(source, exprStart)
	if !ok || nextPos > queryEnd || nextKeyword == "into" {
		nextPos = queryEnd
	}
	valueEnd, comments := csharpTrimQueryValueAndTrailingComments(source, exprStart, nextPos)
	clause.value1 = [2]uint32{exprStart, valueEnd}
	clause.trailingComments = comments
	clause.end = clause.value1[1]
	return clause, nextPos, clause.value1[0] < clause.value1[1]
}

func csharpParseJoinQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQueryJoinClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 4}
	var ok bool
	if clause.name[0], clause.name[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, clause.keyword[1])); !ok {
		return clause, 0, false
	}
	clause.sep1[0] = csharpSkipSpaceBytes(source, clause.name[1])
	clause.sep1[1] = clause.sep1[0] + 2
	if !csharpHasKeywordAt(source, clause.sep1[0], "in") {
		return clause, 0, false
	}
	sourceStart := csharpSkipSpaceBytes(source, clause.sep1[1])
	onPos, ok := csharpFindKeywordAfter(source, sourceStart, queryEnd, "on")
	if !ok {
		return clause, 0, false
	}
	clause.value1 = [2]uint32{sourceStart, csharpTrimRightSpaceBytes(source, onPos)}
	clause.sep2 = [2]uint32{onPos, onPos + 2}
	leftStart := csharpSkipSpaceBytes(source, clause.sep2[1])
	equalsPos, ok := csharpFindKeywordAfter(source, leftStart, queryEnd, "equals")
	if !ok {
		return clause, 0, false
	}
	clause.value2 = [2]uint32{leftStart, csharpTrimRightSpaceBytes(source, equalsPos)}
	clause.sep3 = [2]uint32{equalsPos, equalsPos + 6}
	rightStart := csharpSkipSpaceBytes(source, clause.sep3[1])
	nextKeyword, nextPos, ok := csharpFindNextQueryKeyword(source, rightStart)
	if !ok || nextPos > queryEnd || nextKeyword == "into" {
		nextPos = queryEnd
	}
	valueEnd, comments := csharpTrimQueryValueAndTrailingComments(source, rightStart, nextPos)
	clause.value3 = [2]uint32{rightStart, valueEnd}
	clause.trailingComments = comments
	clause.end = clause.value3[1]
	return clause, nextPos, clause.value1[0] < clause.value1[1] && clause.value2[0] < clause.value2[1] && clause.value3[0] < clause.value3[1]
}

func csharpParseGroupQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQueryGroupClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 5}
	groupExprStart := csharpSkipSpaceBytes(source, clause.keyword[1])
	byPos, ok := csharpFindKeywordAfter(source, groupExprStart, queryEnd, "by")
	if !ok {
		return clause, 0, false
	}
	clause.value1 = [2]uint32{groupExprStart, csharpTrimRightSpaceBytes(source, byPos)}
	clause.sep1 = [2]uint32{byPos, byPos + 2}
	keyExprStart := csharpSkipSpaceBytes(source, clause.sep1[1])
	nextKeyword, nextPos, ok := csharpFindNextQueryKeyword(source, keyExprStart)
	if !ok || nextPos > queryEnd {
		nextPos = queryEnd
	}
	if nextKeyword == "into" {
		clause.value2 = [2]uint32{keyExprStart, csharpTrimRightSpaceBytes(source, nextPos)}
		clause.sep2 = [2]uint32{nextPos, nextPos + 4}
		var okIdent bool
		if clause.name[0], clause.name[1], okIdent = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, clause.sep2[1])); !okIdent {
			return clause, 0, false
		}
		clause.end = clause.name[1]
		nextKeyword, nextPos, ok = csharpFindNextQueryKeyword(source, csharpSkipSpaceBytes(source, clause.end))
		if !ok || nextPos > queryEnd || nextKeyword == "into" {
			return clause, 0, false
		}
		_, comments := csharpTrimQueryValueAndTrailingComments(source, clause.end, nextPos)
		clause.trailingComments = comments
		return clause, nextPos, clause.value1[0] < clause.value1[1] && clause.value2[0] < clause.value2[1]
	}
	valueEnd, comments := csharpTrimQueryValueAndTrailingComments(source, keyExprStart, nextPos)
	clause.value2 = [2]uint32{keyExprStart, valueEnd}
	clause.trailingComments = comments
	clause.end = clause.value2[1]
	return clause, nextPos, clause.value1[0] < clause.value1[1] && clause.value2[0] < clause.value2[1]
}

func csharpParseSelectQueryClause(source []byte, start, queryEnd uint32) (csharpQueryClauseSpec, uint32, bool) {
	var clause csharpQueryClauseSpec
	clause.kind = csharpQuerySelectClause
	clause.start = start
	clause.keyword = [2]uint32{start, start + 6}
	exprStart := csharpSkipSpaceBytes(source, clause.keyword[1])
	valueEnd, comments := csharpTrimQueryValueAndTrailingComments(source, exprStart, queryEnd)
	clause.value1 = [2]uint32{exprStart, valueEnd}
	clause.trailingComments = comments
	clause.end = clause.value1[1]
	return clause, queryEnd, clause.value1[0] < clause.value1[1]
}

func csharpFindNextQueryKeyword(source []byte, start uint32) (string, uint32, bool) {
	keywords := []string{"from", "where", "orderby", "let", "join", "group", "into", "select"}
	for i := start; i < uint32(len(source)); i++ {
		for _, kw := range keywords {
			if csharpHasKeywordBoundaryAt(source, i, kw) {
				return kw, i, true
			}
		}
	}
	return "", 0, false
}

func csharpTrimQueryValueAndTrailingComments(source []byte, start, end uint32) (uint32, [][2]uint32) {
	if end > uint32(len(source)) {
		end = uint32(len(source))
	}
	if start >= end {
		return start, nil
	}
	commentStart, ok := csharpFindTopLevelCommentStart(source, start, end)
	if !ok {
		return csharpTrimRightSpaceBytes(source, end), nil
	}
	valueEnd := csharpTrimRightSpaceBytes(source, commentStart)
	var comments [][2]uint32
	cursor := commentStart
	for cursor < end {
		cursor = csharpSkipSpaceBytes(source, cursor)
		if cursor+1 >= end || source[cursor] != '/' {
			break
		}
		switch source[cursor+1] {
		case '/':
			commentEnd := cursor + 2
			for commentEnd < end && source[commentEnd] != '\n' {
				commentEnd++
			}
			comments = append(comments, [2]uint32{cursor, commentEnd})
			cursor = commentEnd
		case '*':
			commentEnd := csharpFindBlockCommentEnd(source, cursor+2, end)
			if commentEnd <= cursor+1 {
				return valueEnd, comments
			}
			comments = append(comments, [2]uint32{cursor, commentEnd})
			cursor = commentEnd
		default:
			return valueEnd, comments
		}
	}
	return valueEnd, comments
}

func csharpFindTopLevelCommentStart(source []byte, start, end uint32) (uint32, bool) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	inString := false
	inChar := false
	verbatimString := false
	escape := false
	for i := start; i+1 < end; i++ {
		b := source[i]
		if inString {
			if verbatimString {
				if b == '"' {
					if i+1 < end && source[i+1] == '"' {
						i++
						continue
					}
					inString = false
					verbatimString = false
				}
				continue
			}
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		if inChar {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '\'' {
				inChar = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
			verbatimString = i > 0 && source[i-1] == '@'
			escape = false
		case '\'':
			inChar = true
			escape = false
		case '(':
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '/':
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && (source[i+1] == '/' || source[i+1] == '*') {
				return i, true
			}
		}
	}
	return 0, false
}

func csharpFindKeywordAfter(source []byte, start, limit uint32, kw string) (uint32, bool) {
	if limit > uint32(len(source)) {
		limit = uint32(len(source))
	}
	for i := start; i < limit; i++ {
		if csharpHasKeywordBoundaryAt(source, i, kw) {
			return i, true
		}
	}
	return 0, false
}

func csharpHasKeywordBoundaryAt(source []byte, start uint32, kw string) bool {
	if !csharpHasKeywordAt(source, start, kw) {
		return false
	}
	if start > 0 && csharpIdentifierContinueByte(source[start-1]) {
		return false
	}
	end := start + uint32(len(kw))
	if end < uint32(len(source)) && csharpIdentifierContinueByte(source[end]) {
		return false
	}
	return true
}

func csharpFindTrailingDirection(source []byte, start, end uint32) (uint32, uint32, bool) {
	end = csharpTrimRightSpaceBytes(source, end)
	for _, kw := range []string{"ascending", "descending"} {
		if end < uint32(len(kw)) {
			continue
		}
		dirStart := end - uint32(len(kw))
		if dirStart < start {
			continue
		}
		if csharpHasKeywordBoundaryAt(source, dirStart, kw) {
			return dirStart, end, true
		}
	}
	return 0, 0, false
}

func csharpBuildRecoveredQueryExpressionWithoutParser(arena *nodeArena, source []byte, lang *Language, spec csharpQueryAssignmentSpec) (*Node, bool) {
	if arena == nil || lang == nil || len(spec.clauses) == 0 {
		return nil, false
	}
	return csharpBuildRecoveredQueryExpressionWithExpr(arena, source, lang, spec, func(span [2]uint32) (*Node, bool) {
		return csharpRecoverQueryExpressionNodeFromRange(source, span[0], span[1], lang, arena)
	})
}

func csharpBuildRecoveredQueryExpressionWithExpr(arena *nodeArena, source []byte, lang *Language, spec csharpQueryAssignmentSpec, expr func([2]uint32) (*Node, bool)) (*Node, bool) {
	if arena == nil || lang == nil || expr == nil || len(spec.clauses) == 0 {
		return nil, false
	}
	queryExprSym, ok := symbolByName(lang, "query_expression")
	if !ok {
		return nil, false
	}
	queryExprNamed := symbolIsNamed(lang, queryExprSym)
	children := make([]*Node, 0, len(spec.clauses)+2)
	for _, clause := range spec.clauses {
		node, extra, ok := csharpBuildRecoveredQueryClause(arena, source, lang, clause, expr)
		if !ok {
			return nil, false
		}
		children = append(children, node)
		if len(extra) > 0 {
			children = append(children, extra...)
		}
	}
	return newParentNodeInArena(arena, queryExprSym, queryExprNamed, children, nil, 0), true
}

func csharpBuildRecoveredQueryClause(arena *nodeArena, source []byte, lang *Language, clause csharpQueryClauseSpec, expr func([2]uint32) (*Node, bool)) (*Node, []*Node, bool) {
	builder, ok := newCSharpRecoveredQueryClauseBuilder(arena, source, lang, expr)
	if !ok {
		return nil, nil, false
	}
	switch clause.kind {
	case csharpQueryFromClause:
		return builder.fromClause(clause)
	case csharpQueryWhereClause:
		return builder.whereClause(clause)
	case csharpQueryOrderByClause:
		return builder.orderByClause(clause)
	case csharpQueryLetClause:
		return builder.letClause(clause)
	case csharpQueryJoinClause:
		return builder.joinClause(clause)
	case csharpQueryGroupClause:
		return builder.groupClause(clause)
	case csharpQuerySelectClause:
		return builder.selectClause(clause)
	default:
		return nil, nil, false
	}
}

type csharpRecoveredQueryClauseBuilder struct {
	arena           *nodeArena
	source          []byte
	lang            *Language
	expr            func([2]uint32) (*Node, bool)
	identifierSym   Symbol
	identifierNamed bool
}

func newCSharpRecoveredQueryClauseBuilder(arena *nodeArena, source []byte, lang *Language, expr func([2]uint32) (*Node, bool)) (csharpRecoveredQueryClauseBuilder, bool) {
	var builder csharpRecoveredQueryClauseBuilder
	if arena == nil || lang == nil || expr == nil {
		return builder, false
	}
	identifierSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return builder, false
	}
	return csharpRecoveredQueryClauseBuilder{
		arena:           arena,
		source:          source,
		lang:            lang,
		expr:            expr,
		identifierSym:   identifierSym,
		identifierNamed: symbolIsNamed(lang, identifierSym),
	}, true
}

func (b csharpRecoveredQueryClauseBuilder) parentSymbol(name string) (Symbol, bool, bool) {
	sym, ok := symbolByName(b.lang, name)
	if !ok {
		return 0, false, false
	}
	return sym, symbolIsNamed(b.lang, sym), true
}

func (b csharpRecoveredQueryClauseBuilder) leafByName(name string, span [2]uint32) (*Node, bool) {
	sym, ok := symbolByName(b.lang, name)
	if !ok || span[0] >= span[1] {
		return nil, false
	}
	named := symbolIsNamed(b.lang, sym)
	return newLeafNodeInArena(b.arena, sym, named, span[0], span[1], advancePointByBytes(Point{}, b.source[:span[0]]), advancePointByBytes(Point{}, b.source[:span[1]])), true
}

func (b csharpRecoveredQueryClauseBuilder) ident(span [2]uint32) *Node {
	return newLeafNodeInArena(b.arena, b.identifierSym, b.identifierNamed, span[0], span[1], advancePointByBytes(Point{}, b.source[:span[0]]), advancePointByBytes(Point{}, b.source[:span[1]]))
}

func (b csharpRecoveredQueryClauseBuilder) trailingComments(clause csharpQueryClauseSpec, extra []*Node) []*Node {
	for _, span := range clause.trailingComments {
		comment, ok := csharpRecoverTopLevelCommentNodeFromRange(b.source, span[0], span[1], b.lang, b.arena)
		if ok {
			extra = append(extra, comment)
		}
	}
	return extra
}

func (b csharpRecoveredQueryClauseBuilder) fromClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	fromClauseSym, fromClauseNamed, ok := b.parentSymbol("from_clause")
	if !ok {
		return nil, nil, false
	}
	nameFieldID, ok := b.lang.FieldByName("name")
	if !ok {
		return nil, nil, false
	}
	fromTok, ok := b.leafByName("from", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	inTok, ok := b.leafByName("in", clause.sep1)
	if !ok {
		return nil, nil, false
	}
	sourceNode, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	children := []*Node{fromTok, b.ident(clause.name), inTok, sourceNode}
	fields := cloneFieldIDSliceInArena(b.arena, []FieldID{0, nameFieldID, 0, 0})
	return newParentNodeInArena(b.arena, fromClauseSym, fromClauseNamed, children, fields, 0), b.trailingComments(clause, nil), true
}

func (b csharpRecoveredQueryClauseBuilder) whereClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	whereClauseSym, whereClauseNamed, ok := b.parentSymbol("where_clause")
	if !ok {
		return nil, nil, false
	}
	whereTok, ok := b.leafByName("where", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	value, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	return newParentNodeInArena(b.arena, whereClauseSym, whereClauseNamed, []*Node{whereTok, value}, nil, 0), b.trailingComments(clause, nil), true
}

func (b csharpRecoveredQueryClauseBuilder) orderByClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	orderByClauseSym, orderByClauseNamed, ok := b.parentSymbol("order_by_clause")
	if !ok {
		return nil, nil, false
	}
	orderTok, ok := b.leafByName("orderby", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	value, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	children := []*Node{orderTok, value}
	if clause.extra[0] < clause.extra[1] {
		dirName := string(b.source[clause.extra[0]:clause.extra[1]])
		dirTok, ok := b.leafByName(dirName, clause.extra)
		if !ok {
			return nil, nil, false
		}
		children = append(children, dirTok)
	}
	return newParentNodeInArena(b.arena, orderByClauseSym, orderByClauseNamed, children, nil, 0), b.trailingComments(clause, nil), true
}

func (b csharpRecoveredQueryClauseBuilder) letClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	letClauseSym, letClauseNamed, ok := b.parentSymbol("let_clause")
	if !ok {
		return nil, nil, false
	}
	letTok, ok := b.leafByName("let", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	eqTok, ok := b.leafByName("=", clause.sep1)
	if !ok {
		return nil, nil, false
	}
	value, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	children := []*Node{letTok, b.ident(clause.name), eqTok, value}
	return newParentNodeInArena(b.arena, letClauseSym, letClauseNamed, children, nil, 0), b.trailingComments(clause, nil), true
}

func (b csharpRecoveredQueryClauseBuilder) joinClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	joinClauseSym, joinClauseNamed, ok := b.parentSymbol("join_clause")
	if !ok {
		return nil, nil, false
	}
	joinTok, ok := b.leafByName("join", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	inTok, ok := b.leafByName("in", clause.sep1)
	if !ok {
		return nil, nil, false
	}
	onTok, ok := b.leafByName("on", clause.sep2)
	if !ok {
		return nil, nil, false
	}
	equalsTok, ok := b.leafByName("equals", clause.sep3)
	if !ok {
		return nil, nil, false
	}
	sourceNode, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	leftNode, ok := b.expr(clause.value2)
	if !ok {
		return nil, nil, false
	}
	rightNode, ok := b.expr(clause.value3)
	if !ok {
		return nil, nil, false
	}
	children := []*Node{joinTok, b.ident(clause.name), inTok, sourceNode, onTok, leftNode, equalsTok, rightNode}
	return newParentNodeInArena(b.arena, joinClauseSym, joinClauseNamed, children, nil, 0), b.trailingComments(clause, nil), true
}

func (b csharpRecoveredQueryClauseBuilder) groupClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	groupClauseSym, groupClauseNamed, ok := b.parentSymbol("group_clause")
	if !ok {
		return nil, nil, false
	}
	groupTok, ok := b.leafByName("group", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	byTok, ok := b.leafByName("by", clause.sep1)
	if !ok {
		return nil, nil, false
	}
	groupExpr, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	keyExpr, ok := b.expr(clause.value2)
	if !ok {
		return nil, nil, false
	}
	groupClause := newParentNodeInArena(b.arena, groupClauseSym, groupClauseNamed, []*Node{groupTok, groupExpr, byTok, keyExpr}, nil, 0)
	var extra []*Node
	if clause.sep2[0] < clause.sep2[1] && clause.name[0] < clause.name[1] {
		intoTok, ok := b.leafByName("into", clause.sep2)
		if !ok {
			return nil, nil, false
		}
		extra = []*Node{intoTok, b.ident(clause.name)}
	}
	return groupClause, b.trailingComments(clause, extra), true
}

func (b csharpRecoveredQueryClauseBuilder) selectClause(clause csharpQueryClauseSpec) (*Node, []*Node, bool) {
	selectClauseSym, selectClauseNamed, ok := b.parentSymbol("select_clause")
	if !ok {
		return nil, nil, false
	}
	selectTok, ok := b.leafByName("select", clause.keyword)
	if !ok {
		return nil, nil, false
	}
	value, ok := b.expr(clause.value1)
	if !ok {
		return nil, nil, false
	}
	return newParentNodeInArena(b.arena, selectClauseSym, selectClauseNamed, []*Node{selectTok, value}, nil, 0), b.trailingComments(clause, nil), true
}

func csharpRecoverExpressionNodeFromRange(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil {
		return nil, false
	}
	if node, ok := csharpRecoverSimpleQueryAtomNodeFromRange(source, start, end, p.language, arena); ok {
		return node, true
	}
	if _, ok := csharpFindTopLevelOperator(source, start, end, "=>"); ok {
		if node, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, end, p.language, arena); ok {
			return node, true
		}
	}
	if csharpHasKeywordAt(source, start, "from") || csharpHasKeywordAt(source, start, "ref") {
		if node, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, end, p.language, arena); ok {
			return node, true
		}
	}
	if node, ok := csharpRecoverExpressionNodeFromRangeWithWrapper(source, start, end, p, arena); ok {
		return node, true
	}
	return csharpRecoverQueryExpressionNodeFromRange(source, start, end, p.language, arena)
}

func csharpRecoverExpressionNodeFromRangeWithWrapper(source []byte, start, end uint32, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end {
		return nil, false
	}
	const prefix = "class __Q { void __M() { var __q = "
	const suffix = "; } }\n"
	wrapped := make([]byte, 0, len(prefix)+int(end-start)+len(suffix))
	wrapped = append(wrapped, prefix...)
	wrapped = append(wrapped, source[start:end]...)
	wrapped = append(wrapped, suffix...)
	tree, err := p.parseForRecovery(wrapped)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	defer tree.Release()
	node := csharpExtractRecoveredVariableInitializer(tree.RootNode(), p.language, arena)
	if node == nil {
		return nil, false
	}
	if !shiftNodeBytes(node, int64(start)-int64(len(prefix))) {
		return nil, false
	}
	recomputeNodePointsFromBytes(node, source)
	return node, node != nil
}

func csharpRecoverQueryExpressionNodeFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end {
		return nil, false
	}
	if node, handled, ok := csharpRecoverQueryKeywordOrShapeExpressionNode(source, start, end, lang, arena); handled {
		return node, ok
	}
	if node, handled, ok := csharpRecoverQueryOperatorExpressionNode(source, start, end, lang, arena); handled {
		return node, ok
	}
	return csharpRecoverQueryLeafExpressionNode(source, start, end, lang, arena)
}

func csharpRecoverQueryKeywordOrShapeExpressionNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool, bool) {
	if csharpHasKeywordAt(source, start, "from") {
		spec, ok := csharpParseQueryExpressionSpec(source, csharpQueryAssignmentSpec{
			queryStart: start,
			queryEnd:   end,
		})
		if ok {
			if node, ok := csharpBuildRecoveredQueryExpressionWithoutParser(arena, source, lang, spec); ok {
				return node, true, true
			}
		}
	}
	if csharpHasKeywordAt(source, start, "ref") {
		if node, ok := csharpBuildRefExpressionNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
	}
	if qPos, ok := csharpFindTopLevelOperator(source, start, end, "?"); ok {
		colonPos, ok := csharpFindConditionalColon(source, qPos+1, end)
		if !ok {
			return nil, true, false
		}
		condition, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, qPos, lang, arena)
		if !ok {
			return nil, true, false
		}
		consequence, ok := csharpRecoverQueryExpressionNodeFromRange(source, qPos+1, colonPos, lang, arena)
		if !ok {
			return nil, true, false
		}
		alternative, ok := csharpRecoverQueryExpressionNodeFromRange(source, colonPos+1, end, lang, arena)
		if !ok {
			return nil, true, false
		}
		node, ok := csharpBuildConditionalExpressionNode(arena, source, lang, condition, qPos, consequence, colonPos, alternative)
		return node, true, ok
	}
	if csharpHasKeywordAt(source, start, "new") {
		if node, ok := csharpBuildAnonymousObjectCreationNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
		if node, ok := csharpBuildObjectCreationExpressionNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
	}
	if source[start] == '(' && source[end-1] == ')' {
		if node, ok := csharpBuildTupleExpressionNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
	}
	if source[start] == '[' && source[end-1] == ']' {
		if node, ok := csharpBuildElementBindingExpressionNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
	}
	return nil, false, false
}

func csharpRecoverQueryOperatorExpressionNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool, bool) {
	if arrowPos, ok := csharpFindTopLevelOperator(source, start, end, "=>"); ok {
		node, ok := csharpBuildLambdaExpressionNode(arena, source, lang, start, arrowPos, end)
		return node, true, ok
	}
	if opPos, ok := csharpFindTopLevelOperator(source, start, end, "=="); ok {
		node, ok := csharpBuildBinaryExpressionNode(arena, source, lang, start, opPos, opPos+2, end)
		return node, true, ok
	}
	if isPos, ok := csharpFindTopLevelKeyword(source, start, end, "is"); ok {
		node, ok := csharpBuildIsPatternExpressionNode(arena, source, lang, start, isPos, isPos+2, end)
		return node, true, ok
	}
	if opPos, ok := csharpFindTopLevelOperator(source, start, end, "*"); ok {
		node, ok := csharpBuildBinaryExpressionNode(arena, source, lang, start, opPos, opPos+1, end)
		return node, true, ok
	}
	if opPos, ok := csharpFindTopLevelAssignment(source, start, end); ok {
		node, ok := csharpBuildAssignmentExpressionNode(arena, source, lang, start, opPos, end)
		return node, true, ok
	}
	if end > start && source[end-1] == ')' {
		if node, ok := csharpBuildInvocationExpressionNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
	}
	if dotPos, ok := csharpFindLastTopLevelOperator(source, start, end, "."); ok {
		node, ok := csharpBuildMemberAccessExpressionNode(arena, source, lang, start, dotPos, end)
		return node, true, ok
	}
	if end > start && source[end-1] == ']' {
		if node, ok := csharpBuildElementAccessExpressionNode(arena, source, lang, start, end); ok {
			return node, true, true
		}
	}
	return nil, false, false
}

func csharpRecoverQueryLeafExpressionNode(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if source[start] == '"' && source[end-1] == '"' && end-start >= 2 {
		return csharpBuildStringLiteralNode(arena, source, lang, start, end)
	}
	if bytes.Equal(source[start:end], []byte("null")) {
		return csharpBuildLeafNodeByName(arena, source, lang, "null_literal", start, end)
	}
	if csharpIsIntegerLiteral(source[start:end]) {
		return csharpBuildLeafNodeByName(arena, source, lang, "integer_literal", start, end)
	}
	if identStart, identEnd, ok := csharpScanIdentifierAt(source, start); ok && identStart == start && identEnd == end {
		return csharpBuildIdentifierNodeFromSource(source, start, end, lang, arena)
	}
	return nil, false
}

func csharpBuildTupleExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || start >= end || int(end) > len(source) || source[start] != '(' || source[end-1] != ')' {
		return nil, false
	}
	items := csharpSplitTopLevelByComma(source, start+1, end-1)
	if len(items) < 2 {
		return nil, false
	}
	sym, ok := symbolByName(lang, "tuple_expression")
	if !ok {
		return nil, false
	}
	argSym, ok := symbolByName(lang, "argument")
	if !ok {
		return nil, false
	}
	argNamed := symbolIsNamed(lang, argSym)
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "(", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ")", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart >= itemEnd {
			return nil, false
		}
		value, ok := csharpRecoverQueryExpressionNodeFromRange(source, itemStart, itemEnd, lang, arena)
		if !ok {
			return nil, false
		}
		children = append(children, newParentNodeInArena(arena, argSym, argNamed, []*Node{value}, nil, 0))
		if i < len(items)-1 {
			commaPos := csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && source[span[1]] != ',' {
				return nil, false
			}
			if commaPos == 0 {
				commaPos = span[1]
			}
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildElementBindingExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '[' || source[end-1] != ']' {
		return nil, false
	}
	items := csharpSplitTopLevelByComma(source, start+1, end-1)
	if len(items) == 0 {
		return nil, false
	}
	sym, ok := symbolByName(lang, "element_binding_expression")
	if !ok {
		return nil, false
	}
	argSym, ok := symbolByName(lang, "argument")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "[", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "]", end-1, end)
	if !ok {
		return nil, false
	}
	argNamed := symbolIsNamed(lang, argSym)
	children := []*Node{openTok}
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart < itemEnd {
			value, ok := csharpRecoverQueryExpressionNodeFromRange(source, itemStart, itemEnd, lang, arena)
			if !ok {
				return nil, false
			}
			children = append(children, newParentNodeInArena(arena, argSym, argNamed, []*Node{value}, nil, 0))
		}
		commaPos := uint32(0)
		if i < len(items)-1 {
			commaPos = csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && span[1] < uint32(len(source)) && source[span[1]] == ',' {
				commaPos = span[1]
			}
		} else if span[1] < end-1 && source[span[1]] == ',' {
			commaPos = span[1]
		}
		if commaPos != 0 {
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildRefExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || !csharpHasKeywordAt(source, start, "ref") {
		return nil, false
	}
	valueStart := csharpSkipSpaceBytes(source, start+3)
	if valueStart >= end {
		return nil, false
	}
	value, ok := csharpRecoverQueryExpressionNodeFromRange(source, valueStart, end, lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "ref_expression")
	if !ok {
		return nil, false
	}
	refTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "ref", start, start+3)
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{refTok, value}, nil, 0), true
}

func csharpBuildElementAccessExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[end-1] != ']' {
		return nil, false
	}
	openPos, ok := csharpFindElementAccessOpenBracket(source, start, end)
	if !ok || openPos <= start {
		return nil, false
	}
	expr, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, openPos, lang, arena)
	if !ok {
		return nil, false
	}
	args, ok := csharpBuildBracketedArgumentListNode(arena, source, lang, openPos, end)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "element_access_expression")
	if !ok {
		return nil, false
	}
	expressionID, _ := lang.FieldByName("expression")
	argumentID, _ := lang.FieldByName("argument")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{expressionID, argumentID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{expr, args}, fields, 0), true
}

func csharpFindElementAccessOpenBracket(source []byte, start, end uint32) (uint32, bool) {
	if start >= end || source[end-1] != ']' {
		return 0, false
	}
	depth := 0
	inString := false
	escape := false
	for i := end; i > start; i-- {
		b := source[i-1]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case ']':
			depth++
		case '[':
			depth--
			if depth == 0 {
				return i - 1, true
			}
		}
	}
	return 0, false
}

func csharpBuildBracketedArgumentListNode(arena *nodeArena, source []byte, lang *Language, openPos, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || openPos >= end || int(end) > len(source) || source[openPos] != '[' || source[end-1] != ']' {
		return nil, false
	}
	sym, ok := symbolByName(lang, "bracketed_argument_list")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "[", openPos, openPos+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "]", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	items := csharpSplitTopLevelByComma(source, openPos+1, end-1)
	argSym, ok := symbolByName(lang, "argument")
	if !ok {
		return nil, false
	}
	argNamed := symbolIsNamed(lang, argSym)
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart < itemEnd {
			value, ok := csharpRecoverQueryExpressionNodeFromRange(source, itemStart, itemEnd, lang, arena)
			if !ok {
				return nil, false
			}
			children = append(children, newParentNodeInArena(arena, argSym, argNamed, []*Node{value}, nil, 0))
		}
		if i < len(items)-1 {
			commaPos := csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && source[span[1]] != ',' {
				return nil, false
			}
			if commaPos == 0 {
				commaPos = span[1]
			}
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpRecoverSimpleQueryAtomNodeFromRange(source []byte, start, end uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end {
		return nil, false
	}
	if source[start] == '"' && source[end-1] == '"' && end-start >= 2 {
		return csharpBuildStringLiteralNode(arena, source, lang, start, end)
	}
	if csharpIsIntegerLiteral(source[start:end]) {
		return csharpBuildLeafNodeByName(arena, source, lang, "integer_literal", start, end)
	}
	if identStart, identEnd, ok := csharpScanIdentifierAt(source, start); ok && identStart == start && identEnd == end {
		return csharpBuildIdentifierNodeFromSource(source, start, end, lang, arena)
	}
	if dotPos, ok := csharpFindLastTopLevelOperator(source, start, end, "."); ok && csharpLooksLikeSimpleMemberAccessChain(source, start, end) {
		return csharpBuildMemberAccessExpressionNode(arena, source, lang, start, dotPos, end)
	}
	return nil, false
}

func csharpLooksLikeSimpleMemberAccessChain(source []byte, start, end uint32) bool {
	cursor := csharpSkipSpaceBytes(source, start)
	if cursor >= end {
		return false
	}
	for {
		identStart, identEnd, ok := csharpScanIdentifierAt(source, cursor)
		if !ok || identStart != cursor {
			return false
		}
		cursor = csharpSkipSpaceBytes(source, identEnd)
		if cursor >= end {
			return true
		}
		if source[cursor] != '.' {
			return false
		}
		cursor = csharpSkipSpaceBytes(source, cursor+1)
		if cursor >= end {
			return false
		}
	}
}

func csharpBuildConditionalExpressionNode(arena *nodeArena, source []byte, lang *Language, condition *Node, qPos uint32, consequence *Node, colonPos uint32, alternative *Node) (*Node, bool) {
	sym, ok := symbolByName(lang, "conditional_expression")
	if !ok {
		return nil, false
	}
	conditionID, _ := lang.FieldByName("condition")
	consequenceID, _ := lang.FieldByName("consequence")
	alternativeID, _ := lang.FieldByName("alternative")
	named := symbolIsNamed(lang, sym)
	children := []*Node{condition, consequence, alternative}
	fieldIDs := []FieldID{conditionID, consequenceID, alternativeID}
	if qTok, qOK := csharpBuildLeafNodeByName(arena, source, lang, "?", qPos, qPos+1); qOK {
		if colonTok, colonOK := csharpBuildLeafNodeByName(arena, source, lang, ":", colonPos, colonPos+1); colonOK {
			children = []*Node{condition, qTok, consequence, colonTok, alternative}
			fieldIDs = []FieldID{conditionID, 0, consequenceID, 0, alternativeID}
		}
	}
	fields := cloneFieldIDSliceInArena(arena, fieldIDs)
	return newParentNodeInArena(arena, sym, named, children, fields, 0), true
}

func csharpBuildBinaryExpressionNode(arena *nodeArena, source []byte, lang *Language, start, opStart, opEnd, end uint32) (*Node, bool) {
	left, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, opStart, lang, arena)
	if !ok {
		return nil, false
	}
	right, ok := csharpRecoverQueryExpressionNodeFromRange(source, opEnd, end, lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "binary_expression")
	if !ok {
		return nil, false
	}
	opTok, ok := csharpBuildLeafNodeByName(arena, source, lang, string(source[opStart:opEnd]), opStart, opEnd)
	if !ok {
		return nil, false
	}
	leftID, _ := lang.FieldByName("left")
	operatorID, _ := lang.FieldByName("operator")
	rightID, _ := lang.FieldByName("right")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{leftID, operatorID, rightID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{left, opTok, right}, fields, 0), true
}

func csharpBuildIsPatternExpressionNode(arena *nodeArena, source []byte, lang *Language, start, isStart, isEnd, end uint32) (*Node, bool) {
	left, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, isStart, lang, arena)
	if !ok {
		return nil, false
	}
	pattern, ok := csharpBuildConstantPatternNode(arena, source, lang, isEnd, end)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "is_pattern_expression")
	if !ok {
		return nil, false
	}
	isTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "is", isStart, isEnd)
	if !ok {
		return nil, false
	}
	expressionID, _ := lang.FieldByName("expression")
	patternID, _ := lang.FieldByName("pattern")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{expressionID, 0, patternID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{left, isTok, pattern}, fields, 0), true
}

func csharpBuildConstantPatternNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end {
		return nil, false
	}
	var value *Node
	var ok bool
	if csharpIsIntegerLiteral(source[start:end]) {
		value, ok = csharpBuildLeafNodeByName(arena, source, lang, "integer_literal", start, end)
	} else if identStart, identEnd, identOK := csharpScanIdentifierAt(source, start); identOK && identStart == start && identEnd == end {
		value, ok = csharpBuildIdentifierNodeFromSource(source, start, end, lang, arena)
	}
	if !ok || value == nil {
		return nil, false
	}
	sym, ok := symbolByName(lang, "constant_pattern")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{value}, nil, 0), true
}

func csharpBuildAssignmentExpressionNode(arena *nodeArena, source []byte, lang *Language, start, opPos, end uint32) (*Node, bool) {
	left, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, opPos, lang, arena)
	if !ok {
		return nil, false
	}
	right, ok := csharpRecoverQueryExpressionNodeFromRange(source, opPos+1, end, lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "assignment_expression")
	if !ok {
		return nil, false
	}
	eqTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "=", opPos, opPos+1)
	if !ok {
		return nil, false
	}
	leftID, _ := lang.FieldByName("left")
	operatorID, _ := lang.FieldByName("operator")
	rightID, _ := lang.FieldByName("right")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{leftID, operatorID, rightID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{left, eqTok, right}, fields, 0), true
}

func csharpBuildInvocationExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	openPos, ok := csharpFindInvocationOpenParen(source, start, end)
	if !ok {
		return nil, false
	}
	function, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, openPos, lang, arena)
	if !ok {
		return nil, false
	}
	args, ok := csharpBuildArgumentListNode(arena, source, lang, openPos, end)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "invocation_expression")
	if !ok {
		return nil, false
	}
	functionID, _ := lang.FieldByName("function")
	argumentsID, _ := lang.FieldByName("arguments")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{functionID, argumentsID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{function, args}, fields, 0), true
}

func csharpBuildArgumentListNode(arena *nodeArena, source []byte, lang *Language, openPos, end uint32) (*Node, bool) {
	sym, ok := symbolByName(lang, "argument_list")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "(", openPos, openPos+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ")", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	items := csharpSplitTopLevelByComma(source, openPos+1, end-1)
	argSym, ok := symbolByName(lang, "argument")
	if !ok {
		return nil, false
	}
	argNamed := symbolIsNamed(lang, argSym)
	commaSym, _ := symbolByName(lang, ",")
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart >= itemEnd {
			continue
		}
		value, ok := csharpRecoverQueryExpressionNodeFromRange(source, itemStart, itemEnd, lang, arena)
		if !ok {
			return nil, false
		}
		children = append(children, newParentNodeInArena(arena, argSym, argNamed, []*Node{value}, nil, 0))
		if i < len(items)-1 {
			commaPos := csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && source[span[1]] != ',' {
				return nil, false
			}
			if commaPos == 0 {
				commaPos = span[1]
			}
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok && commaSym != 0 {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildMemberAccessExpressionNode(arena *nodeArena, source []byte, lang *Language, start, dotPos, end uint32) (*Node, bool) {
	left, ok := csharpRecoverQueryExpressionNodeFromRange(source, start, dotPos, lang, arena)
	if !ok {
		return nil, false
	}
	rightStart, rightEnd, ok := csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, dotPos+1))
	if !ok || rightEnd != end {
		return nil, false
	}
	sym, ok := symbolByName(lang, "member_access_expression")
	if !ok {
		return nil, false
	}
	dotTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ".", dotPos, dotPos+1)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, rightStart, rightEnd, lang, arena)
	if !ok {
		return nil, false
	}
	expressionID, _ := lang.FieldByName("expression")
	nameID, _ := lang.FieldByName("name")
	fields := cloneFieldIDSliceInArena(arena, []FieldID{expressionID, 0, nameID})
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{left, dotTok, nameNode}, fields, 0), true
}

func csharpBuildObjectCreationExpressionNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || !csharpHasKeywordAt(source, start, "new") {
		return nil, false
	}
	typeStart := csharpSkipSpaceBytes(source, start+3)
	if typeStart >= end {
		return nil, false
	}
	openPos, ok := csharpFindInvocationOpenParen(source, typeStart, end)
	if !ok {
		return nil, false
	}
	typeEnd := csharpTrimRightSpaceBytes(source, openPos)
	if typeStart >= typeEnd {
		return nil, false
	}
	typeNode, ok := csharpBuildTypeNameNodeFromSource(arena, source, lang, typeStart, typeEnd)
	if !ok {
		return nil, false
	}
	args, ok := csharpBuildArgumentListNode(arena, source, lang, openPos, end)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "object_creation_expression")
	if !ok {
		return nil, false
	}
	newTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "new", start, start+3)
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{newTok, typeNode, args}, nil, 0), true
}

func csharpBuildTypeNameNodeFromSource(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	start, end = csharpTrimSpaceBounds(source, start, end)
	if start >= end {
		return nil, false
	}
	if node, ok := csharpBuildGenericNameNode(arena, source, lang, start, end); ok {
		return node, true
	}
	if string(source[start:end]) == "var" {
		sym, ok := symbolByName(lang, "implicit_type")
		if !ok {
			return nil, false
		}
		varTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "var", start, end)
		if !ok {
			return nil, false
		}
		named := symbolIsNamed(lang, sym)
		return newParentNodeInArena(arena, sym, named, []*Node{varTok}, nil, 0), true
	}
	return csharpBuildLambdaParameterTypeNode(arena, source, lang, start, end)
}

func csharpBuildGenericNameNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[end-1] != '>' {
		return nil, false
	}
	ltPos, ok := csharpFindGenericTypeArgumentOpen(source, start, end)
	if !ok || ltPos <= start {
		return nil, false
	}
	nameStart, nameEnd, ok := csharpScanIdentifierAt(source, start)
	if !ok || nameStart != start || nameEnd != ltPos {
		return nil, false
	}
	nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
	if !ok {
		return nil, false
	}
	typeArgs, ok := csharpBuildTypeArgumentListNode(arena, source, lang, ltPos, end)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "generic_name")
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{nameNode, typeArgs}, nil, 0), true
}

func csharpFindGenericTypeArgumentOpen(source []byte, start, end uint32) (uint32, bool) {
	depth := 0
	for i := end; i > start; i-- {
		switch source[i-1] {
		case '>':
			depth++
		case '<':
			depth--
			if depth == 0 {
				return i - 1, true
			}
		}
	}
	return 0, false
}

func csharpBuildTypeArgumentListNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '<' || source[end-1] != '>' {
		return nil, false
	}
	sym, ok := symbolByName(lang, "type_argument_list")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "<", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ">", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	items := csharpSplitTopLevelByComma(source, start+1, end-1)
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart >= itemEnd {
			return nil, false
		}
		typeNode, ok := csharpBuildTypeNameNodeFromSource(arena, source, lang, itemStart, itemEnd)
		if !ok {
			return nil, false
		}
		children = append(children, typeNode)
		if i < len(items)-1 {
			commaPos := csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && source[span[1]] != ',' {
				return nil, false
			}
			if commaPos == 0 {
				commaPos = span[1]
			}
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildAnonymousObjectCreationNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	openPos := csharpSkipSpaceBytes(source, start+3)
	if openPos >= end || end == 0 || source[openPos] != '{' || source[end-1] != '}' {
		return nil, false
	}
	sym, ok := symbolByName(lang, "anonymous_object_creation_expression")
	if !ok {
		return nil, false
	}
	newTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "new", start, start+3)
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "{", openPos, openPos+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "}", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{newTok, openTok}
	items := csharpSplitTopLevelByComma(source, openPos+1, end-1)
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart >= itemEnd {
			continue
		}
		if eqPos, ok := csharpFindTopLevelAssignment(source, itemStart, itemEnd); ok {
			nameStart, nameEnd, ok := csharpScanIdentifierAt(source, itemStart)
			if !ok {
				return nil, false
			}
			nameNode, ok := csharpBuildIdentifierNodeFromSource(source, nameStart, nameEnd, lang, arena)
			if !ok {
				return nil, false
			}
			eqTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "=", eqPos, eqPos+1)
			if !ok {
				return nil, false
			}
			valueNode, ok := csharpRecoverQueryExpressionNodeFromRange(source, eqPos+1, itemEnd, lang, arena)
			if !ok {
				return nil, false
			}
			children = append(children, nameNode, eqTok, valueNode)
		} else {
			valueNode, ok := csharpRecoverQueryExpressionNodeFromRange(source, itemStart, itemEnd, lang, arena)
			if !ok {
				return nil, false
			}
			children = append(children, valueNode)
		}
		if i < len(items)-1 {
			commaPos := csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && source[span[1]] != ',' {
				return nil, false
			}
			if commaPos == 0 {
				commaPos = span[1]
			}
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildLambdaExpressionNode(arena *nodeArena, source []byte, lang *Language, start, arrowPos, end uint32) (*Node, bool) {
	paramStart, paramEnd := csharpTrimSpaceBounds(source, start, arrowPos)
	if paramStart >= paramEnd {
		return nil, false
	}
	bodyNode, ok := csharpRecoverQueryExpressionNodeFromRange(source, arrowPos+2, end, lang, arena)
	if !ok {
		return nil, false
	}
	sym, ok := symbolByName(lang, "lambda_expression")
	if !ok {
		return nil, false
	}
	arrowTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "=>", arrowPos, arrowPos+2)
	if !ok {
		return nil, false
	}
	parametersID, _ := lang.FieldByName("parameters")
	bodyID, _ := lang.FieldByName("body")
	named := symbolIsNamed(lang, sym)
	if source[paramStart] == '(' && source[paramEnd-1] == ')' {
		params, ok := csharpBuildLambdaParameterListNode(arena, source, lang, paramStart, paramEnd)
		if !ok {
			return nil, false
		}
		fields := cloneFieldIDSliceInArena(arena, []FieldID{parametersID, 0, bodyID})
		return newParentNodeInArena(arena, sym, named, []*Node{params, arrowTok, bodyNode}, fields, 0), true
	}
	if typeStart, typeEnd, ok := csharpScanIdentifierAt(source, paramStart); ok {
		cursor := csharpSkipSpaceBytes(source, typeEnd)
		if cursor < paramEnd && source[cursor] == '(' {
			if closeParen, ok := csharpFindMatchingParenByte(source, cursor, paramEnd); ok && closeParen+1 == paramEnd {
				typeNode, ok := csharpBuildLambdaParameterTypeNode(arena, source, lang, typeStart, typeEnd)
				if !ok {
					return nil, false
				}
				params, ok := csharpBuildLambdaParameterListNode(arena, source, lang, cursor, paramEnd)
				if !ok {
					return nil, false
				}
				typeID, _ := lang.FieldByName("type")
				fields := cloneFieldIDSliceInArena(arena, []FieldID{typeID, parametersID, 0, bodyID})
				return newParentNodeInArena(arena, sym, named, []*Node{typeNode, params, arrowTok, bodyNode}, fields, 0), true
			}
		}
		if typeEnd == paramEnd {
			paramSym, ok := symbolByName(lang, "implicit_parameter")
			if !ok {
				return nil, false
			}
			paramNamed := symbolIsNamed(lang, paramSym)
			paramNode := newLeafNodeInArena(arena, paramSym, paramNamed, typeStart, typeEnd, advancePointByBytes(Point{}, source[:typeStart]), advancePointByBytes(Point{}, source[:typeEnd]))
			fields := cloneFieldIDSliceInArena(arena, []FieldID{parametersID, 0, bodyID})
			return newParentNodeInArena(arena, sym, named, []*Node{paramNode, arrowTok, bodyNode}, fields, 0), true
		}
	}
	return nil, false
}

func csharpBuildLambdaParameterListNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) || source[start] != '(' || source[end-1] != ')' {
		return nil, false
	}
	items := csharpSplitTopLevelByComma(source, start+1, end-1)
	if len(items) == 0 {
		return nil, false
	}
	sym, ok := symbolByName(lang, "parameter_list")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "(", start, start+1)
	if !ok {
		return nil, false
	}
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ")", end-1, end)
	if !ok {
		return nil, false
	}
	children := []*Node{openTok}
	for i, span := range items {
		itemStart, itemEnd := csharpTrimSpaceBounds(source, span[0], span[1])
		if itemStart >= itemEnd {
			return nil, false
		}
		param, ok := csharpBuildLambdaParameterNode(arena, source, lang, itemStart, itemEnd)
		if !ok {
			return nil, false
		}
		children = append(children, param)
		if i < len(items)-1 {
			commaPos := csharpFindCommaBetween(source, span[1], items[i+1][0])
			if commaPos == 0 && source[span[1]] != ',' {
				return nil, false
			}
			if commaPos == 0 {
				commaPos = span[1]
			}
			commaTok, ok := csharpBuildLeafNodeByName(arena, source, lang, ",", commaPos, commaPos+1)
			if !ok {
				return nil, false
			}
			children = append(children, commaTok)
		}
	}
	children = append(children, closeTok)
	if arena != nil {
		buf := arena.allocNodeSlice(len(children))
		copy(buf, children)
		children = buf
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, nil, 0), true
}

func csharpBuildLambdaParameterNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	firstStart, firstEnd, ok := csharpScanIdentifierAt(source, start)
	if !ok || firstStart != start {
		return nil, false
	}
	cursor := csharpSkipSpaceBytes(source, firstEnd)
	var children []*Node
	var fields []FieldID
	typeID, _ := lang.FieldByName("type")
	nameID, _ := lang.FieldByName("name")
	if cursor < end {
		secondStart, secondEnd, ok := csharpScanIdentifierAt(source, cursor)
		if !ok || secondStart != cursor || csharpSkipSpaceBytes(source, secondEnd) != end {
			return nil, false
		}
		typeNode, ok := csharpBuildLambdaParameterTypeNode(arena, source, lang, firstStart, firstEnd)
		if !ok {
			return nil, false
		}
		nameNode, ok := csharpBuildIdentifierNodeFromSource(source, secondStart, secondEnd, lang, arena)
		if !ok {
			return nil, false
		}
		children = []*Node{typeNode, nameNode}
		fields = []FieldID{typeID, nameID}
	} else {
		nameNode, ok := csharpBuildIdentifierNodeFromSource(source, firstStart, firstEnd, lang, arena)
		if !ok {
			return nil, false
		}
		children = []*Node{nameNode}
		fields = []FieldID{nameID}
	}
	if arena != nil {
		childBuf := arena.allocNodeSlice(len(children))
		copy(childBuf, children)
		children = childBuf
	}
	sym, ok := symbolByName(lang, "parameter")
	if !ok {
		return nil, false
	}
	fieldIDs := cloneFieldIDSliceInArena(arena, fields)
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, children, fieldIDs, 0), true
}

func csharpBuildLambdaParameterTypeNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	if csharpIsPredefinedTypeName(source[start:end]) {
		if node, ok := csharpBuildLeafNodeByName(arena, source, lang, "predefined_type", start, end); ok {
			return node, true
		}
	}
	return csharpBuildIdentifierNodeFromSource(source, start, end, lang, arena)
}

func csharpIsPredefinedTypeName(name []byte) bool {
	switch string(name) {
	case "bool", "byte", "char", "decimal", "double", "float", "int", "long", "object", "sbyte", "short", "string", "uint", "ulong", "ushort", "void":
		return true
	}
	return false
}

func csharpBuildStringLiteralNode(arena *nodeArena, source []byte, lang *Language, start, end uint32) (*Node, bool) {
	sym, ok := symbolByName(lang, "string_literal")
	if !ok {
		return nil, false
	}
	openTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "\"", start, start+1)
	if !ok {
		return nil, false
	}
	contentSym, ok := symbolByName(lang, "string_literal_content")
	if !ok {
		return nil, false
	}
	contentNamed := symbolIsNamed(lang, contentSym)
	content := newLeafNodeInArena(arena, contentSym, contentNamed, start+1, end-1, advancePointByBytes(Point{}, source[:start+1]), advancePointByBytes(Point{}, source[:end-1]))
	closeTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "\"", end-1, end)
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newParentNodeInArena(arena, sym, named, []*Node{openTok, content, closeTok}, nil, 0), true
}

func csharpBuildLeafNodeByName(arena *nodeArena, source []byte, lang *Language, name string, start, end uint32) (*Node, bool) {
	sym, ok := symbolByName(lang, name)
	if !ok {
		return nil, false
	}
	named := symbolIsNamed(lang, sym)
	return newLeafNodeInArena(arena, sym, named, start, end, advancePointByBytes(Point{}, source[:start]), advancePointByBytes(Point{}, source[:end])), true
}
