package gotreesitter

import "bytes"

// These query-expression repairs remain conservative fallbacks for legacy,
// generated, and overridden C# language artifacts without native capability
// certification.

func normalizeCSharpQueryExpressions(root *Node, source []byte, p *Parser) {
	if root == nil || p == nil || p.language == nil || p.language.Name != "c_sharp" || len(source) == 0 {
		return
	}
	if root.ownerArena == nil {
		return
	}
	if !root.HasError() && root.EndByte() >= uint32(len(source)) {
		return
	}
	if recovered, ok := csharpRecoverQueryAssignmentsRoot(source, p, root.ownerArena); ok {
		*root = *recovered
		root.parent = nil
		root.childIndex = -1
		return
	}
	spec, ok := csharpFindSimpleJoinQuerySpec(source)
	if !ok {
		return
	}
	recovered, ok := csharpRecoverQuerySkeletonRoot(source, p, root.ownerArena, spec)
	if !ok {
		return
	}
	*root = *recovered
	root.parent = nil
	root.childIndex = -1
}

func csharpRecoverQueryAssignmentsRoot(source []byte, p *Parser, arena *nodeArena) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil {
		return nil, false
	}
	specs, ok := csharpFindQueryAssignmentSpecs(source)
	if !ok || len(specs) == 0 {
		return nil, false
	}
	skeleton := append([]byte(nil), source...)
	for _, spec := range specs {
		for i := spec.queryStart; i < spec.queryEnd; i++ {
			skeleton[i] = ' '
		}
		if spec.queryStart < uint32(len(skeleton)) {
			skeleton[spec.queryStart] = '0'
		}
	}
	tree, err := p.parseForRecovery(skeleton)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	defer tree.Release()
	rt := *tree.rawParseRuntime()
	recoveredRoot := tree.RootNode()
	if rt.StopReason != ParseStopAccepted || rt.Truncated || rt.TokenSourceEOFEarly || recoveredRoot.HasError() {
		return nil, false
	}
	cloned := cloneTreeNodesIntoArena(recoveredRoot, arena)
	if cloned == nil {
		return nil, false
	}
	for _, spec := range specs {
		queryExpr, ok := csharpBuildRecoveredQueryExpression(arena, source, p, spec)
		if !ok {
			return nil, false
		}
		if !csharpReplaceRecoveredQueryExpression(cloned, p.language, spec.queryStart, spec.queryEnd, queryExpr) {
			return nil, false
		}
	}
	return cloned, true
}

func csharpFindQueryAssignmentSpecs(source []byte) ([]csharpQueryAssignmentSpec, bool) {
	var specs []csharpQueryAssignmentSpec
	cursor := uint32(0)
	for cursor < uint32(len(source)) {
		eqRel := bytes.IndexByte(source[cursor:], '=')
		if eqRel < 0 {
			break
		}
		eqPos := cursor + uint32(eqRel)
		queryStart := csharpSkipSpaceBytes(source, eqPos+1)
		if !csharpHasKeywordAt(source, queryStart, "from") {
			cursor = eqPos + 1
			continue
		}
		spec, ok := csharpParseQueryAssignmentSpec(source, queryStart)
		if !ok {
			cursor = eqPos + 1
			continue
		}
		specs = append(specs, spec)
		cursor = spec.semiPos + 1
	}
	return specs, len(specs) > 0
}

func csharpParseQueryAssignmentSpec(source []byte, queryStart uint32) (csharpQueryAssignmentSpec, bool) {
	var spec csharpQueryAssignmentSpec
	spec.queryStart = queryStart
	semiRel := bytes.IndexByte(source[queryStart:], ';')
	if semiRel < 0 {
		return spec, false
	}
	spec.semiPos = queryStart + uint32(semiRel)
	spec.queryEnd = csharpTrimRightSpaceBytes(source, spec.semiPos)
	return csharpParseQueryExpressionSpec(source, spec)
}

func csharpBuildRecoveredQueryExpression(arena *nodeArena, source []byte, p *Parser, spec csharpQueryAssignmentSpec) (*Node, bool) {
	if arena == nil || p == nil || p.language == nil || len(spec.clauses) == 0 {
		return nil, false
	}
	return csharpBuildRecoveredQueryExpressionWithExpr(arena, source, p.language, spec, func(span [2]uint32) (*Node, bool) {
		return csharpRecoverExpressionNodeFromRange(source, span[0], span[1], p, arena)
	})
}

type csharpSimpleJoinQuerySpec struct {
	queryStart uint32
	queryEnd   uint32
	semiPos    uint32

	fromStart uint32
	fromEnd   uint32
	rangeName [2]uint32
	in1Start  uint32
	in1End    uint32
	source1   [2]uint32

	joinStart uint32
	joinEnd   uint32
	joinName  [2]uint32
	in2Start  uint32
	in2End    uint32
	source2   [2]uint32

	onStart    uint32
	onEnd      uint32
	leftObj    [2]uint32
	leftDotPos uint32
	leftProp   [2]uint32

	equalsStart uint32
	equalsEnd   uint32
	rightObj    [2]uint32
	rightDotPos uint32
	rightProp   [2]uint32

	selectStart uint32
	selectEnd   uint32
	selectName  [2]uint32
}

func csharpRecoverQuerySkeletonRoot(source []byte, p *Parser, arena *nodeArena, spec csharpSimpleJoinQuerySpec) (*Node, bool) {
	if p == nil || p.language == nil || arena == nil {
		return nil, false
	}
	skeleton := append([]byte(nil), source...)
	for i := spec.queryStart; i < spec.queryEnd; i++ {
		skeleton[i] = ' '
	}
	if spec.queryStart < uint32(len(skeleton)) {
		skeleton[spec.queryStart] = '0'
	}
	tree, err := p.parseForRecovery(skeleton)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return nil, false
	}
	defer tree.Release()
	rt := *tree.rawParseRuntime()
	recoveredRoot := tree.RootNode()
	if rt.StopReason != ParseStopAccepted || rt.Truncated || rt.TokenSourceEOFEarly || recoveredRoot.HasError() {
		return nil, false
	}
	cloned := cloneTreeNodesIntoArena(recoveredRoot, arena)
	if cloned == nil {
		return nil, false
	}
	queryExpr, ok := csharpBuildSimpleJoinQueryExpression(arena, source, p.language, spec)
	if !ok {
		return nil, false
	}
	if !csharpReplaceRecoveredQueryExpression(cloned, p.language, spec.queryStart, spec.queryEnd, queryExpr) {
		return nil, false
	}
	return cloned, true
}

func csharpFindSimpleJoinQuerySpec(source []byte) (csharpSimpleJoinQuerySpec, bool) {
	var spec csharpSimpleJoinQuerySpec
	if len(source) == 0 {
		return spec, false
	}
	eq := bytes.IndexByte(source, '=')
	if eq < 0 {
		return spec, false
	}
	spec.queryStart = csharpSkipSpaceBytes(source, uint32(eq+1))
	spec.fromStart = spec.queryStart
	if !csharpHasKeywordAt(source, spec.fromStart, "from") {
		return spec, false
	}
	spec.fromEnd = spec.fromStart + 4
	var ok bool
	if spec.rangeName[0], spec.rangeName[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.fromEnd)); !ok {
		return spec, false
	}
	spec.in1Start = csharpSkipSpaceBytes(source, spec.rangeName[1])
	if !csharpHasKeywordAt(source, spec.in1Start, "in") {
		return spec, false
	}
	spec.in1End = spec.in1Start + 2
	if spec.source1[0], spec.source1[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.in1End)); !ok {
		return spec, false
	}
	spec.joinStart = csharpSkipSpaceBytes(source, spec.source1[1])
	if !csharpHasKeywordAt(source, spec.joinStart, "join") {
		return spec, false
	}
	spec.joinEnd = spec.joinStart + 4
	if spec.joinName[0], spec.joinName[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.joinEnd)); !ok {
		return spec, false
	}
	spec.in2Start = csharpSkipSpaceBytes(source, spec.joinName[1])
	if !csharpHasKeywordAt(source, spec.in2Start, "in") {
		return spec, false
	}
	spec.in2End = spec.in2Start + 2
	if spec.source2[0], spec.source2[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.in2End)); !ok {
		return spec, false
	}
	spec.onStart = csharpSkipSpaceBytes(source, spec.source2[1])
	if !csharpHasKeywordAt(source, spec.onStart, "on") {
		return spec, false
	}
	spec.onEnd = spec.onStart + 2
	if spec.leftObj[0], spec.leftObj[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.onEnd)); !ok {
		return spec, false
	}
	spec.leftDotPos = spec.leftObj[1]
	if spec.leftDotPos >= uint32(len(source)) || source[spec.leftDotPos] != '.' {
		return spec, false
	}
	if spec.leftProp[0], spec.leftProp[1], ok = csharpScanIdentifierAt(source, spec.leftDotPos+1); !ok {
		return spec, false
	}
	spec.equalsStart = csharpSkipSpaceBytes(source, spec.leftProp[1])
	if !csharpHasKeywordAt(source, spec.equalsStart, "equals") {
		return spec, false
	}
	spec.equalsEnd = spec.equalsStart + 6
	if spec.rightObj[0], spec.rightObj[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.equalsEnd)); !ok {
		return spec, false
	}
	spec.rightDotPos = spec.rightObj[1]
	if spec.rightDotPos >= uint32(len(source)) || source[spec.rightDotPos] != '.' {
		return spec, false
	}
	if spec.rightProp[0], spec.rightProp[1], ok = csharpScanIdentifierAt(source, spec.rightDotPos+1); !ok {
		return spec, false
	}
	spec.selectStart = csharpSkipSpaceBytes(source, spec.rightProp[1])
	if !csharpHasKeywordAt(source, spec.selectStart, "select") {
		return spec, false
	}
	spec.selectEnd = spec.selectStart + 6
	if spec.selectName[0], spec.selectName[1], ok = csharpScanIdentifierAt(source, csharpSkipSpaceBytes(source, spec.selectEnd)); !ok {
		return spec, false
	}
	spec.queryEnd = spec.selectName[1]
	spec.semiPos = csharpSkipSpaceBytes(source, spec.queryEnd)
	if spec.semiPos >= uint32(len(source)) || source[spec.semiPos] != ';' {
		return spec, false
	}
	return spec, true
}

func csharpReplaceRecoveredQueryExpression(root *Node, lang *Language, queryStart, queryEnd uint32, queryExpr *Node) bool {
	if root == nil || lang == nil || queryExpr == nil {
		return false
	}
	var walk func(*Node) bool
	walk = func(n *Node) bool {
		if n == nil {
			return false
		}
		if n.Type(lang) == "variable_declarator" && len(n.children) >= 3 {
			expr := n.children[len(n.children)-1]
			if expr != nil && expr.startByte <= queryStart && expr.endByte > queryStart && n.startByte <= queryStart {
				n.children[len(n.children)-1] = queryExpr
				queryExpr.parent = n
				queryExpr.childIndex = int32(len(n.children) - 1)
				for cur := n; cur != nil; cur = cur.parent {
					populateParentNode(cur, cur.children)
				}
				return true
			}
		}
		for _, child := range n.children {
			if walk(child) {
				n.setHasError(false)
				return true
			}
		}
		return false
	}
	return walk(root)
}

func csharpBuildSimpleJoinQueryExpression(arena *nodeArena, source []byte, lang *Language, spec csharpSimpleJoinQuerySpec) (*Node, bool) {
	if arena == nil || lang == nil {
		return nil, false
	}
	queryExprSym, ok := symbolByName(lang, "query_expression")
	if !ok {
		return nil, false
	}
	fromClauseSym, ok := symbolByName(lang, "from_clause")
	if !ok {
		return nil, false
	}
	joinClauseSym, ok := symbolByName(lang, "join_clause")
	if !ok {
		return nil, false
	}
	selectClauseSym, ok := symbolByName(lang, "select_clause")
	if !ok {
		return nil, false
	}
	memberAccessSym, ok := symbolByName(lang, "member_access_expression")
	if !ok {
		return nil, false
	}
	identifierSym, ok := symbolByName(lang, "identifier")
	if !ok {
		return nil, false
	}
	fromSym, ok := symbolByName(lang, "from")
	if !ok {
		return nil, false
	}
	inSym, ok := symbolByName(lang, "in")
	if !ok {
		return nil, false
	}
	joinSym, ok := symbolByName(lang, "join")
	if !ok {
		return nil, false
	}
	onSym, ok := symbolByName(lang, "on")
	if !ok {
		return nil, false
	}
	equalsSym, ok := symbolByName(lang, "equals")
	if !ok {
		return nil, false
	}
	selectSym, ok := symbolByName(lang, "select")
	if !ok {
		return nil, false
	}
	dotSym, ok := symbolByName(lang, ".")
	if !ok {
		return nil, false
	}
	nameFieldID, hasNameField := lang.FieldByName("name")
	expressionFieldID, hasExpressionField := lang.FieldByName("expression")
	if !hasNameField || !hasExpressionField {
		return nil, false
	}
	identifierNamed := symbolIsNamed(lang, identifierSym)
	memberAccessNamed := symbolIsNamed(lang, memberAccessSym)
	fromClauseNamed := symbolIsNamed(lang, fromClauseSym)
	joinClauseNamed := symbolIsNamed(lang, joinClauseSym)
	selectClauseNamed := symbolIsNamed(lang, selectClauseSym)
	queryExprNamed := symbolIsNamed(lang, queryExprSym)

	ident := func(span [2]uint32) *Node {
		return newLeafNodeInArena(
			arena,
			identifierSym,
			identifierNamed,
			span[0],
			span[1],
			advancePointByBytes(Point{}, source[:span[0]]),
			advancePointByBytes(Point{}, source[:span[1]]),
		)
	}
	leaf := func(sym Symbol, start, end uint32) *Node {
		named := symbolIsNamed(lang, sym)
		return newLeafNodeInArena(
			arena,
			sym,
			named,
			start,
			end,
			advancePointByBytes(Point{}, source[:start]),
			advancePointByBytes(Point{}, source[:end]),
		)
	}
	memberAccess := func(obj, prop [2]uint32, dotPos uint32) *Node {
		children := []*Node{
			ident(obj),
			leaf(dotSym, dotPos, dotPos+1),
			ident(prop),
		}
		fieldIDs := cloneFieldIDSliceInArena(arena, []FieldID{expressionFieldID, 0, nameFieldID})
		return newParentNodeInArena(arena, memberAccessSym, memberAccessNamed, children, fieldIDs, 0)
	}

	fromChildren := []*Node{
		leaf(fromSym, spec.fromStart, spec.fromEnd),
		ident(spec.rangeName),
		leaf(inSym, spec.in1Start, spec.in1End),
		ident(spec.source1),
	}
	fromFields := cloneFieldIDSliceInArena(arena, []FieldID{0, nameFieldID, 0, 0})
	fromClause := newParentNodeInArena(arena, fromClauseSym, fromClauseNamed, fromChildren, fromFields, 0)

	joinClause := newParentNodeInArena(arena, joinClauseSym, joinClauseNamed, []*Node{
		leaf(joinSym, spec.joinStart, spec.joinEnd),
		ident(spec.joinName),
		leaf(inSym, spec.in2Start, spec.in2End),
		ident(spec.source2),
		leaf(onSym, spec.onStart, spec.onEnd),
		memberAccess(spec.leftObj, spec.leftProp, spec.leftDotPos),
		leaf(equalsSym, spec.equalsStart, spec.equalsEnd),
		memberAccess(spec.rightObj, spec.rightProp, spec.rightDotPos),
	}, nil, 0)

	selectClause := newParentNodeInArena(arena, selectClauseSym, selectClauseNamed, []*Node{
		leaf(selectSym, spec.selectStart, spec.selectEnd),
		ident(spec.selectName),
	}, nil, 0)

	queryExpr := newParentNodeInArena(arena, queryExprSym, queryExprNamed, []*Node{
		fromClause,
		joinClause,
		selectClause,
	}, nil, 0)
	return queryExpr, true
}
