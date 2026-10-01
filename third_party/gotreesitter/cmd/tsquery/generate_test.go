package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestGenerateSinglePattern(t *testing.T) {
	query := `(function_declaration
  name: (identifier) @name
  parameters: (parameter_list) @params
  body: (block) @body)`

	code, err := Generate(query, "GoFunctions", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	// Should have a struct with Name, Params, Body fields.
	if !strings.Contains(code, "type FunctionDeclarationMatch struct") {
		t.Error("expected FunctionDeclarationMatch struct")
	}
	if !strings.Contains(code, "Name *gotreesitter.Node") {
		t.Error("expected Name field")
	}
	if !strings.Contains(code, "Params *gotreesitter.Node") {
		t.Error("expected Params field")
	}
	if !strings.Contains(code, "Body *gotreesitter.Node") {
		t.Error("expected Body field")
	}

	// Should have constructor.
	if !strings.Contains(code, "func NewGoFunctionsQuery(lang *gotreesitter.Language)") {
		t.Error("expected NewGoFunctionsQuery constructor")
	}

	// Should have Exec method.
	if !strings.Contains(code, "func (q *GoFunctionsQuery) Exec(") {
		t.Error("expected Exec method")
	}

	// Single-pattern should have direct Next() returning typed struct.
	if !strings.Contains(code, "func (c *GoFunctionsQueryCursor) Next() (FunctionDeclarationMatch, bool)") {
		t.Error("expected typed Next() method")
	}
}

func TestGenerateMultiPattern(t *testing.T) {
	query := `(function_declaration name: (identifier) @name)
(method_declaration name: (field_identifier) @name)`

	code, err := Generate(query, "Declarations", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(code, "type FunctionDeclarationMatch struct") {
		t.Error("expected FunctionDeclarationMatch struct")
	}
	if !strings.Contains(code, "type MethodDeclarationMatch struct") {
		t.Error("expected MethodDeclarationMatch struct")
	}

	// Multi-pattern should use raw match + MatchPatternN helpers.
	if !strings.Contains(code, "func MatchPattern0(") {
		t.Error("expected MatchPattern0 function")
	}
	if !strings.Contains(code, "func MatchPattern1(") {
		t.Error("expected MatchPattern1 function")
	}
}

func TestGenerateDottedCaptureNames(t *testing.T) {
	query := `(call_expression function: (identifier) @function.name arguments: (argument_list) @function.args)`

	code, err := Generate(query, "Calls", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(code, "FunctionName *gotreesitter.Node") {
		t.Error("expected FunctionName field (dotted capture → PascalCase)")
	}
	if !strings.Contains(code, "FunctionArgs *gotreesitter.Node") {
		t.Error("expected FunctionArgs field")
	}
}

func TestGeneratePredicatesPreserved(t *testing.T) {
	query := `(identifier) @name (#eq? @name "main")`

	code, err := Generate(query, "MainIdent", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	// The query source should be in the generated code (for compilation).
	if !strings.Contains(code, `#eq? @name "main"`) {
		t.Error("expected predicate preserved in query source")
	}
}

func TestGenerateEmptyQuery(t *testing.T) {
	_, err := Generate("", "Empty", "queries", "go")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestGenerateCommentOnlyQuery(t *testing.T) {
	_, err := Generate("; just a comment\n; another\n", "Comment", "queries", "go")
	if err == nil {
		t.Fatal("expected error for comment-only query")
	}
}

func TestCaptureToFieldName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"name", "Name"},
		{"function.name", "FunctionName"},
		{"injection.content", "InjectionContent"},
		{"a.b.c", "ABC"},
		{"snake_case", "SnakeCase"},
		{"kebab-case", "KebabCase"},
	}
	for _, tt := range tests {
		got := captureToFieldName(tt.input)
		if got != tt.want {
			t.Errorf("captureToFieldName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"go_functions", "GoFunctions"},
		{"my-query", "MyQuery"},
		{"simple", "Simple"},
		{"a_b_c", "ABC"},
	}
	for _, tt := range tests {
		got := toPascalCase(tt.input)
		if got != tt.want {
			t.Errorf("toPascalCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGeneratePackageDecl(t *testing.T) {
	query := `(identifier) @name`

	code, err := Generate(query, "Test", "mypackage", "go")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(code, "package mypackage") {
		t.Error("expected correct package declaration")
	}
}

func TestGenerateDoNotEditHeader(t *testing.T) {
	query := `(identifier) @name`

	code, err := Generate(query, "Test", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(code, "DO NOT EDIT") {
		t.Error("expected DO NOT EDIT header")
	}
}

func TestGoStringLiteralQuotedFallbackEscapesCarriageReturn(t *testing.T) {
	input := "line1`\r\nline2\t\\\""
	lit := goStringLiteral(input)
	if !strings.HasPrefix(lit, "\"") || !strings.HasSuffix(lit, "\"") {
		t.Fatalf("expected quoted-string fallback, got %q", lit)
	}
	if !strings.Contains(lit, `\r`) {
		t.Fatalf("expected carriage return escape in literal, got %q", lit)
	}
	got, err := strconv.Unquote(lit)
	if err != nil {
		t.Fatalf("Unquote(%q): %v", lit, err)
	}
	if got != input {
		t.Fatalf("roundtrip mismatch: got %q, want %q", got, input)
	}
}

func TestExtractPatternsWithPredicates(t *testing.T) {
	query := `(string_content) @injection.content (#set! injection.language "javascript")`

	patterns, err := extractPatterns(query)
	if err != nil {
		t.Fatal(err)
	}

	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}

	p := patterns[0]
	if p.RootNodeType != "string_content" {
		t.Errorf("RootNodeType = %q, want %q", p.RootNodeType, "string_content")
	}

	// The capture @injection.content should be present.
	found := false
	for _, c := range p.Captures {
		if c.Name == "injection.content" {
			found = true
		}
	}
	if !found {
		t.Error("expected @injection.content capture")
	}
}

func TestGenerateDuplicateRootType(t *testing.T) {
	query := `(function_declaration name: (identifier) @name)
(function_declaration body: (block) @body)`

	code, err := Generate(query, "Funcs", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	// Both patterns have root type function_declaration.
	// The second should get a disambiguated name.
	if strings.Count(code, "type FunctionDeclarationMatch struct") > 1 {
		t.Error("duplicate FunctionDeclarationMatch struct names")
	}
	// First pattern should still be FunctionDeclarationMatch.
	if !strings.Contains(code, "FunctionDeclarationMatch") {
		t.Error("expected FunctionDeclarationMatch for first pattern")
	}
}

func TestGenerateQuantifierReset(t *testing.T) {
	// @params is after +, so should be multiple.
	// @body is a sibling AFTER the quantified group, should NOT be multiple.
	query := `(function_declaration
  (parameter_list (parameter)+ @params)
  body: (block) @body)`

	code, err := Generate(query, "Funcs", "queries", "go")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(code, "Params []*gotreesitter.Node") {
		t.Error("expected Params to be []*Node (multiple)")
	}
	if !strings.Contains(code, "Body *gotreesitter.Node") {
		t.Errorf("expected Body to be *Node (singular), got multiple")
	}
}

func TestGeneratePredicateWithParenInString(t *testing.T) {
	// Inline predicate (depth > 0) with ')' inside a string literal.
	// This exercises the '#' handler inside parsePattern, not skipPredicates.
	query := `(call_expression function: (identifier) @fn (#eq? @fn "foo)") arguments: (argument_list) @args)`

	patterns, err := extractPatterns(query)
	if err != nil {
		t.Fatal(err)
	}

	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].RootNodeType != "call_expression" {
		t.Errorf("RootNodeType = %q, want %q", patterns[0].RootNodeType, "call_expression")
	}
	// Should have both captures.
	if len(patterns[0].Captures) < 2 {
		t.Errorf("expected at least 2 captures, got %d", len(patterns[0].Captures))
	}
}

func TestExtractPatternsAnonymousCapture(t *testing.T) {
	query := `(identifier) @_`

	patterns, err := extractPatterns(query)
	if err != nil {
		t.Fatal(err)
	}

	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}

	// @_ should be skipped.
	if len(patterns[0].Captures) != 0 {
		t.Errorf("expected 0 captures (skip @_), got %d", len(patterns[0].Captures))
	}
}
