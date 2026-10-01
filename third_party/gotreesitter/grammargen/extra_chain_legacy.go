package grammargen

import (
	"fmt"
	"sort"
	"strings"
)

// The legacy chain builder remains the default for non-pathological extras.
// It is substantially faster for large directive-style grammars such as C#.
// Recursive heredoc/interpolation extras use the bounded LALR item-set path.
type extraChainBuilder struct {
	tables          *LRTables
	ng              *NormalizedGrammar
	ctx             *lrContext
	tokenCount      int
	syntheticStart  int
	terminalExtras  []int
	chainStateCache map[string]int
	entryStateCache map[string]int
	entrySeen       map[string]bool
	unionStateCache map[string]int

	// syntheticStateBudget and currentEntryLabel back the defense-in-depth
	// hard-fail in newState; see ExtraChainSyntheticStateBudgetError.
	syntheticStateBudget int
	currentEntryLabel    string
}

func newExtraChainBuilder(tables *LRTables, ng *NormalizedGrammar, ctx *lrContext, terminalExtras []int) *extraChainBuilder {
	return &extraChainBuilder{
		tables:               tables,
		ng:                   ng,
		ctx:                  ctx,
		tokenCount:           ng.TokenCount(),
		syntheticStart:       tables.StateCount,
		terminalExtras:       terminalExtras,
		chainStateCache:      make(map[string]int),
		entryStateCache:      make(map[string]int),
		entrySeen:            make(map[string]bool),
		unionStateCache:      make(map[string]int),
		syntheticStateBudget: extraChainSyntheticStateBudget(tables.StateCount),
	}
}

// extraEntryLabel returns a human-readable name for the nonterminal-extra
// symbol(s) whose chain construction begins with the productions in
// prodIdxs, for ExtraChainSyntheticStateBudgetError diagnostics.
func (b *extraChainBuilder) extraEntryLabel(prodIdxs []int) string {
	seen := make(map[string]struct{}, len(prodIdxs))
	var names []string
	for _, prodIdx := range prodIdxs {
		if prodIdx < 0 || prodIdx >= len(b.ng.Productions) {
			continue
		}
		lhs := b.ng.Productions[prodIdx].LHS
		name := ""
		if lhs >= 0 && lhs < len(b.ng.Symbols) {
			name = b.ng.Symbols[lhs].Name
		}
		if name == "" {
			name = fmt.Sprintf("<symbol %d>", lhs)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "<unknown>"
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func (b *extraChainBuilder) newState() int {
	if syntheticCount := b.tables.StateCount - b.syntheticStart; syntheticCount >= b.syntheticStateBudget {
		panic(&ExtraChainSyntheticStateBudgetError{
			Grammar:        b.ng.GrammarName,
			Symbol:         b.currentEntryLabel,
			SyntheticCount: syntheticCount,
			Budget:         b.syntheticStateBudget,
			MainStateCount: b.syntheticStart,
		})
	}
	stateIdx := b.tables.StateCount
	b.tables.StateCount++
	b.tables.ActionTable[stateIdx] = make(map[int][]lrAction)
	b.tables.GotoTable[stateIdx] = make(map[int]int)
	return stateIdx
}

func (b *extraChainBuilder) finalizeState(stateIdx int) {
	// Synthetic states for nonterminal extras model the interior of that extra
	// production. Do not inject the grammar's terminal extras here: allowing
	// unrelated extras mid-chain lets zero-width/layout extras interrupt
	// constructs like block comments immediately after their opener.
	_ = stateIdx
}

func (b *extraChainBuilder) mergeSyntheticTerminalShift(stateIdx, sym int, action lrAction) {
	acts := b.tables.ActionTable[stateIdx][sym]
	mergedTarget := action.state
	mergeIdx := -1
	for i, act := range acts {
		if act.kind != lrShift || !act.isExtra || act.lhsSym != action.lhsSym {
			continue
		}
		if act.state == action.state {
			acts[i].addRepeatLHSFrom(action)
			b.tables.ActionTable[stateIdx][sym] = acts
			return
		}
		if act.state >= b.syntheticStart && action.state >= b.syntheticStart {
			mergedTarget = b.unionSyntheticStates(act.state, mergedTarget)
			if mergeIdx < 0 {
				mergeIdx = i
			}
		}
	}
	if mergeIdx >= 0 {
		acts[mergeIdx].state = mergedTarget
		acts[mergeIdx].addRepeatLHSFrom(action)
		b.tables.ActionTable[stateIdx][sym] = acts
		return
	}
	b.tables.addAction(stateIdx, sym, action)
}

func extraChainStateKey(a, b int, lookaheads *bitset) string {
	var sb strings.Builder
	sb.Grow(32 + len(lookaheads.words)*17)
	fmt.Fprintf(&sb, "%d:%d", a, b)
	for _, w := range lookaheads.words {
		fmt.Fprintf(&sb, ":%x", w)
	}
	return sb.String()
}

func (b *extraChainBuilder) buildProdChain(prodIdx, pos int, follow bitset) int {
	key := extraChainStateKey(prodIdx, pos, &follow)
	if stateIdx, ok := b.chainStateCache[key]; ok {
		return stateIdx
	}

	stateIdx := b.newState()
	b.chainStateCache[key] = stateIdx
	b.addProdContinuation(stateIdx, prodIdx, pos, follow)
	b.finalizeState(stateIdx)
	return stateIdx
}

func (b *extraChainBuilder) buildEntryState(firstSym int, prodIdxs []int, follow bitset) int {
	key := extraChainStateKey(-(firstSym + 1), 0, &follow)
	if stateIdx, ok := b.entryStateCache[key]; ok {
		return stateIdx
	}

	// Track the nonterminal-extra symbol(s) this entry expands, so a budget
	// hard-fail deep in the recursive chain construction below can name the
	// offending extra (see ExtraChainSyntheticStateBudgetError). Every state
	// minted until the next buildEntryState call belongs to this entry's
	// closure - buildProdChain/addNonterminalEntries/unionSyntheticStates only
	// recurse synchronously from here.
	b.currentEntryLabel = b.extraEntryLabel(prodIdxs)
	stateIdx := b.newState()
	b.entryStateCache[key] = stateIdx
	for _, prodIdx := range prodIdxs {
		b.addProdContinuation(stateIdx, prodIdx, 1, follow)
	}
	b.finalizeState(stateIdx)
	return stateIdx
}

func (b *extraChainBuilder) unionSyntheticStates(a, c int) int {
	if a == c || a < b.syntheticStart || c < b.syntheticStart {
		return a
	}
	if a > c {
		a, c = c, a
	}
	key := fmt.Sprintf("%d:%d", a, c)
	if stateIdx, ok := b.unionStateCache[key]; ok {
		return stateIdx
	}

	stateIdx := b.newState()
	b.unionStateCache[key] = stateIdx
	for _, src := range []int{a, c} {
		if srcActions, ok := b.tables.ActionTable[src]; ok {
			syms := make([]int, 0, len(srcActions))
			for sym := range srcActions {
				syms = append(syms, sym)
			}
			sort.Ints(syms)
			for _, sym := range syms {
				for _, act := range srcActions[sym] {
					if act.kind == lrShift && act.isExtra && sym < b.tokenCount {
						b.mergeSyntheticTerminalShift(stateIdx, sym, act)
						continue
					}
					b.tables.addAction(stateIdx, sym, act)
				}
			}
		}
		if srcGotos, ok := b.tables.GotoTable[src]; ok {
			for sym, target := range srcGotos {
				existing, ok := b.tables.GotoTable[stateIdx][sym]
				if !ok || existing == target {
					b.tables.GotoTable[stateIdx][sym] = target
					continue
				}
				if existing >= b.syntheticStart && target >= b.syntheticStart {
					b.tables.GotoTable[stateIdx][sym] = b.unionSyntheticStates(existing, target)
					continue
				}
			}
		}
	}
	b.finalizeState(stateIdx)
	return stateIdx
}

func (b *extraChainBuilder) addProdContinuation(stateIdx, prodIdx, pos int, follow bitset) {
	prod := &b.ng.Productions[prodIdx]
	if pos >= len(prod.RHS) {
		follow.forEach(func(la int) {
			b.tables.addAction(stateIdx, la, lrAction{
				kind:    lrReduce,
				prodIdx: prodIdx,
				prec:    prod.Prec,
				hasPrec: prod.HasExplicitPrec,
				assoc:   prod.Assoc,
				lhsSym:  prod.LHS,
				isExtra: prod.IsExtra,
			})
		})
		return
	}

	nextSym := prod.RHS[pos]
	if nextSym < b.tokenCount {
		targetState := b.buildProdChain(prodIdx, pos+1, follow)
		repeatLHSs := b.ctx.repetitionShiftHelperLHSSyms(stateIdx, nextSym, targetState)
		action := lrAction{
			kind:    lrShift,
			state:   targetState,
			prec:    prod.Prec,
			hasPrec: prod.HasExplicitPrec,
			assoc:   prod.Assoc,
			lhsSym:  prod.LHS,
			isExtra: false,
		}
		for _, lhs := range repeatLHSs {
			action.addRepeatLHS(lhs)
		}
		b.mergeSyntheticTerminalShift(stateIdx, nextSym, action)
		return
	}

	targetState := b.buildProdChain(prodIdx, pos+1, follow)
	existing, ok := b.tables.GotoTable[stateIdx][nextSym]
	if !ok || existing == targetState {
		b.tables.GotoTable[stateIdx][nextSym] = targetState
	} else if existing >= b.syntheticStart && targetState >= b.syntheticStart {
		b.tables.GotoTable[stateIdx][nextSym] = b.unionSyntheticStates(existing, targetState)
	}
	nextFollow := b.ctx.firstOfSequenceWithFallback(prod.RHS[pos+1:], &follow)
	b.addNonterminalEntries(stateIdx, nextSym, nextFollow)
}

func (b *extraChainBuilder) addNonterminalEntries(stateIdx, sym int, follow bitset) {
	key := extraChainStateKey(stateIdx, sym, &follow)
	if b.entrySeen[key] {
		return
	}
	b.entrySeen[key] = true

	for _, prodIdx := range b.ctx.prodsByLHS[sym] {
		prod := &b.ng.Productions[prodIdx]
		if len(prod.RHS) == 0 {
			follow.forEach(func(la int) {
				b.tables.addAction(stateIdx, la, lrAction{
					kind:    lrReduce,
					prodIdx: prodIdx,
					prec:    prod.Prec,
					hasPrec: prod.HasExplicitPrec,
					assoc:   prod.Assoc,
					lhsSym:  prod.LHS,
					isExtra: prod.IsExtra,
				})
			})
			continue
		}

		firstSym := prod.RHS[0]
		if firstSym < b.tokenCount {
			targetState := b.buildProdChain(prodIdx, 1, follow)
			repeatLHSs := b.ctx.repetitionShiftHelperLHSSyms(stateIdx, firstSym, targetState)
			action := lrAction{
				kind:    lrShift,
				state:   targetState,
				prec:    prod.Prec,
				hasPrec: prod.HasExplicitPrec,
				assoc:   prod.Assoc,
				lhsSym:  prod.LHS,
				isExtra: false,
			}
			for _, lhs := range repeatLHSs {
				action.addRepeatLHS(lhs)
			}
			b.mergeSyntheticTerminalShift(stateIdx, firstSym, action)
			continue
		}

		targetState := b.buildProdChain(prodIdx, 1, follow)
		existing, ok := b.tables.GotoTable[stateIdx][firstSym]
		if !ok || existing == targetState {
			b.tables.GotoTable[stateIdx][firstSym] = targetState
		} else if existing >= b.syntheticStart && targetState >= b.syntheticStart {
			b.tables.GotoTable[stateIdx][firstSym] = b.unionSyntheticStates(existing, targetState)
		}
		nextFollow := b.ctx.firstOfSequenceWithFallback(prod.RHS[1:], &follow)
		b.addNonterminalEntries(stateIdx, firstSym, nextFollow)
	}
}

func addLegacyNonterminalExtraChains(tables *LRTables, ng *NormalizedGrammar, ctx *lrContext) {
	tokenCount := ng.TokenCount()
	if len(ng.ExtraSymbols) == 0 {
		return
	}

	var extraProds []int
	for i := range ng.Productions {
		if ng.Productions[i].IsExtra && len(ng.Productions[i].RHS) > 0 {
			extraProds = append(extraProds, i)
		}
	}
	if len(extraProds) == 0 {
		return
	}

	mainStateCount := tables.StateCount
	if tables.ExtraChainStateStart < 0 {
		tables.ExtraChainStateStart = mainStateCount
	}

	var terminalExtras []int
	extraSymbolSet := make(map[int]struct{}, len(ng.ExtraSymbols))
	for _, e := range ng.ExtraSymbols {
		extraSymbolSet[e] = struct{}{}
		if e > 0 && e < tokenCount {
			terminalExtras = append(terminalExtras, e)
		}
	}
	externalSymbolSet := make(map[int]struct{}, len(ng.ExternalSymbols))
	for _, sym := range ng.ExternalSymbols {
		externalSymbolSet[sym] = struct{}{}
	}

	extraStartsByFirstSym := make(map[int][]int)
	var extraFirstSyms []int
	hasExternalExtraStart := false
	hasNonExternalExtraStart := false
	for _, prodIdx := range extraProds {
		prod := &ng.Productions[prodIdx]
		if len(prod.RHS) > 0 && prod.RHS[0] < tokenCount {
			firstSym := prod.RHS[0]
			if _, ok := extraStartsByFirstSym[firstSym]; !ok {
				extraFirstSyms = append(extraFirstSyms, firstSym)
				if _, ok := externalSymbolSet[firstSym]; ok {
					hasExternalExtraStart = true
				} else {
					hasNonExternalExtraStart = true
				}
			}
			extraStartsByFirstSym[firstSym] = append(extraStartsByFirstSym[firstSym], prodIdx)
		}
	}
	internalExtraStructuralStarts := make(map[int]struct{})
	for _, prodIdx := range extraProds {
		prod := &ng.Productions[prodIdx]
		if len(prod.RHS) == 0 {
			continue
		}
		_, rootExtraProduction := extraSymbolSet[prod.LHS]
		start := 0
		if rootExtraProduction {
			start = 1
		}
		for pos := start; pos < len(prod.RHS); pos++ {
			sym := prod.RHS[pos]
			if sym > 0 && sym < tokenCount {
				internalExtraStructuralStarts[sym] = struct{}{}
			}
		}
	}
	startMatchers := buildTerminalStartMatchers(ng.Terminals)

	builder := newExtraChainBuilder(tables, ng, ctx, terminalExtras)
	stateFollowSet := func(state int) bitset {
		follow := newBitset(tokenCount)
		follow.add(0)
		if acts, ok := tables.ActionTable[state]; ok {
			for sym, actionList := range acts {
				if sym < tokenCount && len(actionList) > 0 {
					follow.add(sym)
				}
			}
		}
		for _, extraSym := range terminalExtras {
			follow.add(extraSym)
		}
		for _, firstSym := range extraFirstSyms {
			follow.add(firstSym)
		}
		return follow
	}
	var externalExtraFollow bitset
	if hasExternalExtraStart {
		externalExtraFollow = newBitset(tokenCount)
		for state := 0; state < mainStateCount; state++ {
			follow := stateFollowSet(state)
			follow.forEach(func(sym int) {
				externalExtraFollow.add(sym)
			})
		}
	}
	stateHasContinuation := func(state int) bool {
		if acts, ok := tables.ActionTable[state]; ok {
			for _, actionList := range acts {
				for _, act := range actionList {
					if act.kind == lrShift {
						return true
					}
				}
			}
		}
		return len(tables.GotoTable[state]) > 0
	}
	stateOnlyReducesCompletedExtra := func(state int) bool {
		if stateHasContinuation(state) {
			return false
		}
		acts, ok := tables.ActionTable[state]
		if !ok {
			return false
		}
		hasReduce := false
		for _, actionList := range acts {
			for _, act := range actionList {
				if act.kind != lrReduce || !act.isExtra {
					return false
				}
				if act.prodIdx < 0 || act.prodIdx >= len(ng.Productions) {
					return false
				}
				if _, ok := extraSymbolSet[ng.Productions[act.prodIdx].LHS]; !ok {
					return false
				}
				hasReduce = true
			}
		}
		return hasReduce
	}
	syntheticStateMayInjectExtraStart := func(state, firstSym int) bool {
		if state < mainStateCount {
			return true
		}
		if _, ok := internalExtraStructuralStarts[firstSym]; ok {
			// A token that is structural syntax inside an extra chain must not
			// be reinterpreted as a sibling extra while that chain is active.
			// This lets block-comment bodies own tokens such as line-comment
			// openers without disabling normal nested extras with distinct
			// starters.
			return false
		}
		if _, ok := externalSymbolSet[firstSym]; ok {
			// External-scanner extras are context sensitive. Recursively
			// injecting their starts into synthetic extra-chain states can make
			// scanner-driven extras such as Perl POD/heredocs expand without a
			// structural bound, while main LR states still receive the extra
			// entry actions they need.
			return false
		}
		extraMatcher, ok := startMatchers[firstSym]
		if !ok {
			return true
		}
		// Narrow pruning for directive-style extras. Languages like Scala rely
		// on nested comment extras inside synthetic states; the current
		// generation pathology is driven by C#-style preprocessor extras whose
		// starters are all '#'-prefixed and do not meaningfully nest.
		if !terminalStartMatcherHasSingleRune(extraMatcher, '#') {
			return true
		}
		acts, ok := tables.ActionTable[state]
		if !ok {
			return false
		}
		for sym, actionList := range acts {
			if sym <= 0 || sym >= tokenCount {
				continue
			}
			hasStructuralShift := false
			for _, act := range actionList {
				if act.kind == lrShift && !act.isExtra {
					hasStructuralShift = true
					break
				}
			}
			if !hasStructuralShift {
				continue
			}
			if matcher, ok := startMatchers[sym]; !ok || terminalStartMatchersOverlap(extraMatcher, matcher) {
				return true
			}
		}
		return false
	}

	// Iterate over the growing state space so synthetic extra-chain states also
	// gain extra entry shifts. This closes the construction under nesting:
	// once block comments (or other nonterminal extras) can start in a
	// synthetic state, newly created states are revisited later in this loop.
	for state := 0; state < tables.StateCount; state++ {
		if state >= mainStateCount && stateOnlyReducesCompletedExtra(state) {
			continue
		}
		var follow bitset
		if hasNonExternalExtraStart {
			follow = stateFollowSet(state)
		}
		for _, firstSym := range extraFirstSyms {
			if !syntheticStateMayInjectExtraStart(state, firstSym) {
				continue
			}
			hasNonExtraAction := false
			for _, act := range tables.ActionTable[state][firstSym] {
				if !act.isExtra {
					hasNonExtraAction = true
					break
				}
			}
			if hasNonExtraAction {
				continue
			}
			entryFollow := follow
			if _, ok := externalSymbolSet[firstSym]; ok {
				entryFollow = externalExtraFollow
			} else if len(entryFollow.words) == 0 {
				entryFollow = stateFollowSet(state)
			}
			prodIdxs := extraStartsByFirstSym[firstSym]
			entryState := builder.buildEntryState(firstSym, prodIdxs, entryFollow)
			tables.addAction(state, firstSym, lrAction{
				kind:    lrShift,
				state:   entryState,
				lhsSym:  0,
				isExtra: true,
			})
		}
	}
}
