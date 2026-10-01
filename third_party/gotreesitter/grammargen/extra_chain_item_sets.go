package grammargen

import (
	"sort"
	"strings"
)

// useLALRNonterminalExtraStates identifies the recursive heredoc shape that
// makes merge-history chain construction non-convergent: a nonterminal extra
// named heredoc_body whose production graph reaches interpolation (and through
// it, in Ruby and Crystal, the ordinary statement grammar). Other extras keep
// the legacy builder, which is materially faster for large directive grammars
// such as C# and already has stable fleet parity receipts.
func useLALRNonterminalExtraStates(ng *NormalizedGrammar) bool {
	if ng == nil {
		return false
	}
	tokenCount := ng.TokenCount()
	heredocBody := -1
	interpolation := -1
	for symbolID, symbol := range ng.Symbols {
		switch symbol.Name {
		case "heredoc_body":
			heredocBody = symbolID
		case "interpolation":
			interpolation = symbolID
		}
	}
	if heredocBody < tokenCount || interpolation < tokenCount {
		return false
	}
	isExtra := false
	for _, symbolID := range ng.ExtraSymbols {
		if symbolID == heredocBody {
			isExtra = true
			break
		}
	}
	if !isExtra {
		return false
	}

	productionsByLHS := make(map[int][]int)
	for productionID := range ng.Productions {
		lhs := ng.Productions[productionID].LHS
		productionsByLHS[lhs] = append(productionsByLHS[lhs], productionID)
	}
	seen := map[int]bool{heredocBody: true}
	queue := []int{heredocBody}
	for len(queue) > 0 {
		symbolID := queue[0]
		queue = queue[1:]
		for _, productionID := range productionsByLHS[symbolID] {
			for _, child := range ng.Productions[productionID].RHS {
				if child == interpolation {
					return true
				}
				if child < tokenCount || seen[child] {
					continue
				}
				seen[child] = true
				queue = append(queue, child)
			}
		}
	}
	return false
}

// buildLALRNonterminalExtraStates builds the dedicated parser states for
// nonterminal extras as LALR item sets. Tree-sitter seeds one item set per first
// terminal, advances the root-extra production past that terminal, and uses the
// end-of-extra sentinel (rendered as EOF/no-lookahead) as the root lookahead.
// Representing the whole closure as an item set is load-bearing: pairwise
// graph-state unions encode merge history rather than parser state and can grow
// combinatorially for extras that re-enter a full statement grammar.
//
// Upstream constructs canonical LR(1) states and then merges compatible states
// with identical LR(0) cores. Ruby's heredoc grammar creates 91,597 canonical
// states before that optimization and 5,989 states afterward, so materializing
// the canonical graph is itself an unacceptable memory spike. This builder
// performs the core merge during construction instead: lookaheads are unioned
// into an existing core and propagated to a fixed point through a worklist.
//
// The returned map contains global parser-state IDs keyed by each extra's first
// terminal. The new states and their actions/gotos are appended to tables.
func buildLALRNonterminalExtraStates(
	tables *LRTables,
	ng *NormalizedGrammar,
	ctx *lrContext,
	extraStartsByFirstSym map[int][]int,
	extraFirstSyms []int,
) map[int]int {
	if tables == nil || ng == nil || ctx == nil || len(extraFirstSyms) == 0 {
		return nil
	}

	tokenCount := ng.TokenCount()
	mainStateCount := tables.StateCount
	budget := extraChainSyntheticStateBudget(mainStateCount)
	entryLabel := extraItemSetEntryLabel(ng, extraStartsByFirstSym)

	type coreHashEntry struct {
		state int
		next  *coreHashEntry
	}
	coreMap := make(map[uint64]*coreHashEntry)
	itemSets := make([]lrItemSet, 0, minInt(len(extraFirstSyms)*8, budget))
	transitions := make([]map[int]int, 0, cap(itemSets))
	worklist := make([]int, 0, cap(itemSets))
	inWorklist := make([]bool, 0, cap(itemSets))

	findOrMerge := func(set *lrItemSet) int {
		for entry := coreMap[set.coreHash]; entry != nil; entry = entry.next {
			existing := &itemSets[entry.state]
			if !sameCoresUsingIndexed(existing, set) {
				continue
			}

			newEntries := make([]coreEntry, 0)
			for _, incoming := range set.cores {
				existingIdx, ok := existing.coreLookup(int(incoming.prodIdx), int(incoming.dot))
				if !ok {
					newEntries = append(newEntries, incoming)
					continue
				}
				current := &existing.cores[existingIdx].lookaheads
				for wordIdx, word := range incoming.lookaheads.words {
					if (wordIdx >= len(current.words) && word != 0) ||
						(wordIdx < len(current.words) && word&^current.words[wordIdx] != 0) {
						newEntries = append(newEntries, incoming)
						break
					}
				}
			}
			if len(newEntries) > 0 {
				ctx.closureIncremental(existing, newEntries)
				if !inWorklist[entry.state] {
					worklist = append(worklist, entry.state)
					inWorklist[entry.state] = true
				}
			}
			ctx.recycleItemSetLookaheads(set)
			return entry.state
		}
		if len(itemSets) >= budget {
			panic(&ExtraChainSyntheticStateBudgetError{
				Grammar:        ng.GrammarName,
				Symbol:         entryLabel,
				SyntheticCount: len(itemSets),
				Budget:         budget,
				MainStateCount: mainStateCount,
			})
		}
		state := len(itemSets)
		itemSets = append(itemSets, *set)
		transitions = append(transitions, make(map[int]int))
		inWorklist = append(inWorklist, true)
		coreMap[set.coreHash] = &coreHashEntry{state: state, next: coreMap[set.coreHash]}
		worklist = append(worklist, state)
		return state
	}

	endOfExtra := newBitset(tokenCount)
	endOfExtra.add(0)
	entryLocal := make(map[int]int, len(extraFirstSyms))
	sortedFirstSyms := append([]int(nil), extraFirstSyms...)
	sort.Ints(sortedFirstSyms)
	for _, firstSym := range sortedFirstSyms {
		prodIdxs := extraStartsByFirstSym[firstSym]
		kernel := make([]coreEntry, 0, len(prodIdxs))
		for _, prodIdx := range prodIdxs {
			if prodIdx < 0 || prodIdx >= len(ng.Productions) {
				continue
			}
			prod := &ng.Productions[prodIdx]
			if len(prod.RHS) == 0 || prod.RHS[0] != firstSym {
				continue
			}
			kernel = append(kernel, coreEntry{
				prodIdx:    uint32(prodIdx),
				dot:        1,
				lookaheads: endOfExtra,
			})
		}
		if len(kernel) == 0 {
			continue
		}
		closed := ctx.closureToSet(kernel)
		entryLocal[firstSym] = findOrMerge(&closed)
	}

	for len(worklist) > 0 {
		state := worklist[0]
		worklist = worklist[1:]
		inWorklist[state] = false
		set := &itemSets[state]
		symsSeen := make(map[int]struct{})
		var syms []int
		for _, item := range set.cores {
			prod := &ng.Productions[int(item.prodIdx)]
			if int(item.dot) >= len(prod.RHS) {
				continue
			}
			sym := prod.RHS[item.dot]
			if _, ok := symsSeen[sym]; ok {
				continue
			}
			symsSeen[sym] = struct{}{}
			syms = append(syms, sym)
		}
		sort.Ints(syms)
		for _, sym := range syms {
			// findOrMerge can append to itemSets and move its backing array, so
			// reacquire the source set for each transition.
			source := &itemSets[state]
			advanced := make([]coreEntry, 0, len(source.cores))
			for _, item := range source.cores {
				prod := &ng.Productions[int(item.prodIdx)]
				if int(item.dot) >= len(prod.RHS) || prod.RHS[item.dot] != sym {
					continue
				}
				advanced = append(advanced, coreEntry{
					prodIdx:    item.prodIdx,
					dot:        item.dot + 1,
					lookaheads: item.lookaheads,
				})
			}
			if len(advanced) == 0 {
				continue
			}
			closed := ctx.closureToSet(advanced)
			transitions[state][sym] = findOrMerge(&closed)
		}
	}

	if len(itemSets) == 0 {
		return nil
	}
	tables.ExtraChainStateStart = mainStateCount
	for local := range itemSets {
		global := mainStateCount + local
		tables.ActionTable[global] = make(map[int][]lrAction)
		tables.GotoTable[global] = make(map[int]int)
	}
	for local, set := range itemSets {
		global := mainStateCount + local
		for _, item := range set.cores {
			prodIdx := int(item.prodIdx)
			prod := &ng.Productions[prodIdx]
			if int(item.dot) < len(prod.RHS) {
				sym := prod.RHS[item.dot]
				targetLocal, ok := transitions[local][sym]
				if !ok {
					continue
				}
				targetGlobal := mainStateCount + targetLocal
				if sym < tokenCount {
					action := lrAction{
						kind:    lrShift,
						state:   targetGlobal,
						prec:    prod.Prec,
						hasPrec: prod.HasExplicitPrec,
						assoc:   prod.Assoc,
						lhsSym:  prod.LHS,
					}
					for _, lhs := range extraItemSetRepeatShiftLHSSyms(ctx, &set, &itemSets[targetLocal], sym) {
						action.addRepeatLHS(lhs)
					}
					tables.addAction(global, sym, action)
				} else {
					tables.GotoTable[global][sym] = targetGlobal
				}
				continue
			}

			item.lookaheads.forEach(func(lookahead int) {
				tables.addAction(global, lookahead, lrAction{
					kind:    lrReduce,
					prodIdx: prodIdx,
					prec:    prod.Prec,
					hasPrec: prod.HasExplicitPrec,
					assoc:   prod.Assoc,
					lhsSym:  prod.LHS,
					isExtra: prod.IsExtra,
				})
			})
		}
	}
	tables.StateCount = mainStateCount + len(itemSets)

	entryGlobal := make(map[int]int, len(entryLocal))
	for firstSym, local := range entryLocal {
		entryGlobal[firstSym] = mainStateCount + local
	}
	return entryGlobal
}

func extraItemSetRepeatShiftLHSSyms(ctx *lrContext, source, target *lrItemSet, sym int) []int {
	if ctx == nil || source == nil || target == nil {
		return nil
	}
	var result []int
	for _, lhs := range ctx.completedRepeatWrapperLHSSymsAcrossTransitions(target, sym, true) {
		if ctx.stateHasRecursiveRepeatSource(source, lhs) {
			result = append(result, lhs)
		}
	}
	return result
}

func extraItemSetEntryLabel(ng *NormalizedGrammar, starts map[int][]int) string {
	if ng == nil {
		return "<unknown>"
	}
	seen := make(map[string]struct{})
	for _, prodIdxs := range starts {
		for _, prodIdx := range prodIdxs {
			if prodIdx < 0 || prodIdx >= len(ng.Productions) {
				continue
			}
			lhs := ng.Productions[prodIdx].LHS
			if lhs < 0 || lhs >= len(ng.Symbols) {
				continue
			}
			seen[ng.Symbols[lhs].Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "<unknown>"
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
