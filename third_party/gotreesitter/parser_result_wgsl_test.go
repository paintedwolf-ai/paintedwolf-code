package gotreesitter

import "testing"

func TestNormalizeWGSLEmptyReturnSemicolonRecovery(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	open := newLeafNodeInArena(arena, 3, false, 0, 1, Point{}, Point{Column: 1})
	inc := newLeafNodeInArena(arena, 5, true, 4, 14, Point{Column: 4}, Point{Column: 14})
	missingReturn := newLeafNodeInArena(arena, 7, false, 14, 14, Point{Column: 14}, Point{Column: 14})
	missingReturn.setMissing(true)
	emptyReturn := newParentNodeInArena(arena, 6, true, []*Node{missingReturn}, nil, 0)
	semi := newLeafNodeInArena(arena, 8, false, 14, 15, Point{Column: 14}, Point{Column: 15})
	close := newLeafNodeInArena(arena, 4, false, 18, 19, Point{Column: 18}, Point{Column: 19})
	block := newParentNodeInArena(arena, 2, true, []*Node{open, inc, emptyReturn, semi, close}, nil, 0)

	normalizeWGSLCompatibility(block, lang)

	if got, want := block.ChildCount(), 4; got != want {
		t.Fatalf("compound child count = %d, want %d", got, want)
	}
	err := block.Child(2)
	if err == nil {
		t.Fatal("recovery child = nil")
	}
	if got, want := err.Type(lang), "ERROR"; got != want {
		t.Fatalf("recovery child type = %q, want %q", got, want)
	}
	if got, want := err.StartByte(), uint32(14); got != want {
		t.Fatalf("ERROR start = %d, want %d", got, want)
	}
	if got, want := err.EndByte(), uint32(15); got != want {
		t.Fatalf("ERROR end = %d, want %d", got, want)
	}
	if got, want := err.ChildCount(), 1; got != want {
		t.Fatalf("ERROR child count = %d, want %d", got, want)
	}
	if child := err.Child(0); child == nil || child.Type(lang) != ";" {
		t.Fatalf("ERROR child = %#v, want semicolon", child)
	}
	if !err.HasError() {
		t.Fatal("ERROR node should carry has_error")
	}
}

func TestNormalizeWGSLArgumentListErrorWrapper(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	open := newLeafNodeInArena(arena, 11, false, 0, 1, Point{}, Point{Column: 1})
	x := newLeafNodeInArena(arena, 16, true, 1, 2, Point{Column: 1}, Point{Column: 2})
	comma := newLeafNodeInArena(arena, 17, false, 2, 3, Point{Column: 2}, Point{Column: 3})
	y := newLeafNodeInArena(arena, 16, true, 4, 5, Point{Column: 4}, Point{Column: 5})
	err := newParentNodeInArena(arena, errorSymbol, true, []*Node{x, comma, y}, nil, 0)
	err.setHasError(true)
	close := newLeafNodeInArena(arena, 12, false, 5, 6, Point{Column: 5}, Point{Column: 6})
	args := newParentNodeInArena(arena, 10, true, []*Node{open, err, close}, nil, 0)
	args.setHasError(true)
	call := newParentNodeInArena(arena, 9, true, []*Node{
		newLeafNodeInArena(arena, 15, false, 0, 0, Point{}, Point{}),
		args,
	}, nil, 0)
	call.setHasError(true)

	normalizeWGSLCompatibility(call, lang)

	if got, want := args.ChildCount(), 5; got != want {
		t.Fatalf("argument child count = %d, want %d", got, want)
	}
	for i, want := range []string{"(", "identifier", ",", "identifier", ")"} {
		if got := args.Child(i).Type(lang); got != want {
			t.Fatalf("argument child %d type = %q, want %q", i, got, want)
		}
	}
	if args.HasError() || call.HasError() {
		t.Fatalf("repaired argument list should be clean: args=%v call=%v", args.HasError(), call.HasError())
	}
}

func TestNormalizeWGSLTrailingArgumentMissingIdentifier(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	open := newLeafNodeInArena(arena, 11, false, 0, 1, Point{}, Point{Column: 1})
	value := newLeafNodeInArena(arena, 16, true, 1, 2, Point{Column: 1}, Point{Column: 2})
	comma := newLeafNodeInArena(arena, 17, false, 2, 3, Point{Column: 2}, Point{Column: 3})
	missing := newLeafNodeInArena(arena, 16, true, 3, 3, Point{Column: 3}, Point{Column: 3})
	missing.setMissing(true)
	close := newLeafNodeInArena(arena, 12, false, 6, 7, Point{Column: 6}, Point{Column: 7})
	args := newParentNodeInArena(arena, 10, true, []*Node{open, value, comma, missing, close}, nil, 0)
	args.setHasError(true)

	normalizeWGSLCompatibility(args, lang)

	if got, want := args.ChildCount(), 4; got != want {
		t.Fatalf("argument child count = %d, want %d", got, want)
	}
	for i, want := range []string{"(", "identifier", ",", ")"} {
		if got := args.Child(i).Type(lang); got != want {
			t.Fatalf("argument child %d type = %q, want %q", i, got, want)
		}
	}
	if args.HasError() {
		t.Fatal("argument list with removed missing identifier should be clean")
	}
}

func TestNormalizeWGSLAtomicArrayRecovery(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	array := newLeafNodeInArena(arena, 20, false, 0, 5, Point{}, Point{Column: 5})
	open := newLeafNodeInArena(arena, 13, false, 5, 6, Point{Column: 5}, Point{Column: 6})
	atomicIdent := newLeafNodeInArena(arena, 16, true, 6, 12, Point{Column: 6}, Point{Column: 12})
	atomicType := newParentNodeInArena(arena, 14, true, []*Node{atomicIdent}, nil, 0)
	err := newParentNodeInArena(arena, errorSymbol, true, []*Node{open, atomicType}, nil, 0)
	err.setHasError(true)
	nestedOpen := newLeafNodeInArena(arena, 13, false, 12, 13, Point{Column: 12}, Point{Column: 13})
	u32 := newLeafNodeInArena(arena, 21, false, 13, 16, Point{Column: 13}, Point{Column: 16})
	u32Type := newParentNodeInArena(arena, 14, true, []*Node{u32}, nil, 0)
	close := newLeafNodeInArena(arena, 19, false, 16, 17, Point{Column: 16}, Point{Column: 17})
	typ := newParentNodeInArena(arena, 14, true, []*Node{array, err, nestedOpen, u32Type, close}, nil, 0)
	typ.setHasError(true)

	normalizeWGSLCompatibility(typ, lang)

	if got, want := typ.ChildCount(), 5; got != want {
		t.Fatalf("type_declaration child count = %d, want %d", got, want)
	}
	for i, want := range []string{"array", "<", "ERROR", "type_declaration", ">"} {
		if got := typ.Child(i).Type(lang); got != want {
			t.Fatalf("type child %d type = %q, want %q", i, got, want)
		}
	}
	recovered := typ.Child(2)
	if got, want := recovered.StartByte(), uint32(6); got != want {
		t.Fatalf("ERROR start = %d, want %d", got, want)
	}
	if got, want := recovered.EndByte(), uint32(13); got != want {
		t.Fatalf("ERROR end = %d, want %d", got, want)
	}
	if !typ.HasError() || !recovered.HasError() {
		t.Fatalf("type_declaration and ERROR should carry has_error: type=%v error=%v", typ.HasError(), recovered.HasError())
	}
	if !recovered.IsExtra() {
		t.Fatal("C-compatible atomic recovery ERROR should be extra")
	}
	if typ.Child(1).HasError() || recovered.Child(0).HasError() || recovered.Child(1).HasError() {
		t.Fatal("C-compatible atomic recovery leaves/token children should not carry has_error")
	}
}

func TestNormalizeWGSLRecoveredCallLHSWrapper(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	ident := newLeafNodeInArena(arena, 16, true, 0, 6, Point{}, Point{Column: 6})
	lhsSym, lhsNamed, _ := symbolMeta(lang, "lhs_expression")
	lhs := newParentNodeInArena(arena, lhsSym, lhsNamed, []*Node{ident}, nil, 0)
	open := newLeafNodeInArena(arena, 11, false, 6, 7, Point{Column: 6}, Point{Column: 7})
	close := newLeafNodeInArena(arena, 12, false, 7, 8, Point{Column: 7}, Point{Column: 8})
	err := newParentNodeInArena(arena, errorSymbol, true, []*Node{lhs, open, close}, nil, 0)
	err.setHasError(true)

	normalizeWGSLCompatibility(err, lang)

	if got, want := err.ChildCount(), 3; got != want {
		t.Fatalf("ERROR child count = %d, want %d", got, want)
	}
	if child := err.Child(0); child == nil || child.Type(lang) != "identifier" {
		t.Fatalf("first ERROR child = %#v, want identifier", child)
	}
	if err.Child(0).StartByte() != 0 || err.Child(0).EndByte() != 6 {
		t.Fatalf("identifier span = [%d:%d], want [0:6]", err.Child(0).StartByte(), err.Child(0).EndByte())
	}
}

func TestNormalizeWGSLConstAssignmentRecovery(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	open := newLeafNodeInArena(arena, 3, false, 0, 1, Point{}, Point{Column: 1})
	constIdent := newLeafNodeInArena(arena, 16, true, 2, 7, Point{Column: 2}, Point{Column: 7})
	constErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{constIdent}, nil, 0)
	constErr.setHasError(true)
	name := newLeafNodeInArena(arena, 16, true, 8, 9, Point{Column: 8}, Point{Column: 9})
	lhsSym, lhsNamed, _ := symbolMeta(lang, "lhs_expression")
	nameLHS := newParentNodeInArena(arena, lhsSym, lhsNamed, []*Node{name}, nil, 0)
	eqSym, eqNamed, _ := symbolMeta(lang, "=")
	eq := newLeafNodeInArena(arena, eqSym, eqNamed, 10, 11, Point{Column: 10}, Point{Column: 11})
	value := newLeafNodeInArena(arena, 16, true, 12, 13, Point{Column: 12}, Point{Column: 13})
	assignSym, assignNamed, _ := symbolMeta(lang, "assignment_statement")
	assign := newParentNodeInArena(arena, assignSym, assignNamed, []*Node{nameLHS, eq, value}, nil, 0)
	semi := newLeafNodeInArena(arena, 8, false, 13, 14, Point{Column: 13}, Point{Column: 14})
	close := newLeafNodeInArena(arena, 4, false, 15, 16, Point{Column: 15}, Point{Column: 16})
	block := newParentNodeInArena(arena, 2, true, []*Node{open, constErr, assign, semi, close}, nil, 0)
	block.setHasError(true)

	normalizeWGSLCompatibility(block, lang)

	if got, want := block.ChildCount(), 4; got != want {
		t.Fatalf("compound child count = %d, want %d", got, want)
	}
	rewritten := block.Child(1)
	if rewritten == nil || rewritten.Type(lang) != "assignment_statement" {
		t.Fatalf("child 1 = %#v, want assignment_statement", rewritten)
	}
	if got, want := rewritten.StartByte(), uint32(2); got != want {
		t.Fatalf("assignment start = %d, want %d", got, want)
	}
	for i, want := range []string{"lhs_expression", "ERROR", "=", "identifier"} {
		if got := rewritten.Child(i).Type(lang); got != want {
			t.Fatalf("assignment child %d type = %q, want %q", i, got, want)
		}
	}
	nameErr := rewritten.Child(1)
	if !nameErr.IsExtra() || !nameErr.HasError() {
		t.Fatalf("name ERROR flags: extra=%v hasError=%v, want both true", nameErr.IsExtra(), nameErr.HasError())
	}
	if child := nameErr.Child(0); child == nil || child.Type(lang) != "identifier" || child.StartByte() != 8 {
		t.Fatalf("name ERROR child = %#v, want identifier at byte 8", child)
	}
}

func TestNormalizeWGSLRecoveredCallAssignment(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	identSym, identNamed, _ := symbolMeta(lang, "identifier")
	lhsSym, lhsNamed, _ := symbolMeta(lang, "lhs_expression")
	postfixSym, postfixNamed, _ := symbolMeta(lang, "postfix_expression")
	openSym, openNamed, _ := symbolMeta(lang, "(")
	closeSym, closeNamed, _ := symbolMeta(lang, ")")
	commaSym, commaNamed, _ := symbolMeta(lang, ",")
	dotSym, dotNamed, _ := symbolMeta(lang, ".")
	starSym, starNamed, _ := symbolMeta(lang, "*")
	semiSym, semiNamed, _ := symbolMeta(lang, ";")

	callee := newLeafNodeInArena(arena, identSym, identNamed, 0, 10, Point{}, Point{Column: 10})
	open := newLeafNodeInArena(arena, openSym, openNamed, 10, 11, Point{Column: 10}, Point{Column: 11})
	hit0 := newLeafNodeInArena(arena, identSym, identNamed, 11, 14, Point{Column: 11}, Point{Column: 14})
	dot0 := newLeafNodeInArena(arena, dotSym, dotNamed, 14, 15, Point{Column: 14}, Point{Column: 15})
	uv := newLeafNodeInArena(arena, identSym, identNamed, 15, 17, Point{Column: 15}, Point{Column: 17})
	comma0 := newLeafNodeInArena(arena, commaSym, commaNamed, 17, 18, Point{Column: 17}, Point{Column: 18})
	fieldErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{uv, comma0}, nil, 0)
	fieldErr.setHasError(true)
	hit1 := newLeafNodeInArena(arena, identSym, identNamed, 19, 22, Point{Column: 19}, Point{Column: 22})
	dot1 := newLeafNodeInArena(arena, dotSym, dotNamed, 22, 23, Point{Column: 22}, Point{Column: 23})
	quad := newLeafNodeInArena(arena, identSym, identNamed, 23, 27, Point{Column: 23}, Point{Column: 27})
	postfix1 := newParentNodeInArena(arena, postfixSym, postfixNamed, []*Node{dot1, quad}, nil, 0)
	postfix0 := newParentNodeInArena(arena, postfixSym, postfixNamed, []*Node{dot0, fieldErr, hit1, postfix1}, nil, 0)
	postfix0.setHasError(true)
	firstLHS := newParentNodeInArena(arena, lhsSym, lhsNamed, []*Node{hit0, postfix0}, nil, 0)
	firstLHS.setHasError(true)
	comma1 := newLeafNodeInArena(arena, commaSym, commaNamed, 27, 28, Point{Column: 27}, Point{Column: 28})
	color := newLeafNodeInArena(arena, identSym, identNamed, 29, 34, Point{Column: 29}, Point{Column: 34})
	argErr := newParentNodeInArena(arena, errorSymbol, true, []*Node{firstLHS, comma1, color}, nil, 0)
	argErr.setHasError(true)
	star := newLeafNodeInArena(arena, starSym, starNamed, 35, 36, Point{Column: 35}, Point{Column: 36})
	light := newLeafNodeInArena(arena, identSym, identNamed, 37, 52, Point{Column: 37}, Point{Column: 52})
	tailLHS := newParentNodeInArena(arena, lhsSym, lhsNamed, []*Node{star, light}, nil, 0)
	close := newLeafNodeInArena(arena, closeSym, closeNamed, 52, 53, Point{Column: 52}, Point{Column: 53})
	parenLHS := newParentNodeInArena(arena, lhsSym, lhsNamed, []*Node{open, argErr, tailLHS, close}, nil, 0)
	parenLHS.setHasError(true)
	semi := newLeafNodeInArena(arena, semiSym, semiNamed, 53, 54, Point{Column: 53}, Point{Column: 54})
	err := newParentNodeInArena(arena, errorSymbol, true, []*Node{callee, parenLHS, semi}, nil, 0)
	err.setHasError(true)
	block := newParentNodeInArena(arena, 2, true, []*Node{err}, nil, 0)
	block.setHasError(true)

	normalizeWGSLCompatibility(block, lang)

	if got, want := block.ChildCount(), 2; got != want {
		t.Fatalf("compound child count = %d, want %d", got, want)
	}
	assign := block.Child(0)
	if assign == nil || assign.Type(lang) != "assignment_statement" {
		t.Fatalf("child 0 = %#v, want assignment_statement", assign)
	}
	if block.Child(1) != semi {
		t.Fatal("semicolon should be split out after recovered assignment")
	}
	if got, want := assign.ChildCount(), 3; got != want {
		t.Fatalf("assignment child count = %d, want %d", got, want)
	}
	for i, want := range []string{"lhs_expression", "compound_assignment_operator", "parenthesized_expression"} {
		if got := assign.Child(i).Type(lang); got != want {
			t.Fatalf("assignment child %d type = %q, want %q", i, got, want)
		}
	}
	op := assign.Child(1)
	plusEq := op.Child(0)
	if plusEq == nil || plusEq.Type(lang) != "+=" || !plusEq.IsMissing() {
		t.Fatalf("missing operator = %#v, want missing +=", plusEq)
	}
	if plusEq.StartByte() != 10 || plusEq.EndByte() != 10 {
		t.Fatalf("missing operator span = [%d:%d], want [10:10]", plusEq.StartByte(), plusEq.EndByte())
	}
	paren := assign.Child(2)
	if got, want := paren.Child(1).Type(lang), "binary_expression"; got != want {
		t.Fatalf("parenthesized child 1 = %q, want %q", got, want)
	}
	binary := paren.Child(1)
	if got, want := binary.Child(0).Type(lang), "composite_value_decomposition_expression"; got != want {
		t.Fatalf("binary child 0 = %q, want %q", got, want)
	}
	if !assign.HasError() || !paren.HasError() || !binary.HasError() {
		t.Fatalf("recovered nodes should carry has_error: assign=%v paren=%v binary=%v",
			assign.HasError(), paren.HasError(), binary.HasError())
	}
}

func TestNormalizeWGSLRecoveredU32CallArgument(t *testing.T) {
	lang := testWGSLLanguage()
	arena := newNodeArena(arenaClassFull)
	openSym, openNamed, _ := symbolMeta(lang, "(")
	closeSym, closeNamed, _ := symbolMeta(lang, ")")
	commaSym, commaNamed, _ := symbolMeta(lang, ",")
	identSym, identNamed, _ := symbolMeta(lang, "identifier")
	u32Sym, u32Named, _ := symbolMeta(lang, "u32")
	subscriptSym, subscriptNamed, _ := symbolMeta(lang, "subscript_expression")
	compositeSym, compositeNamed, _ := symbolMeta(lang, "composite_value_decomposition_expression")
	parenSym, parenNamed, _ := symbolMeta(lang, "parenthesized_expression")
	dotSym, dotNamed, _ := symbolMeta(lang, ".")

	openOuter := newLeafNodeInArena(arena, openSym, openNamed, 0, 1, Point{}, Point{Column: 1})
	subscript := newParentNodeInArena(arena, subscriptSym, subscriptNamed, []*Node{
		newLeafNodeInArena(arena, identSym, identNamed, 1, 13, Point{Column: 1}, Point{Column: 13}),
	}, nil, 0)
	comma := newLeafNodeInArena(arena, commaSym, commaNamed, 13, 14, Point{Column: 13}, Point{Column: 14})
	u32 := newLeafNodeInArena(arena, u32Sym, u32Named, 15, 18, Point{Column: 15}, Point{Column: 18})
	err := newParentNodeInArena(arena, errorSymbol, true, []*Node{subscript, comma, u32}, nil, 0)
	err.setHasError(true)
	openArgs := newLeafNodeInArena(arena, openSym, openNamed, 18, 19, Point{Column: 18}, Point{Column: 19})
	value := newLeafNodeInArena(arena, identSym, identNamed, 19, 25, Point{Column: 19}, Point{Column: 25})
	dot := newLeafNodeInArena(arena, dotSym, dotNamed, 25, 26, Point{Column: 25}, Point{Column: 26})
	field := newLeafNodeInArena(arena, identSym, identNamed, 26, 27, Point{Column: 26}, Point{Column: 27})
	arg := newParentNodeInArena(arena, compositeSym, compositeNamed, []*Node{value, dot, field}, nil, 0)
	closeArgs := newLeafNodeInArena(arena, closeSym, closeNamed, 27, 28, Point{Column: 27}, Point{Column: 28})
	argsParen := newParentNodeInArena(arena, parenSym, parenNamed, []*Node{openArgs, arg, closeArgs}, nil, 0)
	closeOuter := newLeafNodeInArena(arena, closeSym, closeNamed, 28, 29, Point{Column: 28}, Point{Column: 29})
	outer := newParentNodeInArena(arena, parenSym, parenNamed, []*Node{openOuter, err, argsParen, closeOuter}, nil, 0)
	outer.setHasError(true)

	normalizeWGSLCompatibility(outer, lang)

	if got, want := outer.ChildCount(), 4; got != want {
		t.Fatalf("parenthesized child count = %d, want %d", got, want)
	}
	if got, want := outer.Child(1).Type(lang), "ERROR"; got != want {
		t.Fatalf("child 1 type = %q, want %q", got, want)
	}
	if got, want := outer.Child(1).ChildCount(), 2; got != want {
		t.Fatalf("trimmed ERROR child count = %d, want %d", got, want)
	}
	call := outer.Child(2)
	if call == nil || call.Type(lang) != "type_constructor_or_function_call_expression" {
		t.Fatalf("child 2 = %#v, want type constructor call", call)
	}
	if got, want := call.Child(0).Type(lang), "type_declaration"; got != want {
		t.Fatalf("call child 0 = %q, want %q", got, want)
	}
	if got, want := call.Child(1).Type(lang), "argument_list_expression"; got != want {
		t.Fatalf("call child 1 = %q, want %q", got, want)
	}
	if child := call.Child(0).Child(0); child != u32 {
		t.Fatalf("type declaration child = %#v, want original u32", child)
	}
}

func testWGSLLanguage() *Language {
	return &Language{
		Name: "wgsl",
		SymbolNames: []string{
			"EOF",
			"source_file",
			"compound_statement",
			"{",
			"}",
			"increment_statement",
			"return_statement",
			"return",
			";",
			"type_constructor_or_function_call_expression",
			"argument_list_expression",
			"(",
			")",
			"<",
			"type_declaration",
			"vec4",
			"identifier",
			",",
			"ERROR",
			">",
			"array",
			"u32",
			"assignment_statement",
			"lhs_expression",
			"=",
			"compound_assignment_operator",
			"+=",
			"parenthesized_expression",
			"binary_expression",
			"composite_value_decomposition_expression",
			"postfix_expression",
			"*",
			".",
			"subscript_expression",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF"},
			{Name: "source_file", Visible: true, Named: true},
			{Name: "compound_statement", Visible: true, Named: true},
			{Name: "{", Visible: true, Named: false},
			{Name: "}", Visible: true, Named: false},
			{Name: "increment_statement", Visible: true, Named: true},
			{Name: "return_statement", Visible: true, Named: true},
			{Name: "return", Visible: true, Named: false},
			{Name: ";", Visible: true, Named: false},
			{Name: "type_constructor_or_function_call_expression", Visible: true, Named: true},
			{Name: "argument_list_expression", Visible: true, Named: true},
			{Name: "(", Visible: true, Named: false},
			{Name: ")", Visible: true, Named: false},
			{Name: "<", Visible: true, Named: false},
			{Name: "type_declaration", Visible: true, Named: true},
			{Name: "vec4", Visible: true, Named: false},
			{Name: "identifier", Visible: true, Named: true},
			{Name: ",", Visible: true, Named: false},
			{Name: "ERROR", Visible: true, Named: true},
			{Name: ">", Visible: true, Named: false},
			{Name: "array", Visible: true, Named: false},
			{Name: "u32", Visible: true, Named: false},
			{Name: "assignment_statement", Visible: true, Named: true},
			{Name: "lhs_expression", Visible: true, Named: true},
			{Name: "=", Visible: true, Named: false},
			{Name: "compound_assignment_operator", Visible: true, Named: true},
			{Name: "+=", Visible: true, Named: false},
			{Name: "parenthesized_expression", Visible: true, Named: true},
			{Name: "binary_expression", Visible: true, Named: true},
			{Name: "composite_value_decomposition_expression", Visible: true, Named: true},
			{Name: "postfix_expression", Visible: true, Named: true},
			{Name: "*", Visible: true, Named: false},
			{Name: ".", Visible: true, Named: false},
			{Name: "subscript_expression", Visible: true, Named: true},
		},
	}
}
