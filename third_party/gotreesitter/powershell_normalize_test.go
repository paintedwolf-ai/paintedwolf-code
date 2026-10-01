package gotreesitter

import "testing"

func TestBuildPowerShellVariableMemberAccessBuildsRecoveredPath(t *testing.T) {
	lang := &Language{
		Name: "powershell",
		SymbolNames: []string{
			"EOF", "member_access", "variable", "\\", ".", "member_name", "simple_name", "ERROR",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "member_access", Visible: true, Named: true},
			{Name: "variable", Visible: true, Named: true},
			{Name: "\\", Visible: true, Named: false},
			{Name: ".", Visible: true, Named: false},
			{Name: "member_name", Visible: true, Named: true},
			{Name: "simple_name", Visible: true, Named: true},
			{Name: "ERROR", Visible: true, Named: true},
		},
	}

	source := []byte("$targetPsHome\\pwrshplugin.dll")
	arena := newNodeArena(arenaClassFull)
	node := buildPowerShellVariableMemberAccess(arena, source, lang, 0, len(source))
	if node == nil {
		t.Fatal("node = nil")
	}
	if got, want := node.Type(lang), "member_access"; got != want {
		t.Fatalf("node.Type = %q, want %q", got, want)
	}
	if got, want := len(node.children), 4; got != want {
		t.Fatalf("len(node.children) = %d, want %d", got, want)
	}
	if got, want := node.children[1].Type(lang), "ERROR"; got != want {
		t.Fatalf("node.children[1].Type = %q, want %q", got, want)
	}
	if !node.children[1].isExtra() {
		t.Fatalf("node.children[1].isExtra = false, want true")
	}
}

func TestBuildPowerShellExpandableStringLiteralKeepsFullRangeWithVariable(t *testing.T) {
	lang := &Language{
		Name:        "powershell",
		SymbolNames: []string{"EOF", "expandable_string_literal", "variable"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "expandable_string_literal", Visible: true, Named: true},
			{Name: "variable", Visible: true, Named: true},
		},
	}

	source := []byte("\"Creating $pluginBasePath\"")
	arena := newNodeArena(arenaClassFull)
	node := buildPowerShellExpandableStringLiteral(arena, source, lang, 0, len(source))
	if node == nil {
		t.Fatal("node = nil")
	}
	if got, want := node.Type(lang), "expandable_string_literal"; got != want {
		t.Fatalf("node.Type = %q, want %q", got, want)
	}
	if got, want := node.startByte, uint32(0); got != want {
		t.Fatalf("node.startByte = %d, want %d", got, want)
	}
	if got, want := node.endByte, uint32(len(source)); got != want {
		t.Fatalf("node.endByte = %d, want %d", got, want)
	}
	if got, want := len(node.children), 1; got != want {
		t.Fatalf("len(node.children) = %d, want %d", got, want)
	}
	if got, want := node.children[0].Type(lang), "variable"; got != want {
		t.Fatalf("node.children[0].Type = %q, want %q", got, want)
	}
}

func TestBuildPowerShellTypeLiteralMarksRecoveredPlusTailExtra(t *testing.T) {
	lang := &Language{
		Name: "powershell",
		SymbolNames: []string{
			"EOF", "type_literal", "[", "]", "type_spec", "type_name", "type_identifier", ".", "+", "simple_name", "ERROR",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "type_literal", Visible: true, Named: true},
			{Name: "[", Visible: true, Named: false},
			{Name: "]", Visible: true, Named: false},
			{Name: "type_spec", Visible: true, Named: true},
			{Name: "type_name", Visible: true, Named: true},
			{Name: "type_identifier", Visible: true, Named: true},
			{Name: ".", Visible: true, Named: false},
			{Name: "+", Visible: true, Named: false},
			{Name: "simple_name", Visible: true, Named: true},
			{Name: "ERROR", Visible: true, Named: true},
		},
	}

	source := []byte("[System.Environment+SpecialFolder]")
	arena := newNodeArena(arenaClassFull)
	node := buildPowerShellTypeLiteral(arena, source, lang, 0, len(source))
	if node == nil {
		t.Fatal("node = nil")
	}
	if got, want := len(node.children), 4; got != want {
		t.Fatalf("len(node.children) = %d, want %d", got, want)
	}
	if got, want := node.children[2].Type(lang), "ERROR"; got != want {
		t.Fatalf("node.children[2].Type = %q, want %q", got, want)
	}
	if !node.children[2].isExtra() {
		t.Fatalf("node.children[2].isExtra = false, want true")
	}
	if !node.HasError() {
		t.Fatalf("node.HasError = false, want true")
	}
}

func TestNormalizePowerShellErrorProgramRootRetagsCompatibleRoot(t *testing.T) {
	lang := &Language{
		Name: "powershell",
		SymbolNames: []string{
			"EOF", "ERROR", "program", "comment", "param_block", "statement_list",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "ERROR", Visible: true, Named: true},
			{Name: "program", Visible: true, Named: true},
			{Name: "comment", Visible: true, Named: true},
			{Name: "param_block", Visible: true, Named: true},
			{Name: "statement_list", Visible: true, Named: true},
		},
	}
	arena := newNodeArena(arenaClassFull)
	children := []*Node{
		newLeafNodeInArena(arena, 3, true, 0, 9, Point{}, Point{Column: 9}),
		newLeafNodeInArena(arena, 4, true, 10, 20, Point{Column: 10}, Point{Column: 20}),
		newLeafNodeInArena(arena, 5, true, 22, 40, Point{Column: 22}, Point{Column: 40}),
	}
	root := newParentNodeInArena(arena, 1, true, children, nil, 0)

	normalizePowerShellErrorProgramRoot(root, lang)

	if got := root.Type(lang); got != "program" {
		t.Fatalf("root type = %q, want program", got)
	}
	if !root.IsNamed() {
		t.Fatal("root is not named")
	}
}

func TestNormalizePowerShellPathCommandNameWrapsVariable(t *testing.T) {
	lang := &Language{
		Name: "powershell",
		SymbolNames: []string{
			"EOF", "program", "command_name", "variable", "path_command_name",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "program", Visible: true, Named: true},
			{Name: "command_name", Visible: true, Named: true},
			{Name: "variable", Visible: true, Named: true},
			{Name: "path_command_name", Visible: true, Named: true},
		},
	}

	source := []byte("& $sb")
	arena := newNodeArena(arenaClassFull)
	variable := newLeafNodeInArena(arena, 3, true, 2, uint32(len(source)), Point{Column: 2}, Point{Column: uint32(len(source))})
	commandName := newParentNodeInArena(arena, 2, true, []*Node{variable}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{commandName}, nil, 0)

	normalizePowerShellPathCommandNameVariables(root, source, lang)

	wrapped := commandName.children[0]
	if got, want := wrapped.Type(lang), "path_command_name"; got != want {
		t.Fatalf("wrapped.Type = %q, want %q", got, want)
	}
	if got, want := len(wrapped.children), 1; got != want {
		t.Fatalf("len(wrapped.children) = %d, want %d", got, want)
	}
	if got, want := wrapped.children[0].Type(lang), "variable"; got != want {
		t.Fatalf("wrapped.children[0].Type = %q, want %q", got, want)
	}
	if got, want := wrapped.startByte, uint32(2); got != want {
		t.Fatalf("wrapped.startByte = %d, want %d", got, want)
	}
	if got, want := wrapped.endByte, uint32(len(source)); got != want {
		t.Fatalf("wrapped.endByte = %d, want %d", got, want)
	}
}

func TestNormalizePowerShellEnumStatementKeywordSpan(t *testing.T) {
	lang := &Language{
		Name: "powershell",
		SymbolNames: []string{
			"EOF", "program", "statement_list", "enum_statement", "simple_name", "{", "}",
		},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "program", Visible: true, Named: true},
			{Name: "statement_list", Visible: true, Named: true},
			{Name: "enum_statement", Visible: true, Named: true},
			{Name: "simple_name", Visible: true, Named: true},
			{Name: "{", Visible: true, Named: false},
			{Name: "}", Visible: true, Named: false},
		},
	}

	source := []byte("enum LogLevel\n{}")
	arena := newNodeArena(arenaClassFull)
	name := newLeafNodeInArena(arena, 4, true, 5, 13, Point{Column: 5}, Point{Column: 13})
	open := newLeafNodeInArena(arena, 5, false, 14, 15, Point{Row: 1, Column: 0}, Point{Row: 1, Column: 1})
	close := newLeafNodeInArena(arena, 6, false, 15, 16, Point{Row: 1, Column: 1}, Point{Row: 1, Column: 2})
	enum := newParentNodeInArena(arena, 3, true, []*Node{name, open, close}, nil, 0)
	list := newParentNodeInArena(arena, 2, true, []*Node{enum}, nil, 0)
	root := newParentNodeInArena(arena, 1, true, []*Node{list}, nil, 0)

	normalizePowerShellEnumStatementKeywordSpans(root, source, lang)

	if got, want := enum.startByte, uint32(0); got != want {
		t.Fatalf("enum.startByte = %d, want %d", got, want)
	}
	if got, want := enum.startPoint, (Point{}); got != want {
		t.Fatalf("enum.startPoint = %+v, want %+v", got, want)
	}
	if got, want := enum.endByte, uint32(len(source)); got != want {
		t.Fatalf("enum.endByte = %d, want %d", got, want)
	}
}
