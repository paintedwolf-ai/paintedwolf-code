package grammargen

import (
	"fmt"
	"os"
)

// splitCandidate describes a state that may benefit from LR(1) state splitting.
type splitCandidate struct {
	stateIdx     int
	reason       string
	mergeCount   int
	conflictKind ConflictKind
	lookaheadSym int
	resolution   string
}

// splitOracle analyzes conflict diagnostics and merge provenance to identify
// states where unmerging the LALR state back to canonical LR(1) states would
// resolve or reduce conflicts.
type splitOracle struct {
	conflicts []ConflictDiag
	prov      *mergeProvenance
	tables    *LRTables
	ng        *NormalizedGrammar
}

func newSplitOracle(conflicts []ConflictDiag, prov *mergeProvenance, extras ...interface{}) *splitOracle {
	o := &splitOracle{
		conflicts: conflicts,
		prov:      prov,
	}
	// Accept optional LRTables and NormalizedGrammar for external-token-
	// aware candidate detection. Callers that don't pass these still get
	// the original conflict-based candidate list.
	for i := 0; i+1 < len(extras); i += 2 {
		if t, ok := extras[i].(*LRTables); ok {
			o.tables = t
		}
		if n, ok := extras[i].(*NormalizedGrammar); ok {
			o.ng = n
		}
	}
	for _, x := range extras {
		if t, ok := x.(*LRTables); ok && o.tables == nil {
			o.tables = t
		}
		if n, ok := x.(*NormalizedGrammar); ok && o.ng == nil {
			o.ng = n
		}
	}
	return o
}

// candidates returns states that are split candidates.
// A state is a candidate if:
//  1. It has an unresolved conflict (GLR entry with multiple actions), AND
//  2. It was produced by LALR merging (has merge origins)
//
// Additionally, when LR tables and normalized grammar are available, merged
// states where hidden external symbols have reduce-only actions matching a
// production-based counterpart are flagged. These states arise when LALR
// merging conflates contexts where the external scanner should fire (e.g.
// expression_statement) with contexts where it should not (e.g. jsx_expression).
func (o *splitOracle) candidates() []splitCandidate {
	var result []splitCandidate
	seen := make(map[int]bool)

	for _, c := range o.conflicts {
		if len(c.Actions) <= 1 {
			continue
		}
		reason := ""
		switch c.Resolution {
		case "GLR (multiple actions kept)":
			reason = "unresolved GLR conflict in merged LALR state"
		case "shift wins (default yacc behavior)":
			reason = "default shift resolution in merged LALR state"
		default:
			continue
		}

		isMerged := false
		mc := 0
		if o.prov != nil {
			isMerged = o.prov.isMerged(c.State)
			mc = len(o.prov.origins(c.State))
		}

		if !isMerged {
			continue
		}

		if seen[c.State] {
			continue
		}
		seen[c.State] = true

		result = append(result, splitCandidate{
			stateIdx:     c.State,
			reason:       reason,
			mergeCount:   mc,
			conflictKind: c.Kind,
			lookaheadSym: c.LookaheadSym,
			resolution:   c.Resolution,
		})
	}

	// Add candidates for merged states with hidden external symbol
	// contamination.
	if o.tables != nil && o.ng != nil && o.prov != nil {
		extCandidates := o.externalTokenSplitCandidates()
		for _, ec := range extCandidates {
			if !seen[ec.stateIdx] {
				seen[ec.stateIdx] = true
				result = append(result, ec)
			}
		}
	}

	return result
}

// externalTokenSplitCandidates finds merged states where a hidden external
// symbol has reduce-only actions matching a production-based counterpart.
//
// Also flags heavily-merged states where a hidden external symbol has any
// reduce-only action AND the merge origins came from semantically distinct
// LR(0) cores (kernel hash varies across origins). These are the targets of
// Direction-B LR(1) splitting: states where LALR merge fused contexts that
// LR(1) would have kept apart, and the hidden external scanner action depends
// on that distinction.
func (o *splitOracle) externalTokenSplitCandidates() []splitCandidate {
	if o.tables == nil || o.ng == nil || o.prov == nil {
		return nil
	}

	ng := o.ng
	tokenCount := ng.TokenCount()

	extSymSet := make(map[int]int, len(ng.ExternalSymbols))
	for i, symID := range ng.ExternalSymbols {
		extSymSet[symID] = i
	}

	// Collect hidden external symbols (names starting with '_').
	var hiddenExtSyms []int
	for _, symID := range ng.ExternalSymbols {
		name := ng.Symbols[symID].Name
		if name == "" || name[0] != '_' {
			continue
		}
		hiddenExtSyms = append(hiddenExtSyms, symID)
	}

	type extCpInfo struct {
		extSym int
		cpSyms []int
	}
	var cpInfos []extCpInfo
	for _, symID := range hiddenExtSyms {
		alts := findProductionAlternativeCounterparts(ng, symID, extSymSet, tokenCount)
		if len(alts) > 0 {
			cpInfos = append(cpInfos, extCpInfo{extSym: symID, cpSyms: alts})
		}
	}

	// Direction-B thresholds: tuned conservatively to keep table growth in
	// check and avoid regressing other grammars.
	const (
		minOriginsForDirB       = 50
		minDistinctKernelHashes = 2
	)

	var result []splitCandidate
	dbgSplit := os.Getenv("GOT_DEBUG_SPLIT") == "1"
	dbgMergedStates := 0
	dbgManyOrigins := 0
	for state := 0; state < o.tables.StateCount; state++ {
		if !o.prov.isMerged(state) {
			continue
		}
		dbgMergedStates++

		acts, ok := o.tables.ActionTable[state]
		if !ok {
			continue
		}

		// Original criterion: external token with reduce-only action
		// matching a production-based counterpart.
		matchedOriginal := false
		var origExtSym int
		for _, ci := range cpInfos {
			extActs, ok := acts[ci.extSym]
			if !ok || len(extActs) == 0 {
				continue
			}
			if !actionsAreReduceOnly(extActs) {
				continue
			}

			hasSameCp := false
			for _, cpSym := range ci.cpSyms {
				cpActs, cpOk := acts[cpSym]
				if cpOk && len(cpActs) > 0 && actionsAreReduceOnly(cpActs) &&
					actListsEqual(extActs, cpActs) {
					hasSameCp = true
					break
				}
			}
			if !hasSameCp {
				continue
			}

			matchedOriginal = true
			origExtSym = ci.extSym
			break
		}

		if matchedOriginal {
			mc := len(o.prov.origins(state))
			result = append(result, splitCandidate{
				stateIdx:     state,
				reason:       "hidden external token in merged LALR state",
				mergeCount:   mc,
				lookaheadSym: origExtSym,
			})
			continue
		}

		// Direction-B candidate: heavily merged + hidden external reduce-only
		// + semantically distinct origins (different source predecessor states).
		// Note: kernelHash is always identical across origins of a merged state
		// by construction (LR(0) merges on exact same kernel), so we use distinct
		// source predecessor states as the merge-pathology signal.
		origins := o.prov.origins(state)
		if len(origins) >= minOriginsForDirB {
			dbgManyOrigins++
		}
		if len(origins) < minOriginsForDirB {
			continue
		}
		sourceStateSet := make(map[int]struct{}, len(origins))
		for _, og := range origins {
			sourceStateSet[og.sourceState] = struct{}{}
		}
		if len(sourceStateSet) < minDistinctKernelHashes {
			if dbgSplit {
				fmt.Fprintf(os.Stderr, "[LRSPLIT-ORACLE] state=%d origins=%d distinctSrc=%d (too-few-distinct)\n",
					state, len(origins), len(sourceStateSet))
			}
			continue
		}

		// Find first hidden external symbol with single reduce-only action
		// in this state.
		dirBExtSym := -1
		for _, symID := range hiddenExtSyms {
			extActs, ok := acts[symID]
			if !ok || len(extActs) == 0 {
				continue
			}
			if !actionsAreReduceOnly(extActs) {
				continue
			}
			dirBExtSym = symID
			break
		}
		if dirBExtSym < 0 {
			if dbgSplit {
				fmt.Fprintf(os.Stderr, "[LRSPLIT-ORACLE] state=%d origins=%d distinctSrc=%d no-hidden-ext-reduce\n",
					state, len(origins), len(sourceStateSet))
			}
			continue
		}
		if dbgSplit {
			fmt.Fprintf(os.Stderr, "[LRSPLIT-ORACLE] state=%d origins=%d distinctSrc=%d ext=%d MATCH\n",
				state, len(origins), len(sourceStateSet), dirBExtSym)
		}

		result = append(result, splitCandidate{
			stateIdx:     state,
			reason:       "heavily merged LALR state with hidden external reduce",
			mergeCount:   len(origins),
			lookaheadSym: dirBExtSym,
		})
	}

	if dbgSplit {
		fmt.Fprintf(os.Stderr, "[LRSPLIT-ORACLE] mergedStates=%d manyOrigins(>=%d)=%d cpInfos=%d hiddenExt=%d\n",
			dbgMergedStates, minOriginsForDirB, dbgManyOrigins, len(cpInfos), len(hiddenExtSyms))
	}
	return result
}
