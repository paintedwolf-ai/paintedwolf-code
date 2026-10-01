package gotreesitter

// ParseState-by-table-replay (Phase-3 Lane 2).
//
// The compact parser core (internal/parsercorephase0) can build a tree at
// 0.96-1.01x C work, but it drops the per-node parseState / preGotoState that
// production consumers (incremental reuse, error recovery, queries, cursors)
// require. Recording those two StateIDs per node during the compact parse
// reintroduces the metadata cost the compact route exists to avoid.
//
// The Phase-3 insight is that parseState is TABLE-DERIVABLE: for any node its
// (preGotoState, parseState) pair can be reconstructed top-down by replaying
// the LR goto/shift tables from the parent's exposed state over the children's
// symbols. This file implements that reconstruction as a single push-shaped
// top-down pass (no pull-per-edge). Replaying over the VISIBLE, materialized
// tree reconstructs only ~0.30% of nodes (parsestate_replay_diff_test.go): the
// visible tree has already erased the two things replay needs -- hidden-node
// goto transitions and pre-alias grammar symbols. Admission-mode reconstruction
// therefore runs over the FULL compact derivation instead
// (parsestate_replay_compact.go, before hidden elision and aliasing), where the
// same push-shaped traversal is exact except for the documented extra-preGoto
// and internal collapse-class residuals; see the NOTE at the end of this file.
//
// The model, derived from the production assignments in parser_reduce.go:
//
//   * A leaf (terminal) shifted from state s onto the stack gets
//         leaf.preGotoState = s
//         leaf.parseState   = extraShiftTargetState(s, shiftAction(s, sym))
//     where shiftAction(s, sym) is the SHIFT action for sym in state s. For a
//     normal shift that is the action's target State; for an extra shift whose
//     action carries State==0 it stays at s (extraShiftTargetState).
//
//   * A non-leaf produced by reducing production A -> c0 c1 ... ck exposes the
//     state topState below the popped children and gets
//         parent.preGotoState = topState
//         parent.parseState   = lookupGoto(topState, A)  (or topState if 0)
//
//   * The children of a reduce form a state chain: c0 was pushed onto topState
//     (= parent.preGotoState), and each subsequent ci was pushed onto the state
//     exposed by c(i-1). Hence
//         c0.preGotoState = parent.preGotoState
//         ci.preGotoState = c(i-1).parseState        (i > 0)
//         ci.parseState   = advance(ci.preGotoState, ci)
//
// The root of the tree is the start-symbol node produced by the final reduce;
// the state below it is the parser's InitialState, so the whole tree is seeded
// from replayRootPreGotoState(p).
//
// advance() is total: terminals use the shift table, non-terminals the goto
// table, and the "no transition" fall-through keeps the current state (matching
// the extra / no-goto production branches). Node classes whose production state
// is NOT a plain shift/goto of the visible symbol (error/missing recovery
// insertions, and any node whose real derivation differs from its visible
// shape) are the reconstruction's trouble spots; the differential harness
// measures exactly which nodes those are.

// replayVisit is invoked once per node during a top-down replay pass with the
// reconstructed preGotoState and parseState for that node. It must not mutate
// tree structure; production materialization writes the two states onto the
// node, and the differential test compares them against recorded values.
type replayVisit func(n *Node, preGotoState, parseState StateID)

// replayRootPreGotoState returns the state exposed below the tree root, i.e.
// the seed for the whole top-down replay. It is the parser's initial state:
// after the final reduce to the start symbol pops that production's children,
// the state left on the stack is InitialState.
func (p *Parser) replayRootPreGotoState() StateID {
	if p == nil || p.language == nil {
		return 0
	}
	return p.language.InitialState
}

// replaySymbolIsTerminal reports whether sym is a terminal (lexer) symbol, i.e.
// one that reaches the stack via a SHIFT rather than a reduce GOTO. Symbols
// below TokenCount are terminals; the rest are non-terminals. The synthetic
// errorSymbol is treated as a non-terminal (it never shifts).
func (p *Parser) replaySymbolIsTerminal(sym Symbol) bool {
	if p == nil || p.language == nil {
		return false
	}
	if sym == errorSymbol {
		return false
	}
	tc := p.language.TokenCount
	return tc == 0 || uint32(sym) < tc
}

// replayShiftTarget computes the state a terminal leaf with symbol sym reaches
// when shifted from state s, mirroring applyShiftAction: it finds the SHIFT
// action for sym at s and applies extraShiftTargetState. When no shift action
// exists (the leaf did not arrive by a plain shift from this state) it returns
// (s, false) so callers can flag the node as non-replayable.
func (p *Parser) replayShiftTarget(s StateID, sym Symbol) (StateID, bool) {
	entry := p.lookupAction(s, sym)
	if entry == nil {
		return s, false
	}
	for i := range entry.Actions {
		act := entry.Actions[i]
		if act.Type != ParseActionShift {
			continue
		}
		return extraShiftTargetState(s, act), true
	}
	return s, false
}

// replayTransition computes the parser state reached after a symbol is pushed
// onto the stack from state s. terminal selects the shift table (lexer tokens)
// vs the goto table (reduce results). The bool reports whether a table
// transition was found; false falls back to s (the extra / no-goto branch,
// valid only when the recorded parseState also equals preGotoState). This is
// the shared primitive: visible-tree replay guesses terminal from TokenCount,
// while compact-derivation replay uses the record's authoritative terminal
// flag and its pre-alias grammar symbol.
func (p *Parser) replayTransition(s StateID, sym Symbol, terminal bool) (StateID, bool) {
	if terminal {
		return p.replayShiftTarget(s, sym)
	}
	g := p.lookupGoto(s, sym)
	if g != 0 {
		return g, true
	}
	return s, false
}

// replayAdvance computes the parser state after node n (whose preGotoState is
// s) is pushed onto the stack. The bool result reports whether the transition
// was found in the tables; false means the node's state is not a plain
// shift/goto of its visible symbol from s (a trouble-spot node).
func (p *Parser) replayAdvance(s StateID, n *Node) (StateID, bool) {
	if n == nil {
		return s, false
	}
	return p.replayTransition(s, n.symbol, p.replaySymbolIsTerminal(n.symbol))
}

// replayFrame is one entry on the explicit worklist used to avoid deep Go
// recursion on pathologically deep trees (e.g. long left-recursive chains).
type replayFrame struct {
	node    *Node
	preGoto StateID
}

// replayParseStates reconstructs (preGotoState, parseState) for every node in
// the tree rooted at root and reports each via visit. rootPreGoto seeds the
// root; use replayRootPreGotoState for the production seed.
//
// The traversal is push-shaped: a single top-down sweep in which each node's
// state depends only on its preGotoState and its own symbol, and the sibling
// state chain is threaded left-to-right. Every node and edge is touched once
// (the parent computes each child's preGotoState directly, no per-edge pull),
// giving O(nodes) work with sequential access. scratch, if non-nil, is reused
// as the worklist backing store to keep the pass allocation-free across calls.
func (p *Parser) replayParseStates(root *Node, rootPreGoto StateID, visit replayVisit, scratch *[]replayFrame) {
	if p == nil || root == nil || visit == nil {
		return
	}
	var stack []replayFrame
	if scratch != nil {
		stack = (*scratch)[:0]
	}
	stack = append(stack, replayFrame{node: root, preGoto: rootPreGoto})
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := top.node
		ps, _ := p.replayAdvance(top.preGoto, n)
		visit(n, top.preGoto, ps)
		// Thread the sibling state chain: the first child's preGotoState is
		// this node's preGotoState (the state exposed below its reduce), and
		// each subsequent child's preGotoState is the previous child's
		// parseState. Push children in reverse so the leftmost is processed
		// first (deterministic pre-order for the differential report).
		kids := n.children
		if len(kids) == 0 {
			continue
		}
		if len(kids) == 1 {
			stack = append(stack, replayFrame{node: kids[0], preGoto: top.preGoto})
			continue
		}
		// Precompute each child's preGotoState left-to-right, then push in
		// reverse. base indexes the appended child frames.
		base := len(stack)
		cursor := top.preGoto
		for _, c := range kids {
			stack = append(stack, replayFrame{node: c, preGoto: cursor})
			cursor, _ = p.replayAdvance(cursor, c)
		}
		// Reverse the just-appended frames so pop order is left-to-right.
		for i, j := base, len(stack)-1; i < j; i, j = i+1, j-1 {
			stack[i], stack[j] = stack[j], stack[i]
		}
	}
	if scratch != nil {
		*scratch = stack[:0]
	}
}

// NOTE: there is deliberately no "stamp states onto a visible tree" helper.
// The differential harness (parsestate_replay_diff_test.go) proves that the
// visible, materialized tree has already lost the information replay needs --
// hidden-node goto transitions and pre-alias grammar symbols -- so replaying
// over it reconstructs only ~0.3% of nodes. Admission-mode reconstruction runs
// over the FULL compact derivation instead (parsestate_replay_compact.go),
// before hidden elision and aliasing; replayParseStates above is retained as
// the shared push-shaped traversal used by that differential proof.
