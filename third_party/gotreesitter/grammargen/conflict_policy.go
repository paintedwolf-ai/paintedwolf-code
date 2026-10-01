package grammargen

import (
	"sort"

	"github.com/odvcencio/gotreesitter"
)

func buildConflictPolicies(tables *LRTables, ng *NormalizedGrammar) []gotreesitter.ConflictPolicy {
	if tables == nil || ng == nil || len(tables.ActionTable) == 0 {
		return nil
	}
	cache := getConflictResolutionCache(ng)

	states := make([]int, 0, len(tables.ActionTable))
	for state := range tables.ActionTable {
		states = append(states, state)
	}
	sort.Ints(states)

	var policies []gotreesitter.ConflictPolicy
	for _, state := range states {
		if state < 0 || state >= tables.StateCount {
			continue
		}
		row := tables.ActionTable[state]
		lookaheads := make([]int, 0, len(row))
		for lookahead := range row {
			lookaheads = append(lookaheads, lookahead)
		}
		sort.Ints(lookaheads)
		for _, lookahead := range lookaheads {
			if lookahead < 0 || lookahead >= ng.TokenCount() {
				continue
			}
			policy, ok := conflictPolicyForActionRow(tables, state, lookahead, row[lookahead], ng, cache)
			if ok {
				policies = append(policies, policy)
			}
		}
	}
	return policies
}

func conflictPolicyForActionRow(tables *LRTables, state, lookahead int, actions []lrAction, ng *NormalizedGrammar, cache *conflictResolutionCache) (gotreesitter.ConflictPolicy, bool) {
	if state < 0 || lookahead < 0 || len(actions) < 2 || ng == nil {
		return gotreesitter.ConflictPolicy{}, false
	}

	var shift lrAction
	shiftFound := false
	reduceFound := false
	reduceSymbols := make(map[int]struct{})
	for _, action := range actions {
		switch action.kind {
		case lrShift:
			if shiftFound || action.isExtra {
				return gotreesitter.ConflictPolicy{}, false
			}
			shift = action
			shiftFound = true
		case lrReduce:
			if action.isExtra || action.prodIdx < 0 || action.prodIdx >= len(ng.Productions) {
				return gotreesitter.ConflictPolicy{}, false
			}
			reduceFound = true
		default:
			return gotreesitter.ConflictPolicy{}, false
		}
	}
	if !shiftFound || !reduceFound {
		return gotreesitter.ConflictPolicy{}, false
	}
	policyKind := gotreesitter.ConflictPolicyRepetitionShift
	if !shift.repeat {
		policyKind = gotreesitter.ConflictPolicyShift
	}

	for _, action := range actions {
		if action.kind != lrReduce {
			continue
		}
		lhs := ng.Productions[action.prodIdx].LHS
		if lhs < 0 || lhs >= len(ng.Symbols) {
			return gotreesitter.ConflictPolicy{}, false
		}
		if !reduceHasRepeatPolicyProof(lookahead, action, shift, ng, cache) {
			return gotreesitter.ConflictPolicy{}, false
		}
		reduceSymbols[lhs] = struct{}{}
	}

	symbols := make([]int, 0, len(reduceSymbols))
	for sym := range reduceSymbols {
		symbols = append(symbols, sym)
	}
	sort.Ints(symbols)
	reduceSyms := make([]gotreesitter.Symbol, len(symbols))
	for i, sym := range symbols {
		reduceSyms[i] = gotreesitter.Symbol(sym)
	}

	return gotreesitter.ConflictPolicy{
		State:         gotreesitter.StateID(state + 1),
		Lookahead:     gotreesitter.Symbol(lookahead),
		Kind:          policyKind,
		ReduceSymbols: reduceSyms,
	}, true
}

func reduceHasRepeatPolicyProof(lookahead int, reduce, shift lrAction, ng *NormalizedGrammar, cache *conflictResolutionCache) bool {
	if reduce.kind != lrReduce || reduce.prodIdx < 0 || reduce.prodIdx >= len(ng.Productions) {
		return false
	}
	prod := &ng.Productions[reduce.prodIdx]
	if prod.LHS < 0 || prod.LHS >= len(ng.Symbols) {
		return false
	}
	if isStructurallyGeneratedRepeatHelper(prod.LHS, ng, cache) {
		return repeatHelperReduceContinuesWithShift(lookahead, reduce, shift, ng, cache) &&
			!symbolParentSuffixMayConsumeLookahead(prod.LHS, lookahead, ng, cache)
	}
	repeatLHS, ok := singleGeneratedRepeatWrapperHelper(prod, ng, cache)
	if !ok {
		return false
	}
	return repeatHelperWrapperContinuesWithShift(repeatLHS, lookahead, shift, ng, cache) &&
		!symbolParentSuffixMayConsumeLookahead(prod.LHS, lookahead, ng, cache) &&
		!symbolParentSuffixMayConsumeLookahead(repeatLHS, lookahead, ng, cache)
}

func singleGeneratedRepeatWrapperHelper(prod *Production, ng *NormalizedGrammar, cache *conflictResolutionCache) (int, bool) {
	if prod == nil || len(prod.RHS) != 1 {
		return 0, false
	}
	repeatLHS := prod.RHS[0]
	if !isStructurallyGeneratedRepeatHelper(repeatLHS, ng, cache) {
		return 0, false
	}
	return repeatLHS, true
}

func repeatHelperWrapperContinuesWithShift(repeatLHS, lookahead int, shift lrAction, ng *NormalizedGrammar, cache *conflictResolutionCache) bool {
	if repeatLHS < 0 || repeatLHS >= len(cache.prodsByLHS) ||
		lookahead < 0 || lookahead >= ng.TokenCount() ||
		!shiftTargetsGeneratedRepeatHelper(shift, repeatLHS) ||
		!isStructurallyGeneratedRepeatHelper(repeatLHS, ng, cache) {
		return false
	}
	return repeatHelperTailCanBeginWithLookahead(repeatLHS, lookahead, ng, cache)
}

func shiftTargetsGeneratedRepeatHelper(shift lrAction, repeatLHS int) bool {
	if shift.lhsSym == repeatLHS {
		return true
	}
	for _, lhs := range shift.lhsSyms {
		if lhs == repeatLHS {
			return true
		}
	}
	return false
}

func repeatHelperTailCanBeginWithLookahead(repeatLHS, lookahead int, ng *NormalizedGrammar, cache *conflictResolutionCache) bool {
	targets := map[int]bool{lookahead: true}
	for _, prodIdx := range cache.prodsByLHS[repeatLHS] {
		if prodIdx < 0 || prodIdx >= len(ng.Productions) {
			continue
		}
		prod := &ng.Productions[prodIdx]
		if prod.LHS != repeatLHS || len(prod.RHS) < 2 || prod.RHS[0] != repeatLHS {
			continue
		}
		if rhsCanBeginWithAny(prod.RHS[1:], targets, cache, ng) {
			return true
		}
	}
	return false
}

func symbolParentSuffixMayConsumeLookahead(sym, lookahead int, ng *NormalizedGrammar, cache *conflictResolutionCache) bool {
	if cache != nil {
		cache.structuralStats.ParentSuffixLookaheadCalls++
	}
	if ng == nil || cache == nil ||
		sym < 0 || sym >= len(cache.rhsParents) ||
		lookahead < 0 || lookahead >= ng.TokenCount() {
		return false
	}

	for _, parentLHS := range cache.rhsParents[sym] {
		if parentLHS == sym || parentLHS < 0 || parentLHS >= len(cache.prodsByLHS) {
			continue
		}
		for _, prodIdx := range cache.prodsByLHS[parentLHS] {
			if prodIdx < 0 || prodIdx >= len(ng.Productions) {
				continue
			}
			parent := &ng.Productions[prodIdx]
			for i, rhsSym := range parent.RHS {
				if rhsSym != sym {
					continue
				}
				if rhsCanBeginWithAny(parent.RHS[i+1:], map[int]bool{lookahead: true}, cache, ng) {
					return true
				}
			}
		}
	}
	return false
}
