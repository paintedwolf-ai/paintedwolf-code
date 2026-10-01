package gotreesitter

import "testing"

func TestNormalizeDartCompatibilityDoesNotSynthesizeCollapsedChildren(t *testing.T) {
	lang := &Language{
		Name: "dart",
		SymbolNames: []string{
			"EOF",
			"program",
			"final_builtin",
			"final",
			"super",
			"super",
			"base",
			"base",
			"this",
			"this",
			"negation_operator",
			"!",
			"relational_operator",
			"<",
			">",
			"nullable_type",
			"?",
			"null_literal",
			"null",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "program", Visible: true, Named: true},
			{Name: "final_builtin", Visible: true, Named: true},
			{Name: "final", Visible: true, Named: false},
			{Name: "super", Visible: true, Named: true},
			{Name: "super", Visible: true, Named: false},
			{Name: "base", Visible: true, Named: true},
			{Name: "base", Visible: true, Named: false},
			{Name: "this", Visible: true, Named: true},
			{Name: "this", Visible: true, Named: false},
			{Name: "negation_operator", Visible: true, Named: true},
			{Name: "!", Visible: true, Named: false},
			{Name: "relational_operator", Visible: true, Named: true},
			{Name: "<", Visible: true, Named: false},
			{Name: ">", Visible: true, Named: false},
			{Name: "nullable_type", Visible: true, Named: true},
			{Name: "?", Visible: true, Named: false},
			{Name: "null_literal", Visible: true, Named: true},
			{Name: "null", Visible: true, Named: false},
		},
	}
	source := []byte("final super base this ! < > ? null")
	arena := newNodeArena(arenaClassFull)
	finalNode := newLeafNodeInArena(arena, 2, true, 0, 5, Point{}, Point{Column: 5})
	superNode := newLeafNodeInArena(arena, 4, true, 6, 11, Point{Column: 6}, Point{Column: 11})
	baseNode := newLeafNodeInArena(arena, 6, true, 12, 16, Point{Column: 12}, Point{Column: 16})
	thisNode := newLeafNodeInArena(arena, 8, true, 17, 21, Point{Column: 17}, Point{Column: 21})
	negationNode := newLeafNodeInArena(arena, 10, true, 22, 23, Point{Column: 22}, Point{Column: 23})
	relOpNode := newLeafNodeInArena(arena, 12, true, 24, 25, Point{Column: 24}, Point{Column: 25})
	gtNode := newLeafNodeInArena(arena, 12, true, 26, 27, Point{Column: 26}, Point{Column: 27})
	nullableNode := newLeafNodeInArena(arena, 15, true, 28, 29, Point{Column: 28}, Point{Column: 29})
	nullNode := newLeafNodeInArena(arena, 17, true, 30, 34, Point{Column: 30}, Point{Column: 34})
	root := newParentNodeInArena(arena, 1, true, []*Node{finalNode, superNode, baseNode, thisNode, negationNode, relOpNode, gtNode, nullableNode, nullNode}, nil, 0)

	normalizeDartCompatibility(root, source, lang)

	// Note: "final_builtin", "base", "negation_operator", "relational_operator",
	// "nullable_type", and "null_literal" are intentionally NOT asserted here
	// anymore. "base" is no longer collapsed by the parser at all (see
	// wrapsSameNamedAnonymousToken's dart carve-out in parser_reduce.go). The
	// other five are still collapsed by the hand-built tree above, but the
	// post-hoc patches that used to reconstruct them in
	// normalizeDartCollapsedLeafChildren have been removed: the reduce engine's
	// shouldKeepVisibleAnonymousTokenChild now keeps their anonymous token
	// children unconditionally on real parses (see
	// TestDartOperatorWrappersKeepTokenChildrenViaEngine and
	// TestDartNullableTypeAndNullLiteralRestoreAnonymousChildren in grammars/),
	// so finalNode/negationNode/relOpNode/gtNode/nullableNode/nullNode above
	// stay untouched by this call.
	//
	// "super"/"this" are likewise native materialization ownership now. A
	// caller-built childless result is intentionally left alone rather than
	// reconstructed from display names after raw child identity is gone.
	for _, node := range []*Node{finalNode, superNode, baseNode, thisNode, negationNode, relOpNode, gtNode, nullableNode, nullNode} {
		if node.ChildCount() != 0 {
			t.Fatalf("compatibility synthesized %d children for %s", node.ChildCount(), node.Type(lang))
		}
	}
}

func TestNormalizeDartComplexTypeArgumentFreeCallRestoresSelector(t *testing.T) {
	lang := &Language{
		Name: "dart",
		SymbolNames: []string{
			"EOF",
			"program",
			"initialized_identifier",
			"identifier",
			"=",
			"relational_expression",
			"relational_operator",
			"<",
			">",
			"selector",
			"unconditional_assignable_selector",
			".",
			"type_arguments",
			"type_identifier",
			"function_type",
			"Function",
			"parenthesized_expression",
			"arguments",
			"argument",
			"argument_part",
			"(",
			")",
			"string_literal",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "program", Visible: true, Named: true},
			{Name: "initialized_identifier", Visible: true, Named: true},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "=", Visible: true, Named: false},
			{Name: "relational_expression", Visible: true, Named: true},
			{Name: "relational_operator", Visible: true, Named: true},
			{Name: "<", Visible: true, Named: false},
			{Name: ">", Visible: true, Named: false},
			{Name: "selector", Visible: true, Named: true},
			{Name: "unconditional_assignable_selector", Visible: true, Named: true},
			{Name: ".", Visible: true, Named: false},
			{Name: "type_arguments", Visible: true, Named: true},
			{Name: "type_identifier", Visible: true, Named: true},
			{Name: "function_type", Visible: true, Named: true},
			{Name: "Function", Visible: true, Named: false},
			{Name: "parenthesized_expression", Visible: true, Named: true},
			{Name: "arguments", Visible: true, Named: true},
			{Name: "argument", Visible: true, Named: true},
			{Name: "argument_part", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: "string_literal", Visible: true, Named: true},
		},
	}
	sym := func(name string) Symbol {
		t.Helper()
		s, ok := lang.SymbolByName(name)
		if !ok {
			t.Fatalf("missing symbol %q", name)
		}
		return s
	}
	named := func(name string) bool {
		return symbolIsNamed(lang, sym(name))
	}
	leaf := func(name string, start, end uint32) *Node {
		return newLeafNodeInArena(nil, sym(name), named(name), start, end, Point{Column: start}, Point{Column: end})
	}
	parent := func(name string, children ...*Node) *Node {
		return newParentNodeInArena(nil, sym(name), named(name), children, nil, 0)
	}

	declName := leaf("identifier", 0, 1)
	eq := leaf("=", 2, 3)
	callee := leaf("identifier", 4, 11)
	lessOp := parent("relational_operator", leaf("<", 11, 12))
	ffi := leaf("identifier", 12, 15)
	property := parent("selector", parent("unconditional_assignable_selector", leaf(".", 15, 16), leaf("identifier", 16, 30)))
	genericReturn := parent("type_arguments", leaf("<", 40, 41), leaf("type_identifier", 41, 45), leaf(">", 45, 46))
	nestedTypeArgs := parent("selector", parent("type_arguments", leaf("<", 30, 31), parent("function_type", leaf("type_identifier", 31, 34), leaf(".", 34, 35), leaf("type_identifier", 35, 40), genericReturn, leaf("Function", 47, 55)), leaf(">", 55, 56)))
	left := parent("relational_expression", callee, lessOp, ffi, property, nestedTypeArgs)
	greaterOp := parent("relational_operator", leaf(">", 56, 57))
	args := parent("parenthesized_expression", leaf("(", 57, 58), leaf("string_literal", 58, 61), leaf(")", 61, 62))
	value := parent("relational_expression", left, greaterOp, args)
	init := parent("initialized_identifier", declName, eq, value)
	root := parent("program", init)
	source := make([]byte, 80)
	source[40] = '<'

	normalizeDartCompatibility(root, source, lang)

	if got := init.ChildCount(); got != 4 {
		t.Fatalf("initialized_identifier child count = %d, want 4; tree=%s", got, init.SExpr(lang))
	}
	if got := init.Child(2).Type(lang); got != "identifier" {
		t.Fatalf("callee child type = %q, want identifier; tree=%s", got, init.SExpr(lang))
	}
	selector := init.Child(3)
	if selector == nil || selector.Type(lang) != "selector" {
		t.Fatalf("call selector = %v, want selector; tree=%s", selector, init.SExpr(lang))
	}
	argPart := selector.NamedChild(0)
	if argPart == nil || argPart.Type(lang) != "argument_part" {
		t.Fatalf("argument part = %v, want argument_part; tree=%s", argPart, selector.SExpr(lang))
	}
}

func TestNormalizeDartComplexTypeArgumentFreeCallLeavesSingleTypeRelational(t *testing.T) {
	lang := &Language{
		Name: "dart",
		SymbolNames: []string{
			"EOF", "program", "relational_expression", "relational_operator", "identifier", "<", ">", "parenthesized_expression", "(", ")",
			"selector", "argument_part", "arguments", "argument", "type_arguments", "type_identifier",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "program", Visible: true, Named: true},
			{Name: "relational_expression", Visible: true, Named: true},
			{Name: "relational_operator", Visible: true, Named: true},
			{Name: "identifier", Visible: true, Named: true},
			{Name: "<", Visible: true, Named: false},
			{Name: ">", Visible: true, Named: false},
			{Name: "parenthesized_expression", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: "selector", Visible: true, Named: true},
			{Name: "argument_part", Visible: true, Named: true},
			{Name: "arguments", Visible: true, Named: true},
			{Name: "argument", Visible: true, Named: true},
			{Name: "type_arguments", Visible: true, Named: true},
			{Name: "type_identifier", Visible: true, Named: true},
		},
	}
	sym := func(name string) Symbol {
		t.Helper()
		s, ok := lang.SymbolByName(name)
		if !ok {
			t.Fatalf("missing symbol %q", name)
		}
		return s
	}
	leaf := func(name string, start, end uint32) *Node {
		return newLeafNodeInArena(nil, sym(name), symbolIsNamed(lang, sym(name)), start, end, Point{Column: start}, Point{Column: end})
	}
	parent := func(name string, children ...*Node) *Node {
		return newParentNodeInArena(nil, sym(name), symbolIsNamed(lang, sym(name)), children, nil, 0)
	}

	left := parent("relational_expression",
		leaf("identifier", 0, 6),
		parent("relational_operator", leaf("<", 6, 7)),
		leaf("identifier", 7, 11),
	)
	value := parent("relational_expression",
		left,
		parent("relational_operator", leaf(">", 11, 12)),
		parent("parenthesized_expression", leaf("(", 12, 13), leaf("identifier", 13, 14), leaf(")", 14, 15)),
	)
	root := parent("program", value)

	normalizeDartCompatibility(root, nil, lang)

	if got := root.Child(0).Type(lang); got != "relational_expression" {
		t.Fatalf("single-type free call was rewritten to %q, want relational_expression", got)
	}
}
