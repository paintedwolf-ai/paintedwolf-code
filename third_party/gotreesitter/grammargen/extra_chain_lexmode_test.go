package grammargen

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func singleExtraShiftTarget(t *testing.T, ng *NormalizedGrammar, acts []lrAction) int {
	t.Helper()
	if len(acts) != 1 || acts[0].kind != lrShift || !acts[0].isExtra {
		t.Fatalf("expected single synthetic extra-chain shift, got %s", diagFormatActions(ng, acts))
	}
	return acts[0].state
}

func TestNonterminalExtraChainLexModesDoNotInheritTerminalExtras(t *testing.T) {
	g := NewGrammar("extra_chain_lexmode")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`.`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	slashStarSyms := diagFindAllSymbols(ng, "/*")
	if len(slashStarSyms) != 1 {
		t.Fatalf("expected one /* symbol, got %v", slashStarSyms)
	}
	whitespaceSyms := diagFindAllSymbols(ng, "_whitespace")
	if len(whitespaceSyms) != 1 {
		t.Fatalf("expected one _whitespace symbol, got %v", whitespaceSyms)
	}
	closeCommentSyms := diagFindAllSymbols(ng, "*/")
	if len(closeCommentSyms) != 1 {
		t.Fatalf("expected one */ symbol, got %v", closeCommentSyms)
	}

	acts := tables.ActionTable[0][slashStarSyms[0]]
	if len(acts) != 1 || acts[0].kind != lrShift {
		t.Fatalf("expected synthetic extra-chain shift on /*, got %s", diagFormatActions(ng, acts))
	}
	target := acts[0].state
	if target < tables.ExtraChainStateStart {
		t.Fatalf("expected synthetic state >= %d, got %d", tables.ExtraChainStateStart, target)
	}

	lexModes, stateToMode, _ := computeLexModes(
		tables.StateCount,
		ng.TokenCount(),
		func(state, sym int) bool {
			if bySym, ok := tables.ActionTable[state]; ok {
				if acts, ok := bySym[sym]; ok && len(acts) > 0 {
					return true
				}
			}
			return false
		},
		computeStringPrefixExtensions(ng.Terminals),
		ng.ExtraSymbols,
		tables.ExtraChainStateStart,
		map[int]bool{},
		ng.ExternalSymbols,
		ng.WordSymbolID,
		map[int]bool{},
		terminalPatternSymSet(ng),
		nil,
		nil,
		nil,
		patternTerminalSymSet(ng),
		zeroWidthTerminalSymSet(ng),
	)

	initialMode := lexModes[stateToMode[0]]
	if !initialMode.skipWhitespace {
		t.Fatal("initial state should still skip whitespace extras")
	}
	if !initialMode.validSymbols[whitespaceSyms[0]] {
		t.Fatal("initial state should keep terminal extra valid")
	}

	chainMode := lexModes[stateToMode[target]]
	if chainMode.skipWhitespace {
		t.Fatal("synthetic extra-chain state should not skip whitespace")
	}
	if chainMode.validSymbols[whitespaceSyms[0]] {
		t.Fatal("synthetic extra-chain state should not inherit terminal extra symbols")
	}
	if !chainMode.validSymbols[closeCommentSyms[0]] {
		t.Fatal("synthetic extra-chain state should still accept the explicit comment terminator token")
	}
}

func TestExtraChainLexModesSkipMainStateLookaheadWidening(t *testing.T) {
	const (
		tokenCount     = 5
		mainToken      = 1
		chainToken     = 2
		nextChainToken = 3
		widenedToken   = 4
	)

	actionLookup := func(state, sym int) bool {
		switch state {
		case 0:
			return sym == mainToken
		case 2:
			return sym == chainToken
		case 3:
			return sym == nextChainToken
		default:
			return false
		}
	}
	followCalls := make(map[int]int)
	missingCalls := make(map[int]int)

	lexModes, stateToMode, _ := computeLexModes(
		4,
		tokenCount,
		actionLookup,
		nil,
		nil,
		2,
		nil,
		nil,
		0,
		map[int]bool{widenedToken: true},
		map[int]bool{mainToken: true, chainToken: true, nextChainToken: true, widenedToken: true},
		func(state int) []int {
			followCalls[state]++
			return []int{widenedToken}
		},
		func(state int) []int {
			missingCalls[state]++
			return []int{widenedToken}
		},
		nil,
		nil,
		nil,
	)

	if followCalls[2] != 0 || followCalls[3] != 0 {
		t.Fatalf("follow widening called for extra-chain states: %v", followCalls)
	}
	if missingCalls[2] != 0 || missingCalls[3] != 0 {
		t.Fatalf("missing-recovery widening called for extra-chain states: %v", missingCalls)
	}

	mainMode := lexModes[stateToMode[0]]
	if !mainMode.validSymbols[widenedToken] {
		t.Fatal("main state should still receive lookahead widening")
	}

	chainMode := lexModes[stateToMode[2]]
	if !chainMode.validSymbols[chainToken] {
		t.Fatal("extra-chain state should keep direct chain action token")
	}
	if chainMode.validSymbols[widenedToken] {
		t.Fatal("extra-chain state should not receive parser-main-state lookahead widening")
	}

	nextChainMode := lexModes[stateToMode[3]]
	if !nextChainMode.validSymbols[nextChainToken] {
		t.Fatal("later extra-chain state should keep direct chain action token")
	}
	if nextChainMode.validSymbols[widenedToken] {
		t.Fatal("later extra-chain state should not receive parser-main-state lookahead widening")
	}
}

func TestNonterminalExtraChainRuntimeProducesReducedExtraNode(t *testing.T) {
	g := NewGrammar("extra_chain_runtime")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tree, err := gotreesitter.NewParser(report.Language).Parse([]byte("/**/foo"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 16))
	}
	if root.EndByte() != 7 {
		t.Fatalf("root end byte = %d, want 7", root.EndByte())
	}
	if root.ChildCount() < 2 {
		t.Fatalf("root child count = %d, want at least 2", root.ChildCount())
	}
	if got := root.Child(0).Type(report.Language); got != "block_comment" {
		t.Fatalf("child[0] type = %q, want block_comment; sexpr=%s", got, safeSExpr(root, report.Language, 16))
	}
	if got := root.Child(1).Type(report.Language); got != "item" {
		t.Fatalf("child[1] type = %q, want item; sexpr=%s", got, safeSExpr(root, report.Language, 16))
	}
}

func TestNonterminalExtraChainRuntimeProducesReducedRepeatedExtraNode(t *testing.T) {
	g := NewGrammar("extra_chain_runtime_repeat")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`.`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tree, err := gotreesitter.NewParser(report.Language).Parse([]byte("/**/foo"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 16))
	}
	if got := safeSExpr(root, report.Language, 16); got != "(source_file (block_comment) (item))" {
		t.Fatalf("sexpr = %s, want (source_file (block_comment) (item))", got)
	}
}

func TestNonterminalExtraChainRuntimeProducesReducedRepeatedExtraNodeWithSiblingCommentExtra(t *testing.T) {
	g := NewGrammar("extra_chain_runtime_repeat_comment")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("comment", Seq(
		Token(Str("//")),
		Repeat(Token(Pat(`.`))),
	))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`.`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("comment"), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tree, err := gotreesitter.NewParser(report.Language).Parse([]byte("/**/foo"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 16))
	}
	if got := safeSExpr(root, report.Language, 16); got != "(source_file (block_comment) (item))" {
		t.Fatalf("sexpr = %s, want (source_file (block_comment) (item))", got)
	}
}

func TestNonterminalExtraChainRuntimeKeepsSiblingCommentStartAsBlockText(t *testing.T) {
	g := NewGrammar("extra_chain_runtime_comment_start_text")
	g.Define("source_file", Repeat(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("comment", Seq(
		Token(Str("//")),
		Repeat(Token(Pat(`[^\n]`))),
	))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`[\s\S]`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("comment"), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	for _, tc := range []struct {
		name string
		src  []byte
		want string
	}{
		{name: "eof", src: []byte("/* // */"), want: "(source_file (block_comment))"},
		{name: "before_item", src: []byte("/* // */foo"), want: "(source_file (block_comment) (item))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := gotreesitter.NewParser(report.Language).Parse(tc.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			root := tree.RootNode()
			if root == nil {
				t.Fatal("nil root")
			}
			if root.HasError() {
				t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 16))
			}
			if got := safeSExpr(root, report.Language, 16); got != tc.want {
				t.Fatalf("sexpr = %s, want %s", got, tc.want)
			}
			block := root.Child(0)
			if block == nil || block.Type(report.Language) != "block_comment" {
				t.Fatalf("first child = %v, want block_comment; tree=%s", block, safeSExpr(root, report.Language, 16))
			}
			// Span pins follow C tree-sitter reporting: a token's span never
			// includes its leading padding (whitespace between chain tokens), so
			// block children may have byte gaps at padding positions. The
			// original contiguous pins ({2,5},{5,8}) encoded the padding-target
			// widening added by e22008cf and deliberately reverted the same day
			// by 9c82a09b ("Fix hidden padding to prevent span widening"), which
			// unit-pins this exact shape in
			// TestFlattenedGeneratedRepeatPaddingDoesNotWidenAnonymousLeaf:
			// an anonymous leaf ("//") under a generated repeat aux must NOT
			// absorb preceding padding.
			wantSpans := [][2]uint32{{0, 2}, {3, 5}, {6, 8}}
			if got := block.ChildCount(); got != len(wantSpans) {
				t.Fatalf("block_comment child count = %d, want %d; tree=%s", got, len(wantSpans), safeSExpr(root, report.Language, 16))
			}
			for i, want := range wantSpans {
				child := block.Child(i)
				if child == nil {
					t.Fatalf("block child %d is nil", i)
				}
				if child.StartByte() != want[0] || child.EndByte() != want[1] {
					t.Fatalf("block child %d span = %d..%d (%q), want %d..%d",
						i, child.StartByte(), child.EndByte(), string(tc.src[child.StartByte():child.EndByte()]), want[0], want[1])
				}
			}
		})
	}
}

func TestNonterminalExtraChainRuntimeProducesReducedRepeatedExtraNodeAtEOF(t *testing.T) {
	g := NewGrammar("extra_chain_runtime_repeat_eof")
	g.Define("source_file", Repeat(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`.`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tree, err := gotreesitter.NewParser(report.Language).Parse([]byte("/**/"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 16))
	}
	if got := safeSExpr(root, report.Language, 16); got != "(source_file (block_comment))" {
		t.Fatalf("sexpr = %s, want (source_file (block_comment))", got)
	}
}

func TestNonterminalExtraChainSyntheticStatesCanStartNestedExtras(t *testing.T) {
	g := NewGrammar("extra_chain_nested_state")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Token(Pat(`.`))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	slashStarSyms := diagFindAllSymbols(ng, "/*")
	if len(slashStarSyms) != 1 {
		t.Fatalf("expected one /* symbol, got %v", slashStarSyms)
	}

	rootActs := tables.ActionTable[0][slashStarSyms[0]]
	if len(rootActs) != 1 || rootActs[0].kind != lrShift {
		t.Fatalf("expected synthetic extra-chain shift on /* from state 0, got %s", diagFormatActions(ng, rootActs))
	}
	outerState := rootActs[0].state
	if outerState < tables.ExtraChainStateStart {
		t.Fatalf("expected synthetic target >= %d, got %d", tables.ExtraChainStateStart, outerState)
	}

	nestedActs := tables.ActionTable[outerState][slashStarSyms[0]]
	if len(nestedActs) == 0 {
		t.Fatalf("expected nested extra shift on /* from synthetic state %d", outerState)
	}
	foundNestedShift := false
	for _, act := range nestedActs {
		if act.kind == lrShift && act.isExtra {
			foundNestedShift = true
			break
		}
	}
	if !foundNestedShift {
		t.Fatalf("expected nested extra shift on /* from synthetic state %d, got %s", outerState, diagFormatActions(ng, nestedActs))
	}
}

func TestNonterminalExtraChainSyntheticStatesSkipNestedStartsWithoutStarterOverlap(t *testing.T) {
	g := NewGrammar("extra_chain_no_nested_overlap")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("directive", Seq(
		Token(Str("#region")),
		Token(Pat(`[A-Za-z]+`)),
		Token(Str("\n")),
	))
	g.SetExtras(Pat(`[ \t]+`), Sym("directive"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	startSyms := diagFindAllSymbols(ng, "#region")
	if len(startSyms) != 1 {
		t.Fatalf("expected one #region symbol, got %v", startSyms)
	}

	rootActs := tables.ActionTable[0][startSyms[0]]
	if len(rootActs) != 1 || rootActs[0].kind != lrShift {
		t.Fatalf("expected synthetic extra-chain shift on #region from state 0, got %s", diagFormatActions(ng, rootActs))
	}
	outerState := rootActs[0].state
	if outerState < tables.ExtraChainStateStart {
		t.Fatalf("expected synthetic target >= %d, got %d", tables.ExtraChainStateStart, outerState)
	}

	for _, act := range tables.ActionTable[outerState][startSyms[0]] {
		if act.kind == lrShift && act.isExtra {
			t.Fatalf("synthetic state %d should not inject nested #region extra starts: %s", outerState, diagFormatActions(ng, tables.ActionTable[outerState][startSyms[0]]))
		}
	}
}

func TestNonterminalExtraChainSyntheticStatesSkipNestedExternalStarts(t *testing.T) {
	g := NewGrammar("extra_chain_external_no_nested")
	g.SetExternals(Sym("_external_extra_start"))
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("external_extra", Seq(
		Sym("_external_extra_start"),
		Token(Pat(`[a-z]+`)),
	))
	g.SetExtras(Pat(`\s`), Sym("external_extra"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	if len(ng.ExternalSymbols) != 1 {
		t.Fatalf("expected one external symbol, got %v", ng.ExternalSymbols)
	}
	startSym := ng.ExternalSymbols[0]

	rootActs := tables.ActionTable[0][startSym]
	if len(rootActs) != 1 || rootActs[0].kind != lrShift || !rootActs[0].isExtra {
		t.Fatalf("expected synthetic extra-chain shift on external start from state 0, got %s", diagFormatActions(ng, rootActs))
	}
	outerState := rootActs[0].state
	if outerState < tables.ExtraChainStateStart {
		t.Fatalf("expected synthetic target >= %d, got %d", tables.ExtraChainStateStart, outerState)
	}

	for _, act := range tables.ActionTable[outerState][startSym] {
		if act.kind == lrShift && act.isExtra {
			t.Fatalf("synthetic state %d should not inject nested external extra starts: %s", outerState, diagFormatActions(ng, tables.ActionTable[outerState][startSym]))
		}
	}
}

func TestNonterminalExtraChainExternalStartsShareEntryChain(t *testing.T) {
	g := NewGrammar("extra_chain_external_shared")
	g.SetExternals(Sym("_external_extra_start"))
	g.Define("source_file", Seq(Sym("first"), Sym("second")))
	g.Define("first", Str("a"))
	g.Define("second", Str("b"))
	g.Define("external_extra", Seq(
		Sym("_external_extra_start"),
		Token(Pat(`[a-z]+`)),
	))
	g.SetExtras(Pat(`\s`), Sym("external_extra"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	if len(ng.ExternalSymbols) != 1 {
		t.Fatalf("expected one external symbol, got %v", ng.ExternalSymbols)
	}
	startSym := ng.ExternalSymbols[0]
	// Since 38955032 ("promote plain visible string rules to named tokens"),
	// a rule whose whole body is a bare string (second = "b") IS the token,
	// named after the rule — matching tree-sitter C conventions. There is no
	// separate anonymous "b" terminal anymore, so locate the structural shift
	// via the promoted "second" token instead.
	bSyms := diagFindAllSymbols(ng, "second")
	if len(bSyms) != 1 {
		t.Fatalf("expected one second token symbol, got %v", bSyms)
	}

	bState := -1
	for state := 0; state < tables.ExtraChainStateStart; state++ {
		for _, act := range tables.ActionTable[state][bSyms[0]] {
			if act.kind == lrShift && !act.isExtra {
				bState = state
				break
			}
		}
		if bState >= 0 {
			break
		}
	}
	if bState < 0 {
		t.Fatal("state with structural b shift not found")
	}

	rootTarget := singleExtraShiftTarget(t, ng, tables.ActionTable[0][startSym])
	bTarget := singleExtraShiftTarget(t, ng, tables.ActionTable[bState][startSym])
	if rootTarget != bTarget {
		t.Fatalf("external-starting extras should share entry chain across main states, got state0=%d state%d=%d", rootTarget, bState, bTarget)
	}
}

func TestNonterminalExtraChainSyntheticStatesPreferStructuralTokensOverExtraInjection(t *testing.T) {
	g := NewGrammar("extra_chain_structural_preference")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("comment", Seq(
		Token(Str("//")),
		Token(Pat(`.`)),
	))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`.`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("comment"), Sym("block_comment"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	slashStarSyms := diagFindAllSymbols(ng, "/*")
	if len(slashStarSyms) != 1 {
		t.Fatalf("expected one /* symbol, got %v", slashStarSyms)
	}
	slashSlashSyms := diagFindAllSymbols(ng, "//")
	if len(slashSlashSyms) != 1 {
		t.Fatalf("expected one // symbol, got %v", slashSlashSyms)
	}

	rootActs := tables.ActionTable[0][slashStarSyms[0]]
	if len(rootActs) != 1 || rootActs[0].kind != lrShift {
		t.Fatalf("expected synthetic extra-chain shift on /* from state 0, got %s", diagFormatActions(ng, rootActs))
	}
	outerState := rootActs[0].state
	if outerState < tables.ExtraChainStateStart {
		t.Fatalf("expected synthetic target >= %d, got %d", tables.ExtraChainStateStart, outerState)
	}

	actions := tables.ActionTable[outerState][slashSlashSyms[0]]
	if len(actions) == 0 {
		t.Fatalf("expected structural // action in synthetic state %d", outerState)
	}
	for _, act := range actions {
		if act.isExtra {
			t.Fatalf("synthetic state %d should not inject // as an extra when a structural action already exists: %s", outerState, diagFormatActions(ng, actions))
		}
	}
}

func TestNonterminalExtraChainShiftExtraChainFlagSurvivesAssembly(t *testing.T) {
	g := NewGrammar("extra_chain_shift_extra_flag")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Token(Pat(`.`))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("GenerateWithReport: %v", err)
	}
	lang := report.Language

	var (
		slashStarSym gotreesitter.Symbol
		foundSlash   bool
	)
	for i, name := range lang.SymbolNames {
		if name == "/*" {
			slashStarSym = gotreesitter.Symbol(i)
			foundSlash = true
			break
		}
	}
	if !foundSlash {
		t.Fatal("missing /* symbol")
	}

	actionIdx := lookupActionIndexForLanguage(lang, 1, slashStarSym)
	if actionIdx == 0 || int(actionIdx) >= len(lang.ParseActions) {
		t.Fatalf("missing parse action for /* in root state: %d", actionIdx)
	}
	actions := lang.ParseActions[actionIdx].Actions
	if len(actions) != 1 || actions[0].Type != gotreesitter.ParseActionShift {
		t.Fatalf("unexpected actions for /* in root state: %+v", actions)
	}
	if !actions[0].ExtraChain {
		t.Fatalf("root extra-chain shift for /* lost ExtraChain flag: %+v", actions[0])
	}
	if actions[0].Extra {
		t.Fatalf("root extra-chain shift for /* should not be treated as a terminal extra: %+v", actions[0])
	}
}

func TestNonterminalExtraChainSyntheticReduceStatesDoNotInjectNestedExtraStarts(t *testing.T) {
	g := NewGrammar("extra_chain_reduce_state")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`.`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, ctx, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	addNonterminalExtraChains(tables, ng, ctx)

	slashStarSyms := diagFindAllSymbols(ng, "/*")
	if len(slashStarSyms) != 1 {
		t.Fatalf("expected one /* symbol, got %v", slashStarSyms)
	}
	closeCommentSyms := diagFindAllSymbols(ng, "*/")
	if len(closeCommentSyms) != 1 {
		t.Fatalf("expected one */ symbol, got %v", closeCommentSyms)
	}

	rootActs := tables.ActionTable[0][slashStarSyms[0]]
	if len(rootActs) != 1 || rootActs[0].kind != lrShift {
		t.Fatalf("expected synthetic extra-chain shift on /* from state 0, got %s", diagFormatActions(ng, rootActs))
	}
	outerState := rootActs[0].state

	closeActs := tables.ActionTable[outerState][closeCommentSyms[0]]
	if len(closeActs) == 0 {
		t.Fatalf("expected */ shift from synthetic state %d", outerState)
	}
	closeState := -1
	for _, act := range closeActs {
		if act.kind == lrShift {
			closeState = act.state
			break
		}
	}
	if closeState < 0 {
		t.Fatalf("expected shift on */ from synthetic state %d, got %s", outerState, diagFormatActions(ng, closeActs))
	}

	actions := tables.ActionTable[closeState][slashStarSyms[0]]
	if len(actions) == 0 {
		t.Fatalf("expected reduce lookahead on /* from synthetic reduce state %d", closeState)
	}
	for _, act := range actions {
		if act.kind == lrShift {
			t.Fatalf("synthetic reduce state %d should not inject nested extra starts: %s", closeState, diagFormatActions(ng, actions))
		}
	}
}

func TestNonterminalExtraChainRuntimeSupportsNestedExtras(t *testing.T) {
	g := NewGrammar("extra_chain_nested_runtime")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Token(Pat(`.`))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tree, err := gotreesitter.NewParser(report.Language).Parse([]byte("/*a/*b*/c*/foo"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 16))
	}
	if root.ChildCount() < 2 {
		t.Fatalf("root child count = %d, want at least 2; sexpr=%s", root.ChildCount(), safeSExpr(root, report.Language, 32))
	}
	outer := root.Child(0)
	if got := outer.Type(report.Language); got != "block_comment" {
		t.Fatalf("child[0] type = %q, want block_comment; sexpr=%s", got, safeSExpr(root, report.Language, 32))
	}
	if got := root.Child(1).Type(report.Language); got != "item" {
		t.Fatalf("child[1] type = %q, want item; sexpr=%s", got, safeSExpr(root, report.Language, 32))
	}
	if got := safeSExpr(root, report.Language, 32); got != "(source_file (block_comment (block_comment)) (item))" {
		t.Fatalf("sexpr = %s, want nested block_comment shape", got)
	}
}

func TestNonterminalExtraChainRuntimeMatchesScalaStyleNestedBlockComments(t *testing.T) {
	g := NewGrammar("extra_chain_scala_style")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("comment", Seq(
		Token(Str("//")),
		Repeat(Token(Pat(`[^\n]`))),
	))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`[\s\S]`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("comment"), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	src := []byte(`/**/
/** comment 1
 * /* comment 2
 *  /* / * * /comment 3 */
 // comment 4
 * @param
 *  */
*/
foo`)
	tree, err := gotreesitter.NewParser(report.Language).Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 64))
	}
	if got := safeSExpr(root, report.Language, 64); got != "(source_file (block_comment) (block_comment (block_comment (block_comment))) (item))" {
		t.Fatalf("sexpr = %s, want Scala-style nested block_comment shape", got)
	}
}

func TestNonterminalExtraChainRuntimeMatchesScalaStyleNestedBlockCommentsAtEOF(t *testing.T) {
	g := NewGrammar("extra_chain_scala_style_eof")
	g.Define("source_file", Repeat(Sym("item")))
	g.Define("item", Pat(`[a-z]+`))
	g.Define("comment", Seq(
		Token(Str("//")),
		Repeat(Token(Pat(`[^\n]`))),
	))
	g.Define("block_comment", Seq(
		Token(Str("/*")),
		Repeat(Choice(Token(Pat(`[\s\S]`)), Token(Str("//")))),
		Token(Str("*/")),
	))
	g.SetExtras(Pat(`\s`), Sym("comment"), Sym("block_comment"))

	report, err := GenerateWithReport(g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	src := []byte(`/**/
/** comment 1
 * /* comment 2
 *  /* / * * /comment 3 */
 // comment 4
 * @param
 *  */
*/`)
	tree, err := gotreesitter.NewParser(report.Language).Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if root.HasError() {
		t.Fatalf("root has error: %s", safeSExpr(root, report.Language, 64))
	}
	if got := safeSExpr(root, report.Language, 64); got != "(source_file (block_comment) (block_comment (block_comment (block_comment))))" {
		t.Fatalf("sexpr = %s, want Scala-style nested block_comment EOF shape", got)
	}
}
