package gotreesitter

import (
	"runtime"
	"testing"
)

// This file tests the TypeScript candidate-index memory boundary. The fused
// walk polls for cancellation and memory pressure as it collects candidates.
// A wide dynamic-import tree gives the index bounded, repeatable work.

const (
	testTSCompatProgramSym       Symbol = 1
	testTSCompatDynamicImportSym Symbol = 3 // named "import" (dynamic import() call)
	// Symbol 2 is the anonymous "import" token.
)

// buildTypeScriptCompatWideDynamicImportTree builds a wide TypeScript tree.
// Each childless dynamic-import leaf becomes an index candidate.
func buildTypeScriptCompatWideDynamicImportTree(numImport int) (*Node, *Language, []byte) {
	arena := newNodeArena(arenaClassFull)
	children := make([]*Node, numImport)
	for i := 0; i < numImport; i++ {
		off := uint32(i)
		children[i] = newLeafNodeInArena(arena, testTSCompatDynamicImportSym, true, off, off+1, Point{}, Point{})
	}
	root := newParentNodeInArena(arena, testTSCompatProgramSym, true, cloneNodeSliceInArena(arena, children), nil, 0)

	lang := &Language{
		Name: "typescript",
		SymbolNames: []string{
			"EOF", "program", "import", "import", "call_expression",
			"type_arguments", "arguments", "predefined_type",
			"binary_expression", ">", "parenthesized_expression", "<",
			"identifier", "member_expression", "sequence_expression",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "program", Visible: true, Named: true},
			{Name: "import", Visible: true, Named: false},
			{Name: "import", Visible: true, Named: true},
			{Name: "call_expression", Visible: true, Named: true},
			{Name: "type_arguments", Visible: true, Named: true},
			{Name: "arguments", Visible: true, Named: true},
			{Name: "predefined_type", Visible: true, Named: true},
			{Name: "binary_expression", Visible: true, Named: true},
			{Name: ">", Visible: true, Named: false},
			{Name: "parenthesized_expression", Visible: true, Named: true},
			{Name: "<", Visible: true, Named: false},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "member_expression", Visible: true, Named: true},
			{Name: "sequence_expression", Visible: true, Named: true},
		},
	}
	return root, lang, []byte("import")
}

func TestTypeScriptCandidateIndexUnboundedBaselineCompletes(t *testing.T) {
	const numImport = 2000000
	root, lang, source := buildTypeScriptCompatWideDynamicImportTree(numImport)

	stats, reason := normalizeJavaScriptTypeScriptStatementKeywordsAndPrecedenceWithDetailedStats(root, source, lang, nil)
	if reason != ParseStopNone {
		t.Fatalf("reason = %q, want %q (unbounded walk must not stop)", reason, ParseStopNone)
	}
	if got, want := stats.indexNodesVisited, uint64(numImport+1); got != want {
		t.Fatalf("indexNodesVisited = %d, want %d (unbounded walk must visit every node)", got, want)
	}
	if got := stats.typeScriptCompatibility.len(); got != numImport {
		t.Fatalf("candidate count = %d, want %d", got, numImport)
	}
}

func TestTypeScriptCandidateIndexStopsOnMemoryBudget(t *testing.T) {
	const numImport = 2000000
	root, lang, source := buildTypeScriptCompatWideDynamicImportTree(numImport)

	const budgetBytes = 1 << 20 // The candidate index exceeds this budget.
	parser := newGoCompatBudgetTestParser(budgetBytes)
	parser.language = lang

	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	poller := parseStopPoller{memoryBudgetParser: parser}
	stats, reason := normalizeJavaScriptTypeScriptStatementKeywordsAndPrecedenceWithDetailedStats(root, source, lang, &poller)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	if reason != ParseStopMemoryBudget {
		t.Fatalf("reason = %q, want %q", reason, ParseStopMemoryBudget)
	}
	if !parser.compatMemoryBudgetTripped {
		t.Fatal("compatMemoryBudgetTripped = false, want true (sticky latch for resultMaterializationStopReason's later recheck)")
	}
	if stats.indexNodesVisited == 0 {
		t.Fatal("indexNodesVisited = 0, want some real work done before the trip")
	}
	if stats.indexNodesVisited >= uint64(numImport+1) {
		t.Fatalf("indexNodesVisited = %d, want a partial prefix < %d", stats.indexNodesVisited, numImport+1)
	}

	growth := int64(0)
	if after.HeapAlloc > before.HeapAlloc {
		growth = int64(after.HeapAlloc - before.HeapAlloc)
	}
	const maxOvershoot = 20 * budgetBytes
	t.Logf("nodesVisited=%d/%d heap growth=%d bytes (budget=%d, %.2fx)",
		stats.indexNodesVisited, numImport+1, growth, int64(budgetBytes), float64(growth)/float64(budgetBytes))
	if growth > maxOvershoot {
		t.Fatalf("heap growth = %d bytes, want <= %d (20x budget=%d)", growth, maxOvershoot, int64(budgetBytes))
	}
}

func TestNormalizeTypeScriptTreeCompatibilityPropagatesMemoryBudgetStop(t *testing.T) {
	const numImport = 2000000
	root, lang, source := buildTypeScriptCompatWideDynamicImportTree(numImport)

	const budgetBytes = 1 << 20
	parser := newGoCompatBudgetTestParser(budgetBytes)
	parser.language = lang

	reason := normalizeTypeScriptTreeCompatibilityWithParser(root, source, parser, lang)
	if reason != ParseStopMemoryBudget {
		t.Fatalf("normalizeTypeScriptTreeCompatibilityWithParser reason = %q, want %q", reason, ParseStopMemoryBudget)
	}
	if !parser.compatMemoryBudgetTripped {
		t.Fatal("compatMemoryBudgetTripped = false, want true")
	}
	if got := parser.resultMaterializationStopReason(nil); got != ParseStopMemoryBudget {
		t.Fatalf("resultMaterializationStopReason() = %q, want %q after a TypeScript compatibility memory-budget trip", got, ParseStopMemoryBudget)
	}
}
