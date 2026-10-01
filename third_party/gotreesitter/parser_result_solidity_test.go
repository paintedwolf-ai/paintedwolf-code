package gotreesitter

import "testing"

// solidityMemberLang builds a minimal solidity-named Language whose symbol
// table contains just the node kinds the member-object normalizer inspects.
func solidityMemberLang() *Language {
	return &Language{
		Name:        "solidity",
		SymbolNames: []string{"EOF", "member_expression", "expression", "identifier", ".", "new_expression", "type_cast_expression", "call_expression", "type_name", "call_argument", "number_literal", "primitive_type"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "member_expression", Visible: true, Named: true},
			{Name: "expression", Visible: true, Named: true},
			{Name: "identifier", Visible: true, Named: true},
			{Name: ".", Visible: true, Named: false},
			{Name: "new_expression", Visible: true, Named: true},
			{Name: "type_cast_expression", Visible: true, Named: true},
			{Name: "call_expression", Visible: true, Named: true},
			{Name: "type_name", Visible: true, Named: true},
			{Name: "call_argument", Visible: true, Named: true},
			{Name: "number_literal", Visible: true, Named: true},
			{Name: "primitive_type", Visible: true, Named: true},
		},
	}
}

// TestNormalizeSolidityMemberObjectWrappersCollapsesUnaryExpression mirrors the
// C oracle: `super._transferOwnership` has a bare identifier object, not an
// `expression(identifier)` wrapper.
func TestNormalizeSolidityMemberObjectWrappersCollapsesUnaryExpression(t *testing.T) {
	lang := solidityMemberLang()
	arena := newNodeArena(arenaClassFull)

	// member_expression [ expression(identifier "super"), ".", identifier ]
	innerObj := newLeafNodeInArena(arena, 3, true, 2123, 2128, Point{Row: 0, Column: 2123}, Point{Row: 0, Column: 2128})
	exprWrap := newParentNodeInArena(arena, 2, true, []*Node{innerObj}, nil, 0)
	exprWrap.startByte, exprWrap.endByte = 2123, 2128
	dot := newLeafNodeInArena(arena, 4, false, 2128, 2129, Point{Row: 0, Column: 2128}, Point{Row: 0, Column: 2129})
	prop := newLeafNodeInArena(arena, 3, true, 2129, 2147, Point{Row: 0, Column: 2129}, Point{Row: 0, Column: 2147})
	member := newParentNodeInArena(arena, 1, true, []*Node{exprWrap, dot, prop}, nil, 0)

	normalizeSolidityMemberObjectWrappers(member, lang)

	if got, want := resultChildCount(member), 3; got != want {
		t.Fatalf("member child count = %d, want %d", got, want)
	}
	obj := member.Child(0)
	if got, want := obj.Type(lang), "identifier"; got != want {
		t.Fatalf("collapsed object type = %q, want %q", got, want)
	}
	if got, want := obj.StartByte(), uint32(2123); got != want {
		t.Fatalf("object StartByte = %d, want %d", got, want)
	}
	if got, want := obj.EndByte(), uint32(2128); got != want {
		t.Fatalf("object EndByte = %d, want %d", got, want)
	}
	if obj.parent != member {
		t.Fatal("collapsed object parent not reattached to member_expression")
	}
	if got, want := obj.childIndex, int32(0); got != want {
		t.Fatalf("collapsed object childIndex = %d, want %d", got, want)
	}
}

// TestNormalizeSolidityMemberObjectWrappersLeavesCompoundObject is a guard: an
// object operand that is itself a compound expression (more than one child, or
// a non-identifier inner node) must not be collapsed.
func TestNormalizeSolidityMemberObjectWrappersLeavesCompoundObject(t *testing.T) {
	lang := solidityMemberLang()
	arena := newNodeArena(arenaClassFull)

	// expression wrapping TWO children — not a pure unary identifier wrapper.
	c0 := newLeafNodeInArena(arena, 3, true, 0, 3, Point{}, Point{Row: 0, Column: 3})
	c1 := newLeafNodeInArena(arena, 3, true, 4, 7, Point{}, Point{Row: 0, Column: 7})
	exprWrap := newParentNodeInArena(arena, 2, true, []*Node{c0, c1}, nil, 0)
	exprWrap.startByte, exprWrap.endByte = 0, 7
	dot := newLeafNodeInArena(arena, 4, false, 7, 8, Point{}, Point{})
	prop := newLeafNodeInArena(arena, 3, true, 8, 11, Point{}, Point{})
	member := newParentNodeInArena(arena, 1, true, []*Node{exprWrap, dot, prop}, nil, 0)

	normalizeSolidityMemberObjectWrappers(member, lang)

	if got, want := member.Child(0).Type(lang), "expression"; got != want {
		t.Fatalf("compound object type = %q, want %q (must be untouched)", got, want)
	}
}

// TestNormalizeSolidityMemberObjectWrappersLeavesSpanMismatch is a guard: if the
// expression wrapper captures trivia beyond the inner identifier span, it is a
// meaningful node and must be left intact.
func TestNormalizeSolidityMemberObjectWrappersLeavesSpanMismatch(t *testing.T) {
	lang := solidityMemberLang()
	arena := newNodeArena(arenaClassFull)

	innerObj := newLeafNodeInArena(arena, 3, true, 1, 6, Point{}, Point{}) // inner [1:6]
	exprWrap := newParentNodeInArena(arena, 2, true, []*Node{innerObj}, nil, 0)
	exprWrap.startByte, exprWrap.endByte = 0, 6 // wrapper [0:6] — extends left
	dot := newLeafNodeInArena(arena, 4, false, 6, 7, Point{}, Point{})
	prop := newLeafNodeInArena(arena, 3, true, 7, 10, Point{}, Point{})
	member := newParentNodeInArena(arena, 1, true, []*Node{exprWrap, dot, prop}, nil, 0)

	normalizeSolidityMemberObjectWrappers(member, lang)

	if got, want := member.Child(0).Type(lang), "expression"; got != want {
		t.Fatalf("span-mismatched object type = %q, want %q (must be untouched)", got, want)
	}
}

func TestNormalizeSolidityCallExpressionAliasesWrapsCallShapedNodes(t *testing.T) {
	lang := solidityMemberLang()
	arena := newNodeArena(arenaClassFull)

	newKeyword := newLeafNodeInArena(arena, 3, true, 0, 3, Point{}, Point{})
	typeName := newParentNodeInArena(arena, 8, true, []*Node{
		newLeafNodeInArena(arena, 3, true, 4, 9, Point{}, Point{}),
	}, nil, 0)
	arg := newParentNodeInArena(arena, 9, true, []*Node{
		newLeafNodeInArena(arena, 10, true, 10, 11, Point{}, Point{}),
	}, nil, 0)
	ctor := newParentNodeInArena(arena, 5, true, []*Node{newKeyword, typeName, arg}, nil, 0)

	normalizeSolidityCallExpressionAliases(ctor, lang)

	if got, want := ctor.Type(lang), "call_expression"; got != want {
		t.Fatalf("constructor call type = %q, want %q", got, want)
	}
	if !ctor.IsNamed() {
		t.Fatal("constructor call alias lost named metadata")
	}
	if got, want := resultChildCount(ctor), 2; got != want {
		t.Fatalf("constructor call child count = %d, want %d", got, want)
	}
	wrapper := ctor.Child(0)
	if got, want := wrapper.Type(lang), "expression"; got != want {
		t.Fatalf("constructor callee wrapper type = %q, want %q", got, want)
	}
	inner := wrapper.Child(0)
	if got, want := inner.Type(lang), "new_expression"; got != want {
		t.Fatalf("constructor inner type = %q, want %q", got, want)
	}
	if got, want := resultChildCount(inner), 2; got != want {
		t.Fatalf("constructor inner child count = %d, want %d", got, want)
	}
	if ctor.Child(1) != arg {
		t.Fatal("call argument not preserved as outer call child")
	}
}

func TestNormalizeSolidityCallExpressionAliasesLeavesNonCallNewExpression(t *testing.T) {
	lang := solidityMemberLang()
	arena := newNodeArena(arenaClassFull)

	typeName := newParentNodeInArena(arena, 8, true, []*Node{
		newLeafNodeInArena(arena, 3, true, 4, 9, Point{}, Point{}),
	}, nil, 0)
	ctor := newParentNodeInArena(arena, 5, true, []*Node{typeName}, nil, 0)

	normalizeSolidityCallExpressionAliases(ctor, lang)

	if got, want := ctor.Type(lang), "new_expression"; got != want {
		t.Fatalf("non-call constructor type = %q, want %q", got, want)
	}
}

func TestNormalizeSolidityCallExpressionAliasesWrapsTypeCastCallee(t *testing.T) {
	lang := solidityMemberLang()
	arena := newNodeArena(arenaClassFull)

	primitive := newLeafNodeInArena(arena, 11, true, 0, 7, Point{}, Point{})
	openParen := newLeafNodeInArena(arena, 3, false, 7, 8, Point{}, Point{})
	arg := newParentNodeInArena(arena, 9, true, []*Node{
		newLeafNodeInArena(arena, 10, true, 8, 9, Point{}, Point{}),
	}, nil, 0)
	cast := newParentNodeInArena(arena, 6, true, []*Node{primitive, openParen, arg}, nil, 0)

	normalizeSolidityCallExpressionAliases(cast, lang)

	if got, want := cast.Type(lang), "call_expression"; got != want {
		t.Fatalf("type-cast call type = %q, want %q", got, want)
	}
	wrapper := cast.Child(0)
	if got, want := wrapper.Type(lang), "expression"; got != want {
		t.Fatalf("type-cast callee wrapper type = %q, want %q", got, want)
	}
	if got, want := wrapper.Child(0).Type(lang), "primitive_type"; got != want {
		t.Fatalf("type-cast callee type = %q, want %q", got, want)
	}
	if cast.Child(1) != openParen {
		t.Fatal("open paren was not preserved as an outer call child")
	}
	if cast.Child(2) != arg {
		t.Fatal("call argument was not preserved as an outer call child")
	}
}
