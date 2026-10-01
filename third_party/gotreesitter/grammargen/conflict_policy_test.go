package grammargen

import (
	"reflect"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestBuildConflictPolicyRejectsNonGeneratedRepetitionShift(t *testing.T) {
	ng := conflictPolicyTestGrammar(false)
	tables := &LRTables{
		StateCount: 3,
		ActionTable: map[int]map[int][]lrAction{
			1: {
				2: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, repeat: true, repeatLHS: 4},
				},
			},
		},
	}

	policies := buildConflictPolicies(tables, ng)
	if len(policies) != 0 {
		t.Fatalf("buildConflictPolicies emitted non-generated policy %+v, want none", policies)
	}
}

func TestBuildConflictPolicyEmitsSafeGeneratedRepeatContinuation(t *testing.T) {
	ng := conflictPolicyTestGrammar(true)
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				1: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, lhsSym: 1, repeat: true, repeatLHS: 3},
				},
			},
		},
	}

	policies := buildConflictPolicies(tables, ng)
	if len(policies) != 1 {
		t.Fatalf("buildConflictPolicies emitted %d policies, want 1: %+v", len(policies), policies)
	}
	if got := policies[0].ReduceSymbols; !reflect.DeepEqual(got, []gotreesitter.Symbol{3}) {
		t.Fatalf("policy ReduceSymbols = %v, want generated repeat symbol [3]", got)
	}
}

func TestBuildConflictPolicyEmitsExactGeneratedRepeatWrapperContinuation(t *testing.T) {
	ng := wrappedGeneratedRepeatConflictPolicyTestGrammar()
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				1: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, lhsSym: 4},
				},
			},
		},
	}

	policies := buildConflictPolicies(tables, ng)
	if len(policies) != 1 {
		t.Fatalf("buildConflictPolicies emitted %d policies, want 1: %+v", len(policies), policies)
	}
	if got := policies[0].Kind; got != gotreesitter.ConflictPolicyShift {
		t.Fatalf("policy kind = %d, want ConflictPolicyShift", got)
	}
	if got := policies[0].ReduceSymbols; !reflect.DeepEqual(got, []gotreesitter.Symbol{3}) {
		t.Fatalf("policy ReduceSymbols = %v, want wrapper symbol [3]", got)
	}
}

func TestBuildConflictPolicyEmitsGeneratedRepeatBeforeSafeCloseBoundary(t *testing.T) {
	ng := generatedRepeatParentSuffixGrammar([]int{4, 3})
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				2: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, lhsSym: 2, repeat: true, repeatLHS: 4},
				},
			},
		},
	}

	policies := buildConflictPolicies(tables, ng)
	if len(policies) != 1 {
		t.Fatalf("buildConflictPolicies emitted %d policies, want 1: %+v", len(policies), policies)
	}
}

func TestBuildConflictPolicyRejectsGeneratedRepeatTrailingSeparator(t *testing.T) {
	ng := generatedRepeatParentSuffixGrammar([]int{4, 2, 3})
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				2: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, lhsSym: 2, repeat: true, repeatLHS: 4},
				},
			},
		},
	}

	if policies := buildConflictPolicies(tables, ng); len(policies) != 0 {
		t.Fatalf("buildConflictPolicies emitted generated-repeat trailing-separator policy %+v, want none", policies)
	}
}

func TestBuildConflictPolicyEmitsPostfixPunctuationGeneratedRepeatContinuation(t *testing.T) {
	ng := generatedRepeatPostfixGrammar()
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				1: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, lhsSym: 1, repeat: true, repeatLHS: 3},
				},
			},
		},
	}

	policies := buildConflictPolicies(tables, ng)
	if len(policies) != 1 {
		t.Fatalf("buildConflictPolicies emitted %d policies, want 1: %+v", len(policies), policies)
	}
}

func TestBuildConflictPolicyRejectsGeneratedRepeatWithoutSameHelperProvenance(t *testing.T) {
	ng := generatedRepeatParentSuffixGrammar([]int{4, 3})
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				2: {
					{kind: lrReduce, prodIdx: 0},
					{kind: lrShift, state: 7, lhsSym: 2, repeat: true, repeatLHS: 6},
				},
			},
		},
	}

	if policies := buildConflictPolicies(tables, ng); len(policies) != 0 {
		t.Fatalf("buildConflictPolicies emitted unrelated-repeat policy %+v, want none", policies)
	}
}

func TestBuildConflictPolicyRemapsAllStatesByErrorStateOffset(t *testing.T) {
	ng := conflictPolicyTestGrammar(true)
	tables := &LRTables{
		StateCount: 4,
		ActionTable: map[int]map[int][]lrAction{
			0: {1: {{kind: lrReduce, prodIdx: 0}, {kind: lrShift, state: 7, lhsSym: 1, repeat: true, repeatLHS: 3}}},
			3: {1: {{kind: lrReduce, prodIdx: 0}, {kind: lrShift, state: 8, lhsSym: 1, repeat: true, repeatLHS: 3}}},
		},
	}

	policies := buildConflictPolicies(tables, ng)
	if len(policies) != 2 {
		t.Fatalf("buildConflictPolicies emitted %d policies, want 2: %+v", len(policies), policies)
	}
	if policies[0].State != 1 || policies[1].State != 4 {
		t.Fatalf("policy states = [%d %d], want [1 4]", policies[0].State, policies[1].State)
	}
}

func TestBuildConflictPolicyRejectsUnsupportedActionShapes(t *testing.T) {
	ng := conflictPolicyTestGrammar(false)
	tests := []struct {
		name    string
		actions []lrAction
	}{
		{
			name:    "extra shift",
			actions: []lrAction{{kind: lrReduce, prodIdx: 0}, {kind: lrShift, state: 7, repeat: true, isExtra: true}},
		},
		{
			name:    "multiple shifts",
			actions: []lrAction{{kind: lrReduce, prodIdx: 0}, {kind: lrShift, state: 7, repeat: true}, {kind: lrShift, state: 8, repeat: true}},
		},
		{
			name:    "non repetition shift",
			actions: []lrAction{{kind: lrReduce, prodIdx: 0}, {kind: lrShift, state: 7}},
		},
		{
			name:    "accept",
			actions: []lrAction{{kind: lrReduce, prodIdx: 0}, {kind: lrAccept}},
		},
		{
			name:    "recover or unknown",
			actions: []lrAction{{kind: lrReduce, prodIdx: 0}, {kind: lrActionKind(99)}},
		},
		{
			name:    "extra reduce",
			actions: []lrAction{{kind: lrReduce, prodIdx: 0, isExtra: true}, {kind: lrShift, state: 7, repeat: true}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tables := &LRTables{
				StateCount: 1,
				ActionTable: map[int]map[int][]lrAction{
					0: {2: tc.actions},
				},
			}
			if policies := buildConflictPolicies(tables, ng); len(policies) != 0 {
				t.Fatalf("buildConflictPolicies emitted %+v, want none", policies)
			}
		})
	}
}

func TestBuildConflictPolicyHasNoLanguageNameDependence(t *testing.T) {
	ng := conflictPolicyTestGrammar(true)
	tables := &LRTables{
		StateCount: 1,
		ActionTable: map[int]map[int][]lrAction{
			0: {1: {{kind: lrReduce, prodIdx: 0}, {kind: lrShift, state: 7, lhsSym: 1, repeat: true, repeatLHS: 3}}},
		},
	}

	first := buildConflictPolicies(tables, ng)
	renamed := *ng
	renamed.Symbols = append([]SymbolInfo(nil), ng.Symbols...)
	renamed.Symbols[3].Name = "renamed_nonrepeat_list"
	second := buildConflictPolicies(tables, &renamed)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("policies changed after symbol rename:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func conflictPolicyTestGrammar(reduceIsGeneratedRepeat bool) *NormalizedGrammar {
	return &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "end", Kind: SymbolTerminal},
			{Name: "item", Kind: SymbolNamedToken},
			{Name: "terminator", Kind: SymbolTerminal, Named: true},
			{Name: "list", Kind: SymbolNonterminal, GeneratedRepeatAux: reduceIsGeneratedRepeat},
			{Name: "list_repeat1", Kind: SymbolNonterminal, GeneratedRepeatAux: true},
		},
		Productions: []Production{
			{LHS: 3, RHS: []int{3, 1}},
			{LHS: 3, RHS: []int{1}},
		},
	}
}

func wrappedGeneratedRepeatConflictPolicyTestGrammar() *NormalizedGrammar {
	return &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "end", Kind: SymbolTerminal},
			{Name: "item", Kind: SymbolNamedToken},
			{Name: "terminator", Kind: SymbolTerminal, Named: true},
			{Name: "content", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "content_repeat1", Kind: SymbolNonterminal, GeneratedRepeatAux: true},
		},
		Productions: []Production{
			{LHS: 3, RHS: []int{4}},
			{LHS: 4, RHS: []int{4, 1}},
			{LHS: 4, RHS: []int{1}},
		},
	}
}

func generatedRepeatParentSuffixGrammar(parentRHS []int) *NormalizedGrammar {
	return &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "end", Kind: SymbolTerminal},
			{Name: "item", Kind: SymbolNamedToken},
			{Name: "separator", Kind: SymbolTerminal},
			{Name: "close", Kind: SymbolTerminal},
			{Name: "items_repeat1", Kind: SymbolNonterminal, GeneratedRepeatAux: true},
			{Name: "container", Kind: SymbolNonterminal, Visible: true, Named: true},
			{Name: "other_repeat1", Kind: SymbolNonterminal, GeneratedRepeatAux: true},
		},
		Productions: []Production{
			{LHS: 4, RHS: []int{4, 2, 1}},
			{LHS: 4, RHS: []int{2, 1}},
			{LHS: 5, RHS: parentRHS},
			{LHS: 6, RHS: []int{6, 2, 1}},
			{LHS: 6, RHS: []int{2, 1}},
		},
	}
}

func generatedRepeatPostfixGrammar() *NormalizedGrammar {
	return &NormalizedGrammar{
		Symbols: []SymbolInfo{
			{Name: "end", Kind: SymbolTerminal},
			{Name: ".", Kind: SymbolTerminal},
			{Name: "identifier", Kind: SymbolNamedToken},
			{Name: "attribute_repeat1", Kind: SymbolNonterminal, GeneratedRepeatAux: true},
		},
		Productions: []Production{
			{LHS: 3, RHS: []int{3, 1, 2}},
			{LHS: 3, RHS: []int{1, 2}},
		},
	}
}
