package gotreesitter

// Swift control-flow trailing-closure ambiguity recovery (issues #118, #123, #561).
//
// The Swift grammar misparses a control-flow header whose expression ends in a
// value that can take a trailing closure, because the following statement body
// brace is greedily consumed as that trailing closure:
//
//   - if/while condition with a comparison operator (#118): `if x > 0 { foo() }`
//     parses as `... 0 { foo() }` → call_expression(0, lambda_literal{...}), so
//     the body is lost and the function collapses into ERROR nodes.
//   - for…in with a range or call iterable (#123): `for i in 0..<n { t += i }`
//     parses as `0..<n { ... }` → range_expression(..., call_expression(n,
//     lambda_literal{...})), so the loop body is swallowed and the enclosing
//     function silently collapses to _modifierless_function_declaration_no_body
//     with the body statements re-homed as siblings — and *without* an ERROR
//     node, so it can't even be detected as a parse failure.
//   - for…in with a range iterable, inside a type body, followed by another
//     method (#561): `class T { func a() { for n in 4...100 { } } func b() {} }`
//     hits the same trailing-closure absorption, but this time a for_statement
//     node still forms (nesting the `for` token), so the #123 detection — which
//     only looks for a `for` token with no for_statement parent — misses it. The
//     absorption still leaves an ERROR somewhere in the for_statement's subtree
//     (the swallowed loop body plus whatever the parser glued the next member
//     onto), so this case is detected by that for_statement's HasError() bit.
//
// Swift's real grammar forbids trailing closures in both positions; wrapping the
// condition / iterable in parentheses removes the ambiguity (`if (x > 0) {…}`,
// `for i in (0..<n) {…}` parse cleanly). This pass detects each affected header,
// reparses the source with synthetic parentheses around it, then maps the
// recovered tree back to the original byte coordinates — dropping the synthetic
// parens and unwrapping the synthetic parenthesised expression so the result is
// byte-faithful to the original source.

type swiftParenInsert struct {
	pos uint32 // original-source byte offset to insert before
	ch  byte   // '(' or ')'
}

func normalizeSwiftRecoveredTrailingClosureConditions(root *Node, source []byte, p *Parser, lang *Language) {
	if root == nil || p == nil || p.skipRecoveryReparse || lang == nil || root.ownerArena == nil || len(source) == 0 {
		return
	}
	// Note: unlike the if/while case (#118), the for…in collapse (#123) leaves no
	// ERROR node, so we cannot gate on root.HasError(). Instead we always run the
	// (cheap) detection walk and bail when it finds nothing to rewrite.
	if root.Type(lang) != "source_file" {
		return
	}
	inserts := swiftCollectConditionParenInserts(root, source, lang)
	if len(inserts) == 0 {
		return
	}
	transformed, insTPos := swiftApplyParenInserts(source, inserts)
	tree, err := parseSwiftCleanFullSourceRecovery(p, transformed, lang)
	if err != nil || tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return
	}
	defer tree.Release()
	tRoot := tree.RootNode()
	// Apply only when removing the trailing-closure ambiguity makes the reparse
	// fully clean. This keeps the rewrite trivially safe — a file with other,
	// unrelated parse errors is left untouched rather than partially rewritten.
	// The byte-faithfulness check is essential: an incompletely-bracketed chain
	// still collapses to _modifierless_function_declaration_no_body and silently
	// drops trailing statements *without* an ERROR node (#131), so HasError alone
	// would accept a truncated, still-broken reparse.
	if !swiftCleanFullSourceRecoveryAccepted(tree, transformed, lang) {
		return
	}
	rm := &swiftRemap{
		tsrc:   transformed,
		insSet: make(map[uint32]bool, len(insTPos)),
		insPos: insTPos,
		arena:  root.ownerArena,
	}
	for _, t := range insTPos {
		rm.insSet[t] = true
	}
	newRoot, ok := rm.remap(tRoot)
	if !ok || newRoot == nil {
		return
	}
	root.symbol = newRoot.symbol
	root.productionID = newRoot.productionID
	root.setFieldIDs(newRoot.fieldIDs())
	root.children = cloneNodeSliceIfArena(root.ownerArena, newRoot.children)
	// populateParentNode derives the span from the children; reassert the
	// source_file bounds afterwards so leading/trailing trivia is covered, then
	// recompute points (root included) from the corrected byte offsets.
	populateParentNode(root, root.children)
	root.startByte = 0
	root.endByte = uint32(len(source))
	recomputeNodePointsFromBytes(root, source)
	if !swiftAnyChildHasError(root) {
		root.setHasError(false)
	}
}

// swiftCollectConditionParenInserts walks the (broken) tree with explicit parent
// tracking and, for every `if`/`while` keyword that failed to form its statement
// (#118), every `if_statement` whose then-block was swallowed as a trailing
// closure and whose real `else` landed in an ERROR node instead of the
// else-clause (#560), every `for` keyword whose loop failed to form a
// for_statement (#123), and every `for` keyword whose for_statement formed but
// still carries an ERROR (#561), computes a `(`/`)` insertion pair around the
// trailing-closure-ambiguous expression (the condition, or the for…in
// iterable). The body brace is located by scanning the source forward to the
// first top-level `{`, skipping comments, strings and bracketed/parenthesised
// groups.
func swiftCollectConditionParenInserts(root *Node, source []byte, lang *Language) []swiftParenInsert {
	var inserts []swiftParenInsert
	seen := make(map[uint32]bool)
	add := func(lp, rp uint32, ok bool) {
		if ok && !seen[lp] {
			seen[lp] = true
			inserts = append(inserts, swiftParenInsert{pos: lp, ch: '('})
			inserts = append(inserts, swiftParenInsert{pos: rp, ch: ')'})
		}
	}
	var walk func(n, parent *Node)
	walk = func(n, parent *Node) {
		if n == nil {
			return
		}
		typ := n.Type(lang)
		parentType := ""
		if parent != nil {
			parentType = parent.Type(lang)
		}
		switch {
		case (typ == "if" || typ == "while") && resultChildCount(n) == 0:
			if (parentType != "if_statement" && parentType != "while_statement") || (parent != nil && parent.HasError()) {
				// Bracket this header's condition, then follow any `else if`
				// continuation (#131): the chained `if` keyword is swallowed into
				// an ERROR node, so it never surfaces as its own `if` token for the
				// walk to find — we have to discover it by scanning the source from
				// the body's matching close brace.
				swiftCollectIfChainParens(source, n.endByte, add)
			}
		case typ == "if_statement":
			if keywordEnd, ok := swiftIfStatementSwallowedThenBlockKeywordEnd(n, lang); ok {
				// The condition's last operand absorbed the then-block as a
				// trailing closure (#560), so the real `else` that follows could
				// not close this if_statement and was wrapped in an ERROR node
				// instead — with the block after it reinterpreted as this
				// if_statement's own body. Bracket the condition the same way as
				// the orphaned-`if` case above.
				swiftCollectIfChainParens(source, keywordEnd, add)
			}
		case typ == "for" && resultChildCount(n) == 0:
			// A well-formed loop nests the `for` token inside an error-free
			// for_statement. Two broken shapes both need the same iterable
			// bracketing: a total collapse leaves the `for` token dangling
			// under source_file/ERROR/etc (#123), while a partial collapse
			// (#561) still nests it inside a for_statement, but the loop's
			// own closing brace was greedily consumed as a trailing closure
			// on the iterable's upper bound, so the for_statement (and
			// everything the parser glued on after it) carries an ERROR.
			if parentType != "for_statement" || (parent != nil && parent.HasError()) {
				lp, rp, ok := swiftForIterableParenPositions(source, n.endByte)
				add(lp, rp, ok)
			}
		}
		for i := 0; i < resultChildCount(n); i++ {
			walk(resultChildAt(n, i), n)
		}
	}
	walk(root, nil)
	return inserts
}

// swiftIfStatementSwallowedThenBlockKeywordEnd reports whether if_statement n
// suffers the #560 collapse: a comparison condition whose last operand takes a
// parenthesised-member-access argument absorbs the then-block as a trailing
// closure, so the real `else` keyword that follows cannot close this
// if_statement and is wrapped in an ERROR node instead — with the block after
// that ERROR node reinterpreted as this if_statement's own body. On success it
// returns the byte offset just past this if_statement's own `if` keyword,
// ready for swiftCollectIfChainParens.
func swiftIfStatementSwallowedThenBlockKeywordEnd(n *Node, lang *Language) (uint32, bool) {
	childCount := resultChildCount(n)
	if childCount == 0 {
		return 0, false
	}
	first := resultChildAt(n, 0)
	if first == nil || first.Type(lang) != "if" {
		return 0, false
	}
	for i := 1; i < childCount; i++ {
		child := resultChildAt(n, i)
		if child == nil || child.Type(lang) != "ERROR" || resultChildCount(child) == 0 {
			continue
		}
		if errFirst := resultChildAt(child, 0); errFirst != nil && errFirst.Type(lang) == "else" {
			return first.endByte, true
		}
	}
	return 0, false
}

// swiftCollectIfChainParens brackets the condition of an `if`/`while` header that
// begins right after a control-flow keyword ending at keywordEnd, then walks the
// whole `if … else if …` chain bracketing each subsequent condition. The `else if`
// continuation (#131) is found by source-scanning: when the chained `if`'s body
// brace is greedily consumed as a trailing closure, the function collapses to
// _modifierless_function_declaration_no_body and the chained `if` keyword is buried
// in an ERROR node, so it never surfaces as its own token for the tree walk to find.
// Anchoring on the first (surviving) `if` token and following the chain through the
// source recovers every condition.
func swiftCollectIfChainParens(source []byte, keywordEnd uint32, add func(lp, rp uint32, ok bool)) {
	for {
		condStart := swiftSkipHorizontalAndNewlineSpace(source, keywordEnd)
		bodyOpen, found := swiftFindConditionBodyBrace(source, condStart)
		if !found {
			return
		}
		// An optional-binding condition (`if let a = a {`, `if var a = a {`)
		// cannot be bracketed as a whole: real Swift forbids parenthesizing the
		// entire `let PATTERN = EXPR` clause. Bracket only the right-hand-side
		// expression instead (#558), leaving the pattern and `=` untouched.
		if lp, rp, ok := swiftOptionalBindingConditionParens(source, condStart, bodyOpen); ok {
			add(lp, rp, true)
		} else {
			rp := bodyOpen
			for rp > condStart {
				switch source[rp-1] {
				case ' ', '\t', '\n', '\r':
					rp--
					continue
				}
				break
			}
			// Skip the insertion when the span is empty or already parenthesised
			// (`if (cond) {`); swiftConditionParenPositions applies the same guard.
			if condStart < rp && !(source[condStart] == '(' && source[rp-1] == ')') {
				add(condStart, rp, true)
			}
		}
		// Follow an `else if` continuation: find the body's matching close brace,
		// then look for `else if`. Anything else (a plain `else {` block, or the end
		// of the chain) terminates the walk.
		closeBrace, ok := swiftFindMatchingCloseBrace(source, bodyOpen)
		if !ok {
			return
		}
		nextKeywordEnd, isElseIf := swiftFindElseIfKeywordEnd(source, closeBrace+1)
		if !isElseIf {
			return
		}
		keywordEnd = nextKeywordEnd
	}
}

// swiftOptionalBindingConditionParens computes paren-insert positions around
// only the right-hand-side expression of a `let`/`var` optional-binding
// condition (`if let a = a {`), when the ambiguous nested-if-let and
// if-let-with-&&-and-nested-if shapes (#558) collapse the header into an
// ERROR node the same way the plain-boolean-condition case (#118) does.
// Returns ok=false when condStart does not begin with a `let`/`var` keyword,
// so the caller falls back to bracketing the entire condition.
func swiftOptionalBindingConditionParens(source []byte, condStart, bodyOpen uint32) (lp, rp uint32, ok bool) {
	patternStart, matched := swiftSkipOptionalBindingKeyword(source, condStart)
	if !matched {
		return 0, 0, false
	}
	eq, found := swiftFindOptionalBindingEquals(source, patternStart, bodyOpen)
	if !found {
		return 0, 0, false
	}
	rhsStart := swiftSkipHorizontalAndNewlineSpace(source, eq+1)
	rhsEnd := swiftFindOptionalBindingRHSEnd(source, rhsStart, bodyOpen)
	for rhsEnd > rhsStart {
		switch source[rhsEnd-1] {
		case ' ', '\t', '\n', '\r':
			rhsEnd--
			continue
		}
		break
	}
	if rhsStart >= rhsEnd {
		return 0, 0, false
	}
	// Already parenthesised (`if let a = (a) {`): no need to inject.
	if source[rhsStart] == '(' && source[rhsEnd-1] == ')' {
		return 0, 0, false
	}
	return rhsStart, rhsEnd, true
}

// swiftSkipOptionalBindingKeyword reports whether source at pos begins with
// the `let` or `var` keyword, word-boundaried, and if so returns the byte
// offset just past it.
func swiftSkipOptionalBindingKeyword(source []byte, pos uint32) (uint32, bool) {
	n := uint32(len(source))
	for _, kw := range [...]string{"let", "var"} {
		end := pos + uint32(len(kw))
		if end > n || string(source[pos:end]) != kw {
			continue
		}
		if end < n && isSwiftWordByte(source[end]) {
			continue // `letter`/`variable`, not the keyword
		}
		return end, true
	}
	return 0, false
}

// swiftFindOptionalBindingEquals scans forward from patternStart (right after
// the `let`/`var` keyword) to limit, looking for the pattern-binding `=` at
// bracket depth zero. It skips line/block comments, string literals, and
// ()/[] nesting (so tuple patterns like `let (a, b) = c` and type-annotated
// patterns like `let a: Foo = b` are handled), and rejects `==`, `!=`, `<=`,
// `>=`, and compound-assignment operators so a comparison is never mistaken
// for the binding operator.
func swiftFindOptionalBindingEquals(source []byte, patternStart, limit uint32) (uint32, bool) {
	depth := 0
	i := patternStart
	for i < limit {
		b := source[i]
		if b == '/' && i+1 < limit {
			if source[i+1] == '/' {
				i += 2
				for i < limit && source[i] != '\n' {
					i++
				}
				continue
			}
			if source[i+1] == '*' {
				i += 2
				depthC := 1
				for i+1 < limit && depthC > 0 {
					if source[i] == '/' && source[i+1] == '*' {
						depthC++
						i += 2
					} else if source[i] == '*' && source[i+1] == '/' {
						depthC--
						i += 2
					} else {
						i++
					}
				}
				continue
			}
		}
		if b == '"' {
			if i+2 < limit && source[i+1] == '"' && source[i+2] == '"' {
				i += 3
				for i+2 < limit && !(source[i] == '"' && source[i+1] == '"' && source[i+2] == '"') {
					if source[i] == '\\' {
						i++
					}
					i++
				}
				i += 3
				continue
			}
			i++
			for i < limit && source[i] != '"' {
				if source[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		switch b {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth == 0 {
				prevOK := i == patternStart || !swiftIsAssignmentOperatorByte(source[i-1])
				nextOK := i+1 >= limit || source[i+1] != '='
				if prevOK && nextOK {
					return i, true
				}
			}
		}
		i++
	}
	return 0, false
}

// swiftIsAssignmentOperatorByte reports whether b is an operator character
// that, immediately before `=`, would make it part of a compound-assignment
// or comparison operator (`==`, `!=`, `<=`, `>=`, `+=`, ...) rather than the
// bare pattern-binding `=`.
func swiftIsAssignmentOperatorByte(b byte) bool {
	switch b {
	case '<', '>', '!', '+', '-', '*', '/', '%', '&', '|', '^', '~':
		return true
	default:
		return false
	}
}

// swiftFindOptionalBindingRHSEnd scans forward from rhsStart to limit at
// bracket depth zero for the end of the binding's right-hand-side expression:
// either a top-level `,` (a further condition-list item, e.g. `if let a = a,
// let b = b {`) or limit itself (the body brace), whichever comes first.
func swiftFindOptionalBindingRHSEnd(source []byte, rhsStart, limit uint32) uint32 {
	depth := 0
	i := rhsStart
	for i < limit {
		b := source[i]
		if b == '/' && i+1 < limit {
			if source[i+1] == '/' {
				i += 2
				for i < limit && source[i] != '\n' {
					i++
				}
				continue
			}
			if source[i+1] == '*' {
				i += 2
				depthC := 1
				for i+1 < limit && depthC > 0 {
					if source[i] == '/' && source[i+1] == '*' {
						depthC++
						i += 2
					} else if source[i] == '*' && source[i+1] == '/' {
						depthC--
						i += 2
					} else {
						i++
					}
				}
				continue
			}
		}
		if b == '"' {
			if i+2 < limit && source[i+1] == '"' && source[i+2] == '"' {
				i += 3
				for i+2 < limit && !(source[i] == '"' && source[i+1] == '"' && source[i+2] == '"') {
					if source[i] == '\\' {
						i++
					}
					i++
				}
				i += 3
				continue
			}
			i++
			for i < limit && source[i] != '"' {
				if source[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		switch b {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return limit
}

// swiftFindMatchingCloseBrace scans forward from openPos (which must point at a
// `{`) to its matching `}` at the same brace depth, skipping line/block comments,
// string literals and ()/[] groups. Returns the index of the matching `}`, or
// ok=false if none is found.
func swiftFindMatchingCloseBrace(source []byte, openPos uint32) (uint32, bool) {
	n := uint32(len(source))
	if openPos >= n || source[openPos] != '{' {
		return 0, false
	}
	depth := 0
	i := openPos
	for i < n {
		b := source[i]
		// Comments.
		if b == '/' && i+1 < n {
			if source[i+1] == '/' {
				i += 2
				for i < n && source[i] != '\n' {
					i++
				}
				continue
			}
			if source[i+1] == '*' {
				i += 2
				depthC := 1
				for i+1 < n && depthC > 0 {
					if source[i] == '/' && source[i+1] == '*' {
						depthC++
						i += 2
					} else if source[i] == '*' && source[i+1] == '/' {
						depthC--
						i += 2
					} else {
						i++
					}
				}
				if depthC > 0 {
					// Unclosed comment runs to EOF; don't treat its last byte as code.
					i = n
				}
				continue
			}
		}
		// Strings.
		if b == '"' {
			if i+2 < n && source[i+1] == '"' && source[i+2] == '"' {
				i += 3
				for i+2 < n && !(source[i] == '"' && source[i+1] == '"' && source[i+2] == '"') {
					if source[i] == '\\' {
						i++
					}
					i++
				}
				i += 3
				continue
			}
			i++
			for i < n && source[i] != '"' {
				if source[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		switch b {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
		i++
	}
	return 0, false
}

// swiftFindElseIfKeywordEnd skips whitespace and comments from start and reports
// whether an `else if` continuation begins there. On a match it returns the byte
// offset just past the chained `if` keyword (the position swiftConditionParenPositions
// expects). A plain `else {` block, or any other token, returns ok=false.
func swiftFindElseIfKeywordEnd(source []byte, start uint32) (uint32, bool) {
	i := swiftSkipSpaceAndComments(source, start)
	n := uint32(len(source))
	// Byte-by-byte keyword matching keeps this allocation-free on the recovery path.
	if i+4 > n || source[i] != 'e' || source[i+1] != 'l' || source[i+2] != 's' || source[i+3] != 'e' {
		return 0, false
	}
	after := i + 4
	if after < n && isSwiftWordByte(source[after]) {
		return 0, false // `elsewhere`, not the `else` keyword.
	}
	i = swiftSkipSpaceAndComments(source, after)
	if i+2 > n || source[i] != 'i' || source[i+1] != 'f' {
		return 0, false // plain `else { … }`, no further condition.
	}
	end := i + 2
	if end < n && isSwiftWordByte(source[end]) {
		return 0, false // `iffy`, not the `if` keyword.
	}
	return end, true
}

// swiftSkipSpaceAndComments advances past horizontal/newline whitespace and
// line/block comments starting at i.
func swiftSkipSpaceAndComments(source []byte, i uint32) uint32 {
	n := uint32(len(source))
	for i < n {
		switch source[i] {
		case ' ', '\t', '\n', '\r':
			i++
			continue
		}
		if source[i] == '/' && i+1 < n {
			if source[i+1] == '/' {
				i += 2
				for i < n && source[i] != '\n' {
					i++
				}
				continue
			}
			if source[i+1] == '*' {
				i += 2
				depthC := 1
				for i+1 < n && depthC > 0 {
					if source[i] == '/' && source[i+1] == '*' {
						depthC++
						i += 2
					} else if source[i] == '*' && source[i+1] == '/' {
						depthC--
						i += 2
					} else {
						i++
					}
				}
				if depthC > 0 {
					// Unclosed comment runs to EOF.
					i = n
				}
				continue
			}
		}
		break
	}
	return i
}

// swiftForIterableParenPositions locates the `in` keyword that follows a `for`
// keyword ending at forKeywordEnd, then reuses the condition logic to bracket the
// iterable expression between `in` and the loop body brace.
func swiftForIterableParenPositions(source []byte, forKeywordEnd uint32) (lParen, rParen uint32, ok bool) {
	inEnd, found := swiftFindForInKeywordEnd(source, forKeywordEnd)
	if !found {
		return 0, 0, false
	}
	return swiftConditionParenPositions(source, inEnd)
}

// swiftFindForInKeywordEnd scans forward from start to the loop's `in` keyword at
// bracket depth zero, skipping line/block comments, string literals and ()/[]
// nesting (so a destructuring pattern like `for (a, b) in …` is handled). Returns
// the byte offset just past `in`, or ok=false if a `{`/`}`/`;` at depth zero is
// reached first (no `in` keyword → not a recoverable for…in header).
func swiftFindForInKeywordEnd(source []byte, start uint32) (uint32, bool) {
	depth := 0
	i := start
	n := uint32(len(source))
	for i < n {
		b := source[i]
		// Comments.
		if b == '/' && i+1 < n {
			if source[i+1] == '/' {
				i += 2
				for i < n && source[i] != '\n' {
					i++
				}
				continue
			}
			if source[i+1] == '*' {
				i += 2
				depthC := 1
				for i+1 < n && depthC > 0 {
					if source[i] == '/' && source[i+1] == '*' {
						depthC++
						i += 2
					} else if source[i] == '*' && source[i+1] == '/' {
						depthC--
						i += 2
					} else {
						i++
					}
				}
				continue
			}
		}
		// Strings.
		if b == '"' {
			if i+2 < n && source[i+1] == '"' && source[i+2] == '"' {
				i += 3
				for i+2 < n && !(source[i] == '"' && source[i+1] == '"' && source[i+2] == '"') {
					if source[i] == '\\' {
						i++
					}
					i++
				}
				i += 3
				continue
			}
			i++
			for i < n && source[i] != '"' {
				if source[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		switch b {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '{', '}', ';':
			if depth == 0 {
				return 0, false
			}
		}
		// The loop separator is the first word-boundaried `in` at depth zero.
		// Skip a backtick-escaped identifier `` `in` ``, which is a loop variable
		// name and not the keyword.
		if depth == 0 && b == 'i' && i+1 < n && source[i+1] == 'n' {
			backticked := i > 0 && source[i-1] == '`' && i+2 < n && source[i+2] == '`'
			beforeOK := i == 0 || !isSwiftWordByte(source[i-1])
			after := i + 2
			afterOK := after >= n || !isSwiftWordByte(source[after])
			if !backticked && beforeOK && afterOK {
				return after, true
			}
		}
		i++
	}
	return 0, false
}

// isSwiftWordByte reports whether b can be part of a Swift identifier. Any UTF-8
// continuation/lead byte (>= 0x80) counts, since Swift identifiers admit Unicode
// characters (e.g. Greek letters, emoji) — so `inπ`/`πin` is not mistaken for a
// bare `in` keyword.
func isSwiftWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b >= 0x80
}

// swiftConditionParenPositions returns the byte offsets at which to insert the
// opening and closing parenthesis for a condition that begins right after a
// control-flow keyword ending at keywordEnd. It returns ok=false when no plausible
// top-level body brace is found.
func swiftConditionParenPositions(source []byte, keywordEnd uint32) (lParen, rParen uint32, ok bool) {
	condStart := swiftSkipHorizontalAndNewlineSpace(source, keywordEnd)
	bodyOpen, found := swiftFindConditionBodyBrace(source, condStart)
	if !found {
		return 0, 0, false
	}
	rp := bodyOpen
	for rp > condStart {
		switch source[rp-1] {
		case ' ', '\t', '\n', '\r':
			rp--
			continue
		}
		break
	}
	if condStart >= rp {
		return 0, 0, false
	}
	// Already fully parenthesised (`if (cond) {`): no need to inject.
	if source[condStart] == '(' && source[rp-1] == ')' {
		return 0, 0, false
	}
	return condStart, rp, true
}

func swiftSkipHorizontalAndNewlineSpace(source []byte, start uint32) uint32 {
	i := start
	for i < uint32(len(source)) {
		switch source[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// swiftFindConditionBodyBrace scans forward from start to the first `{` at
// bracket depth zero, skipping line/block comments and string literals (single
// and multiline) and counting (), [] nesting. Returns false if a `}`/`;` is
// reached at depth zero first (no body brace).
func swiftFindConditionBodyBrace(source []byte, start uint32) (uint32, bool) {
	depth := 0
	i := start
	n := uint32(len(source))
	for i < n {
		b := source[i]
		// Comments.
		if b == '/' && i+1 < n {
			if source[i+1] == '/' {
				i += 2
				for i < n && source[i] != '\n' {
					i++
				}
				continue
			}
			if source[i+1] == '*' {
				// Swift block comments nest: /* outer /* inner */ outer */.
				i += 2
				depthC := 1
				for i+1 < n && depthC > 0 {
					if source[i] == '/' && source[i+1] == '*' {
						depthC++
						i += 2
					} else if source[i] == '*' && source[i+1] == '/' {
						depthC--
						i += 2
					} else {
						i++
					}
				}
				continue
			}
		}
		// Strings.
		if b == '"' {
			if i+2 < n && source[i+1] == '"' && source[i+2] == '"' {
				i += 3
				for i+2 < n && !(source[i] == '"' && source[i+1] == '"' && source[i+2] == '"') {
					if source[i] == '\\' {
						i++
					}
					i++
				}
				i += 3
				continue
			}
			i++
			for i < n && source[i] != '"' {
				if source[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		switch b {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '{':
			if depth == 0 {
				return i, true
			}
		case '}', ';':
			if depth == 0 {
				return 0, false
			}
		}
		i++
	}
	return 0, false
}

func swiftApplyParenInserts(source []byte, inserts []swiftParenInsert) ([]byte, []uint32) {
	// Insertion sort by position (stable); the list is tiny.
	sorted := make([]swiftParenInsert, len(inserts))
	copy(sorted, inserts)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1].pos > sorted[j].pos; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	out := make([]byte, 0, len(source)+len(sorted))
	insTPos := make([]uint32, 0, len(sorted))
	srcIdx := uint32(0)
	for _, ins := range sorted {
		if ins.pos > srcIdx {
			out = append(out, source[srcIdx:ins.pos]...)
			srcIdx = ins.pos
		} else if ins.pos < srcIdx {
			// Overlapping/unsorted guard — should not happen.
			continue
		}
		insTPos = append(insTPos, uint32(len(out)))
		out = append(out, ins.ch)
	}
	out = append(out, source[srcIdx:]...)
	return out, insTPos
}

type swiftRemap struct {
	tsrc   []byte
	insSet map[uint32]bool
	insPos []uint32
	arena  *nodeArena
}

func (r *swiftRemap) isSyntheticParen(n *Node) bool {
	if n == nil || resultChildCount(n) != 0 {
		return false
	}
	if n.endByte != n.startByte+1 || !r.insSet[n.startByte] {
		return false
	}
	c := r.tsrc[n.startByte]
	return c == '(' || c == ')'
}

func (r *swiftRemap) mapByte(t uint32) uint32 {
	c := uint32(0)
	for _, p := range r.insPos {
		if p < t {
			c++
		}
	}
	return t - c
}

// remap clones a node from transformed coordinates into the arena in original
// coordinates, dropping synthetic parens and unwrapping the synthetic
// parenthesised expression. Returns ok=false if a synthetic paren is found
// outside an unwrappable wrapper (so the caller can abort safely).
func (r *swiftRemap) remap(n *Node) (*Node, bool) {
	if n == nil {
		return nil, true
	}
	// Unwrap a wrapper whose first and last children are our synthetic parens
	// (e.g. the tuple_expression the grammar builds around `(cond)`).
	if cc := resultChildCount(n); cc >= 2 {
		first := resultChildAt(n, 0)
		last := resultChildAt(n, cc-1)
		if r.isSyntheticParen(first) && r.isSyntheticParen(last) {
			var inner *Node
			for i := 0; i < cc; i++ {
				c := resultChildAt(n, i)
				if r.isSyntheticParen(c) {
					continue
				}
				if inner != nil {
					return nil, false
				}
				inner = c
			}
			if inner == nil {
				return nil, false
			}
			return r.remap(inner)
		}
	}
	if r.isSyntheticParen(n) {
		return nil, false
	}
	newStart := r.mapByte(n.startByte)
	newEnd := r.mapByte(n.endByte)
	if resultChildCount(n) == 0 {
		leaf := newLeafNodeInArena(r.arena, n.symbol, n.isNamed(), newStart, newEnd, Point{}, Point{})
		leaf.setExtra(n.IsExtra())
		return leaf, true
	}
	kids := make([]*Node, 0, resultChildCount(n))
	for i := 0; i < resultChildCount(n); i++ {
		rc, ok := r.remap(resultChildAt(n, i))
		if !ok {
			return nil, false
		}
		if rc != nil {
			kids = append(kids, rc)
		}
	}
	kids = cloneNodeSliceInArena(r.arena, kids)
	fieldIDs := cloneFieldIDSliceInArena(r.arena, n.fieldIDs())
	node := newParentNodeInArena(r.arena, n.symbol, n.isNamed(), kids, fieldIDs, n.productionID)
	node.setExtra(n.IsExtra())
	node.startByte = newStart
	node.endByte = newEnd
	return node, true
}
