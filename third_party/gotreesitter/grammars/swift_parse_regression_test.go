package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestSwiftLineCommentsStayExtraComments(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("// header\n//\n// body\nlet x = 1\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse swift comments: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("swift comment fixture has parse errors: %s", root.SExpr(lang))
	}
	if got, want := root.NamedChildCount(), 4; got != want {
		t.Fatalf("named child count = %d, want %d; tree: %s", got, want, root.SExpr(lang))
	}
	expectedCommentSpans := [][2]uint32{
		{0, 9},
		{10, 12},
		{13, 20},
	}
	for i, span := range expectedCommentSpans {
		child := root.NamedChild(i)
		if got := child.Type(lang); got != "comment" {
			t.Fatalf("named child %d type = %q, want comment; tree: %s", i, got, root.SExpr(lang))
		}
		if !child.IsExtra() {
			t.Fatalf("named child %d is not extra; tree: %s", i, root.SExpr(lang))
		}
		if got, want := child.StartByte(), span[0]; got != want {
			t.Fatalf("comment %d start = %d, want %d; tree: %s", i, got, want, root.SExpr(lang))
		}
		if got, want := child.EndByte(), span[1]; got != want {
			t.Fatalf("comment %d end = %d, want %d; tree: %s", i, got, want, root.SExpr(lang))
		}
	}
}

func TestSwiftMemberKeywordSelfAfterDotStaysNavigable(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("let element = Element.self\nlet storage = _HashNode.Storage.self\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse swift member self: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("swift member self fixture has parse errors: %s", root.SExpr(lang))
	}
	sexpr := root.SExpr(lang)
	if got, want := strings.Count(sexpr, "(navigation_suffix (simple_identifier))"), 3; got != want {
		t.Fatalf("navigation suffix count = %d, want %d; tree: %s", got, want, sexpr)
	}
}

func TestSwiftNestedGenericCallAdjacentClosers(t *testing.T) {
	lang := SwiftLanguage()
	for _, route := range []struct {
		name    string
		compact bool
	}{
		{name: "production"},
		{name: "compact", compact: true},
	} {
		t.Run(route.name, func(t *testing.T) {
			src := []byte("let v = X<Y<Z>>()\n")
			parser := gotreesitter.NewParser(lang)
			parser.SetAdmissionCandidateRoute(route.compact)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse nested generic call: %v", err)
			}
			defer tree.Release()

			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("nested generic call has parse errors: %s", root.SExpr(lang))
			}
			sexpr := root.SExpr(lang)
			if got, want := strings.Count(sexpr, "(type_arguments"), 2; got != want {
				t.Fatalf("type argument count = %d, want %d; tree: %s", got, want, sexpr)
			}
			if !strings.Contains(sexpr, "(constructor_expression") ||
				!strings.Contains(sexpr, "(constructor_suffix") {
				t.Fatalf("nested generic call has no constructor call: %s", sexpr)
			}
		})
	}
}

// TestSwiftOptionalGenericTypeParses checks issue #556: a generic type
// annotated optional (`Range<Int>?`) must parse error-free, and the
// reconstructed tree must be optional_type wrapping a user_type with a
// type_arguments list, not just an error-free tree of any shape.
func TestSwiftOptionalGenericTypeParses(t *testing.T) {
	lang := SwiftLanguage()
	for _, tc := range []struct {
		src          string
		typeArgCount int
	}{
		{src: "struct S { internal let kRange: Range<Int>? }", typeArgCount: 1},
		{src: "struct S { let value: Dictionary<String, Int>? }", typeArgCount: 2},
	} {
		t.Run(tc.src, func(t *testing.T) {
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse optional generic type: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("optional generic type has parse errors: %s", root.SExpr(lang))
			}
			var optType *gotreesitter.Node
			var find func(n *gotreesitter.Node)
			find = func(n *gotreesitter.Node) {
				if n == nil || optType != nil {
					return
				}
				if n.Type(lang) == "optional_type" {
					optType = n
					return
				}
				for i := 0; i < n.ChildCount(); i++ {
					find(n.Child(i))
				}
			}
			find(root)
			if optType == nil {
				t.Fatalf("no optional_type node: %s", root.SExpr(lang))
			}
			userType := optType.NamedChild(0)
			if userType == nil || userType.Type(lang) != "user_type" {
				t.Fatalf("optional_type does not wrap a user_type: %s", root.SExpr(lang))
			}
			var typeArgs *gotreesitter.Node
			for i := 0; i < userType.NamedChildCount(); i++ {
				if c := userType.NamedChild(i); c.Type(lang) == "type_arguments" {
					typeArgs = c
				}
			}
			if typeArgs == nil {
				t.Fatalf("user_type is missing type_arguments: %s", root.SExpr(lang))
			}
			if got, want := typeArgs.NamedChildCount(), tc.typeArgCount; got != want {
				t.Fatalf("type_arguments count = %d, want %d: %s", got, want, root.SExpr(lang))
			}
		})
	}
}

// TestSwiftOptionalGenericCloseDoesNotStarveCustomOperator guards the fix for
// issue #556 against a regression the fix itself introduced: deferring the
// `>?` close-angle split to the DFA must fire only when a `>` genuinely
// closes an open generic argument list. A standalone trailing `>?` (no open
// generic in scope) must still reach the external scanner as one
// `_custom_operator` token and parse exactly as it does without the #556
// fix in place: as its own custom_operator node, never a full parse
// failure and never a bare anonymous `>` / `?` pair with no error reported.
func TestSwiftOptionalGenericCloseDoesNotStarveCustomOperator(t *testing.T) {
	lang := SwiftLanguage()
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "trailing after property",
			src:  "let a = 1\n>?",
			want: "(source_file (property_declaration (value_binding_pattern) (pattern (simple_identifier)) (integer_literal)) (custom_operator))",
		},
		{
			name: "trailing after import",
			src:  "import Foundation\n>?",
			want: "(source_file (import_declaration (identifier (simple_identifier))) (custom_operator))",
		},
		{
			name: "trailing after function",
			src:  "func f() {}\n>?",
			want: "(source_file (function_declaration (simple_identifier) (function_body)) (custom_operator))",
		},
		{
			name: "trailing after bare identifier",
			src:  "let a = 1\nb >?",
			want: "(source_file (property_declaration (value_binding_pattern) (pattern (simple_identifier)) (integer_literal)) (simple_identifier) (custom_operator))",
		},
		{
			name: "trailing after class",
			src:  "class C {}\n>?",
			want: "(source_file (class_declaration (type_identifier) (class_body)) (custom_operator))",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("trailing >? fixture has parse errors: %s", root.SExpr(lang))
			}
			if got := root.SExpr(lang); got != tc.want {
				t.Fatalf("s-expr = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSwiftOptionalGenericCloseKeepsCustomOperatorDeclarations guards that
// the #556 fix does not disturb genuine custom operators: an `infix operator
// >?` declaration and its use must keep the `>?` spelling intact as a single
// custom_operator node, both in the declaration and at each use site.
func TestSwiftOptionalGenericCloseKeepsCustomOperatorDeclarations(t *testing.T) {
	lang := SwiftLanguage()
	src := "infix operator >?\nlet q = a >? b\n"
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("custom operator declaration has parse errors: %s", root.SExpr(lang))
	}
	want := "(source_file (operator_declaration (custom_operator)) (property_declaration (value_binding_pattern) (pattern (simple_identifier)) (infix_expression (simple_identifier) (custom_operator) (simple_identifier))))"
	if got := root.SExpr(lang); got != want {
		t.Fatalf("s-expr = %s, want %s", got, want)
	}
}

func TestSwiftRightShiftOperatorUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	for _, route := range []struct {
		name    string
		compact bool
	}{
		{name: "production"},
		{name: "compact", compact: true},
	} {
		t.Run(route.name, func(t *testing.T) {
			src := []byte("let v = x >> y\n")
			parser := gotreesitter.NewParser(lang)
			parser.SetAdmissionCandidateRoute(route.compact)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse right shift: %v", err)
			}
			defer tree.Release()

			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("right shift has parse errors: %s", root.SExpr(lang))
			}
			sexpr := root.SExpr(lang)
			if !strings.Contains(sexpr, "(bitwise_operation") {
				t.Fatalf("right shift has no bitwise_operation: %s", sexpr)
			}
			if strings.Contains(sexpr, "(type_arguments") {
				t.Fatalf("right shift became type arguments: %s", sexpr)
			}
		})
	}
}

// TestSwiftStringLiteralEndingInDotDoesNotCorruptFollowingToken guards against
// a regression in shouldDemoteSwiftMemberKeyword/isAfterSwiftMemberDot: a
// string literal whose last content character is '.' immediately before the
// closing quote used to trick the "keyword used as a member name after a
// dot" demotion into firing on the closing-quote token itself (any anonymous
// literal's symbol name trivially equals its own spelling, so the old check
// couldn't tell a real keyword like "self" apart from punctuation). That
// corrupted the closing quote into a bogus identifier token, forcing an
// ERROR/recovery cascade that (among other symptoms) misclassified an
// unrelated later call expression as a constructor_expression.
func TestSwiftStringLiteralEndingInDotDoesNotCorruptFollowingToken(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("let s = \"An element inside an array literal.\"\nlet y = foo(\"x.\")\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse swift trailing-dot string: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("swift trailing-dot string fixture has parse errors: %s", root.SExpr(lang))
	}
	sexpr := root.SExpr(lang)
	if !strings.Contains(sexpr, "line_string_literal") {
		t.Fatalf("expected a line_string_literal node; tree: %s", sexpr)
	}
	if strings.Contains(sexpr, "constructor_expression") {
		t.Fatalf("foo(\"x.\") must parse as call_expression, not constructor_expression; tree: %s", sexpr)
	}
	if !strings.Contains(sexpr, "call_expression") {
		t.Fatalf("expected foo(\"x.\") to parse as call_expression; tree: %s", sexpr)
	}
}

func TestSwiftImportThenClassParsesAsTopLevelDeclarations(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("import Foundation\nclass Foo {}\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse swift import/class: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("swift import/class fixture has parse errors: %s", root.SExpr(lang))
	}
	if got, want := root.Type(lang), "source_file"; got != want {
		t.Fatalf("root type = %q, want %q", got, want)
	}
	if got, want := root.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("root end = %d, want %d; tree: %s", got, want, root.SExpr(lang))
	}
	sexpr := root.SExpr(lang)
	for _, want := range []string{"(import_declaration", "(class_declaration"} {
		if !strings.Contains(sexpr, want) {
			t.Fatalf("missing %s in tree: %s", want, sexpr)
		}
	}
}

func TestSwiftLicenseHeaderImportThenClassParses(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte(`//
//  Foo.swift
//
//  Copyright (c) 2025 Foo Foundation (http://example.org/)
//
//  Permission is hereby granted, free of charge, to any person obtaining a copy
//  of this software and associated documentation files (the "Software"), to deal

import Foundation
class Foo {}
`)
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse swift license header: %v", err)
	}
	defer tree.Release()

	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("swift license header fixture has parse errors: %s", root.SExpr(lang))
	}
	if got, want := root.Type(lang), "source_file"; got != want {
		t.Fatalf("root type = %q, want %q", got, want)
	}
	if got, want := root.EndByte(), uint32(len(src)); got != want {
		t.Fatalf("root end = %d, want %d; tree: %s", got, want, root.SExpr(lang))
	}
	sexpr := root.SExpr(lang)
	if strings.Count(sexpr, "(comment)") < 7 {
		t.Fatalf("license header comments were not preserved as comments: %s", sexpr)
	}
	for _, want := range []string{"(import_declaration", "(class_declaration"} {
		if !strings.Contains(sexpr, want) {
			t.Fatalf("missing %s in tree: %s", want, sexpr)
		}
	}
}

// countSwiftNodeType walks the tree and counts nodes of the given type.
func countSwiftNodeType(lang *gotreesitter.Language, n *gotreesitter.Node, typ string) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.Type(lang) == typ {
		count++
	}
	for i := 0; i < n.ChildCount(); i++ {
		count += countSwiftNodeType(lang, n.Child(i), typ)
	}
	return count
}

// TestSwiftComparisonInConditionRecoversFunction is the regression test for
// issue #118: a comparison operator (< / > / ==) in an if/while condition used
// to make the body brace be consumed as a trailing closure, collapsing the
// whole function into ERROR nodes with no recoverable function_declaration.
func TestSwiftComparisonInConditionRecoversFunction(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"if-greater", "func a() { if x > 0 { foo() } }"},
		{"if-less", "func a() { if x < 0 { foo() } }"},
		{"if-equal", "func a() { if x == 0 { foo() } }"},
		{"while-greater", "func a() { while x > 0 { foo() } }"},
		{"compound", "func a() { if x > 0 && y < 1 { foo() } }"},
		{"nested", "func a() { if x > 0 { if y < 2 { foo() } } }"},
		{"if-else", "func a() { if x > 0 { a() } else { b() } }"},
		{"class-methods", "class C {\n  func a() { if x > 0 { foo() } }\n  func b() { bar() }\n}"},
		{"struct-method", "struct S {\n  func a() { if x > 0 { foo() } }\n}"},
		{"extension-method", "extension S {\n  func a() { if x > 0 { foo() } }\n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("recovered tree still reports error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "function_declaration"); got < 1 {
				t.Fatalf("function_declaration count = %d, want >= 1: %s", got, root.SExpr(lang))
			}
		})
	}
}

// TestSwiftComparisonConditionTreeIsFaithful checks that the recovered tree is
// structurally correct: a proper if_statement whose condition is the bare
// comparison_expression (the synthetic parenthesis used during recovery is
// stripped) with byte-faithful spans.
func TestSwiftComparisonConditionTreeIsFaithful(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("func a() { if x > 0 { foo() } }")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	sexpr := root.SExpr(lang)
	for _, want := range []string{"(function_declaration", "(if_statement", "(comparison_expression"} {
		if !strings.Contains(sexpr, want) {
			t.Fatalf("missing %s in recovered tree: %s", want, sexpr)
		}
	}
	// The synthetic parenthesis must not survive as a tuple_expression condition.
	if countSwiftNodeType(lang, root, "tuple_expression") != 0 {
		t.Fatalf("synthetic parenthesis leaked as tuple_expression: %s", sexpr)
	}
	if countSwiftNodeType(lang, root, "lambda_literal") != 0 {
		t.Fatalf("if body misparsed as trailing-closure lambda_literal: %s", sexpr)
	}
}

// issue #123: a `for…in` loop whose iterable is a range (`0..<n`, `0...n`) or a
// call expression (`stride(from:to:by:)`) used to make the loop body brace be
// consumed as a trailing closure, silently collapsing the enclosing function to
// _modifierless_function_declaration_no_body (with no ERROR node) and spilling
// the body statements out as siblings.
func TestSwiftForRangeIterableRecoversFunction(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"half-open-range", "func f(n: Int) -> Int {\n  var t = 0\n  for i in 0..<n { t += i }\n  return t\n}"},
		{"closed-range", "func f(n: Int) -> Int {\n  var t = 0\n  for i in 0...n { t += i }\n  return t\n}"},
		{"spaced-range", "func f(n: Int) -> Int {\n  var t = 0\n  for i in 0 ..< n { t += i }\n  return t\n}"},
		{"stride-call", "func f(n: Int) -> Int {\n  var t = 0\n  for i in stride(from: 0, to: n, by: 1) { t += i }\n  return t\n}"},
		{"class-method", "class C {\n  func f(n: Int) -> Int {\n    var t = 0\n    for i in 0..<n { t += i }\n    return t\n  }\n}"},
		{"struct-method", "struct S {\n  func f(n: Int) {\n    for i in 0...n { print(i) }\n  }\n}"},
		{"destructuring", "func f() { for (a, b) in zip(xs, ys) { print(a, b) } }"},
		// The loop variable is a backtick-escaped keyword, not the `in` separator.
		{"backtick-var", "func f(n: Int) { for `in` in 0..<n { print(`in`) } }"},
		// A Unicode loop variable must not be split at the `in` substring boundary.
		{"unicode-var", "func f(n: Int) { for π in 0..<n { print(π) } }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("recovered tree still reports error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "function_declaration"); got < 1 {
				t.Fatalf("function_declaration count = %d, want >= 1: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "for_statement"); got < 1 {
				t.Fatalf("for_statement count = %d, want >= 1: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "_modifierless_function_declaration_no_body"); got != 0 {
				t.Fatalf("function collapsed to _modifierless_function_declaration_no_body: %s", root.SExpr(lang))
			}
			// The synthetic parenthesis used during recovery must be unwrapped, not
			// left as a tuple_expression iterable.
			if countSwiftNodeType(lang, root, "tuple_expression") != 0 {
				t.Fatalf("synthetic parenthesis leaked as tuple_expression: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftForRangeThenMethodRecoversFunction covers issue #561: a for…in loop
// over a range, nested in a type body, followed by another method. The #123 fix
// only detects a total collapse (the `for` token left with no for_statement
// parent); here a for_statement node still forms — its own closing brace is
// still absorbed as a trailing closure on the range's upper bound, but the
// method that follows keeps the parser from collapsing cleanly, so it hits an
// ERROR instead. That ERROR still needs the same iterable-bracketing recovery.
func TestSwiftForRangeThenMethodRecoversFunction(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"class-closed-range", "class T {\n  func a() {\n    for n in 4...100 {\n    }\n  }\n  func b() {\n  }\n}\n"},
		{"struct-closed-range", "struct T {\n  func a() {\n    for n in 4...100 {\n    }\n  }\n  func b() {\n  }\n}\n"},
		{"extension-closed-range", "extension T {\n  func a() {\n    for n in 4...100 {\n    }\n  }\n  func b() {\n  }\n}\n"},
		{"half-open-range", "class T {\n  func a() {\n    for n in 0..<10 {\n    }\n  }\n  func b() {\n  }\n}\n"},
		{"identifier-operands", "class T {\n  func a() {\n    for n in a...b {\n    }\n  }\n  func b() {\n  }\n}\n"},
		{"non-empty-bodies", "class T {\n  func a() {\n    for n in 4...100 { g(n) }\n  }\n  func b() { h() }\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("recovered tree still reports error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "function_declaration"); got != 2 {
				t.Fatalf("function_declaration count = %d, want 2: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "for_statement"); got != 1 {
				t.Fatalf("for_statement count = %d, want 1: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "lambda_literal"); got != 0 {
				t.Fatalf("range upper bound absorbed a trailing closure: %s", root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "tuple_expression"); got != 0 {
				t.Fatalf("synthetic parenthesis leaked as tuple_expression: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftForRangeThenMethodCleanVariantsUnaffected guards the shapes from
// issue #561 that already parse cleanly, so the broadened #561 detection (any
// for_statement with an ERROR descendant, not just a totally collapsed `for`
// token) doesn't start rewriting sources that don't need it.
func TestSwiftForRangeThenMethodCleanVariantsUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"no-enclosing-type", "func a() { for n in 4...100 { } }\nfunc b() { }\n"},
		{"only-one-method", "class T { func a() { for n in 4...100 { } } }\n"},
		{"iterable-not-a-range", "class T {\n  func a() { for n in xs { } }\n  func b() { }\n}\nlet xs = [1, 2, 3]\n"},
		{"following-member-is-a-property", "class T {\n  func a() { for n in 4...100 { } }\n  var b = 1\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("clean source reported error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if countSwiftNodeType(lang, root, "for_statement") != 1 {
				t.Fatalf("expected exactly one for_statement: %s", root.SExpr(lang))
			}
			if countSwiftNodeType(lang, root, "tuple_expression") != 0 {
				t.Fatalf("recovery pass wrapped a clean iterable in tuple_expression: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftForBareIdentifierUnaffected guards against the recovery pass disturbing
// a for…in over a bare identifier, which already parses correctly.
func TestSwiftForBareIdentifierUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("func f(xs: [Int]) -> Int {\n  var t = 0\n  for x in xs { t += x }\n  return t\n}")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("clean for…in source reported error: %s", root.SExpr(lang))
	}
	if countSwiftNodeType(lang, root, "for_statement") != 1 {
		t.Fatalf("expected exactly one for_statement: %s", root.SExpr(lang))
	}
	if countSwiftNodeType(lang, root, "tuple_expression") != 0 {
		t.Fatalf("recovery pass wrapped a clean iterable in tuple_expression: %s", root.SExpr(lang))
	}
}

// TestSwiftNormalTrailingClosureUnaffected guards against the recovery pass
// disturbing a legitimate trailing closure (which must stay a lambda_literal).
func TestSwiftNormalTrailingClosureUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("func a() { items.map { x in x } }")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("clean trailing-closure source reported error: %s", root.SExpr(lang))
	}
	if countSwiftNodeType(lang, root, "lambda_literal") != 1 {
		t.Fatalf("expected exactly one lambda_literal: %s", root.SExpr(lang))
	}
}

// issue #131: an `if … else if …` chain used to collapse the enclosing function
// to _modifierless_function_declaration_no_body (with no ERROR node, like #123).
// The trailing-closure ambiguity recovery only found the first `if` token — the
// chained `if` keyword is swallowed into an ERROR node — so it bracketed only the
// first condition, leaving the else-if's body brace absorbed as a trailing closure
// and the function silently truncated. Following the chain through the source and
// requiring a byte-faithful reparse recovers every condition.
func TestSwiftElseIfChainRecoversFunction(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"else-if-trailing-return", "func f(_ x: Int) -> Int {\n    if x > 0 {\n        return 1\n    } else if x < 0 {\n        return 2\n    }\n    return 3\n}\n"},
		{"else-if-else", "func f(_ x: Int) -> Int {\n    if x > 0 {\n        return 1\n    } else if x < 0 {\n        return 2\n    } else {\n        return 3\n    }\n}\n"},
		{"three-way-chain", "func f(_ x: Int) -> Int {\n    if x > 0 {\n        return 1\n    } else if x < 0 {\n        return 2\n    } else if x == 5 {\n        return 5\n    } else {\n        return 3\n    }\n}\n"},
		{"oneline-chain", "func f(_ x: Int) -> Int { if x > 0 { return 1 } else if x < 0 { return 2 } else { return 3 } }"},
		{"else-if-no-return", "func f(_ x: Int) {\n    if x > 0 {\n        a()\n    } else if x < 0 {\n        b()\n    }\n}\n"},
		{"class-method-chain", "class C {\n  func f(_ x: Int) -> Int {\n    if x > 0 { return 1 } else if x < 0 { return 2 }\n    return 3\n  }\n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("recovered tree still reports error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "function_declaration"); got < 1 {
				t.Fatalf("function_declaration count = %d, want >= 1: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "_modifierless_function_declaration_no_body"); got != 0 {
				t.Fatalf("function collapsed to _modifierless_function_declaration_no_body: %s", root.SExpr(lang))
			}
			// Each `else if` continuation must form a nested if_statement, not be
			// absorbed as a trailing-closure lambda_literal.
			if got := countSwiftNodeType(lang, root, "if_statement"); got < 2 {
				t.Fatalf("if_statement count = %d, want >= 2 (chain not nested): %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "lambda_literal"); got != 0 {
				t.Fatalf("else-if body misparsed as trailing-closure lambda_literal: %s", root.SExpr(lang))
			}
			// The synthetic parens injected during recovery must be unwrapped.
			if got := countSwiftNodeType(lang, root, "tuple_expression"); got != 0 {
				t.Fatalf("synthetic parenthesis leaked as tuple_expression: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftTernaryExpressionRecovers is the regression test for issue #135: the
// conditional operator `cond ? a : b` never fired the ternary_expression
// reduction on the runtime blob, dropping `? a : b` into an ERROR node in every
// position and collapsing any enclosing function. The recovery normalizer now
// reconstructs the ternary_expression in place.
func TestSwiftTernaryExpressionRecovers(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"top-level-let", "let y = 3 > 0 ? 1 : 2\n"},
		{"return-no-param", "func f() -> Int { return true ? 1 : 2 }\n"},
		{"return-with-param", "func f(x: Int) -> Int { return x > 0 ? 1 : 2 }\n"},
		{"call-argument", "func f(x: Int) { print(x > 0 ? 1 : 2) }\n"},
		{"string-operands", "let a = cond ? \"x\" : \"y\"\n"},
		{"nested-in-parens", "let z = a + (cond ? 1 : 2) + b\n"},
		{"parenthesised-condition", "let availableRules = (context == nil) ? [.noContextRule] : []\n"},
		{"enum-dot-operands", "let result: MyEnumType = condition ? .someEnumCase : .someOtherEnum\n"},
		{"multi-argument", "let m = foo(a, b > 0 ? 1 : 2)\n"},
		{"as-if-condition", "if a ? b : c == d {\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("recovered tree still reports error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "ternary_expression"); got < 1 {
				t.Fatalf("ternary_expression count = %d, want >= 1: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "ERROR"); got != 0 {
				t.Fatalf("ERROR node present after recovery: %s", root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "_modifierless_function_declaration_no_body"); got != 0 {
				t.Fatalf("function collapsed to _modifierless_function_declaration_no_body: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftTernaryFieldsAndShape checks the reconstructed ternary_expression has
// the exact upstream child layout: condition/if_true/if_false fields around the
// `?` and `:` tokens.
func TestSwiftTernaryFieldsAndShape(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("let y = a ? b : c\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("ternary tree reports error: %s", root.SExpr(lang))
	}
	var tern *gotreesitter.Node
	var find func(n *gotreesitter.Node)
	find = func(n *gotreesitter.Node) {
		if n == nil || tern != nil {
			return
		}
		if n.Type(lang) == "ternary_expression" {
			tern = n
			return
		}
		for i := 0; i < n.ChildCount(); i++ {
			find(n.Child(i))
		}
	}
	find(root)
	if tern == nil {
		t.Fatalf("no ternary_expression: %s", root.SExpr(lang))
	}
	for _, field := range []string{"condition", "if_true", "if_false"} {
		c := tern.ChildByFieldName(field, lang)
		if c == nil {
			t.Fatalf("ternary_expression missing %q field: %s", field, root.SExpr(lang))
		}
	}
	if got, want := tern.StartByte(), uint32(8); got != want {
		t.Fatalf("ternary start = %d, want %d", got, want)
	}
	if got, want := tern.EndByte(), uint32(17); got != want {
		t.Fatalf("ternary end = %d, want %d", got, want)
	}
}

// issue #560: an if/else whose condition is a comparison and whose then-branch
// contains a member access inside a parenthesised context (a call argument or
// a parenthesised negation) makes the condition's last operand absorb the
// then-block as a trailing closure. The real `else` keyword that follows can
// no longer close the if_statement, so it lands in an ERROR node instead —
// with the block after it reinterpreted as the if_statement's own body.
func TestSwiftComparisonThenBranchMemberAccessRecoversElse(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"top-level-minimal", "if a == b { f(c.d) } else {}\n"},
		{"no-call-negated-paren", "func f() {\n  if i == e {\n    d = -(a.b)\n  } else {\n  }\n}\n"},
		{"literal-on-left", "func f() { if 0 == i { d = -(a.b) } else {} }\n"},
		{"less-than", "func f() { if a < b { f(c.d) } else {} }\n"},
		{"statement-form-no-return", "func f() { if i == e { g(base.x) } else { g(0) } }\n"},
		{"deeper-member-path", "func f() { if i == e { g(a.b.c) } else {} }\n"},
		{"else-if-non-comparison", "func f() { if i == e { d = -(a.b) } else if x { } }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("recovered tree still reports error: %s", root.SExpr(lang))
			}
			if got, want := root.EndByte(), uint32(len(src)); got != want {
				t.Fatalf("root end = %d, want %d (span not byte-faithful): %s", got, want, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "if_statement"); got < 1 {
				t.Fatalf("if_statement count = %d, want >= 1: %s", got, root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "lambda_literal"); got != 0 {
				t.Fatalf("then-branch misparsed as trailing-closure lambda_literal: %s", root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "tuple_expression"); got != 0 {
				t.Fatalf("synthetic parenthesis leaked as tuple_expression: %s", root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "else"); got < 1 {
				t.Fatalf("else keyword count = %d, want >= 1 (else clause not reconstructed): %s", got, root.SExpr(lang))
			}
		})
	}
}

// TestSwiftComparisonThenBranchMemberAccessTreeIsFaithful checks the recovered
// tree for the 28-byte repro is structurally correct: a proper if_statement
// whose condition is the bare equality_expression (the synthetic parenthesis
// used during recovery is stripped), with the then-branch call and the else
// block both preserved as ordinary siblings of the if_statement.
func TestSwiftComparisonThenBranchMemberAccessTreeIsFaithful(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("if a == b { f(c.d) } else {}\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	sexpr := root.SExpr(lang)
	for _, want := range []string{"(if_statement", "(equality_expression", "(call_expression", "(navigation_expression"} {
		if !strings.Contains(sexpr, want) {
			t.Fatalf("missing %s in recovered tree: %s", want, sexpr)
		}
	}
	if countSwiftNodeType(lang, root, "constructor_expression") != 0 {
		t.Fatalf("condition operand misparsed as constructor_expression: %s", sexpr)
	}
	if countSwiftNodeType(lang, root, "lambda_literal") != 0 {
		t.Fatalf("then-block misparsed as trailing-closure lambda_literal: %s", sexpr)
	}
	if countSwiftNodeType(lang, root, "ERROR") != 0 {
		t.Fatalf("ERROR node present after recovery: %s", sexpr)
	}
}

// TestSwiftComparisonThenBranchGuardVariantsUnaffected guards the ingredients
// the #560 report identified as individually necessary: each variant below
// drops exactly one ingredient (comparison, member access, parenthesised
// context) and must keep parsing clean, both before and after this fix.
func TestSwiftComparisonThenBranchGuardVariantsUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"not-a-comparison", "func f() -> Int { if x { return g(base.x) } else { return 0 } }\n"},
		{"arg-not-member-access", "func f() -> Int { if i == e { return g(x) } else { return 0 } }\n"},
		{"no-parenthesised-context", "func f() -> Int { if i == e { return base.x } else { return 0 } }\n"},
		{"subscript-not-member-access", "func f() -> Int { if i == e { return g(a[j]) } else { return 0 } }\n"},
		{"parens-without-member-access", "func f() { if i == e { d = -(a - 1) } else {} }\n"},
		{"member-access-without-parens", "func f() { if i == e { d = -a.b } else {} }\n"},
		{"no-else-branch", "func f() -> Int {\n  if i == e {\n    return g(base.x)\n  }\n  return 0\n}\n"},
		{"literal-on-right", "func f() { if i == 0 { d = -(a.b) } else {} }\n"},
		{"else-if-comparison", "func f() { if i == e { d = -(a.b) } else if j == k { } }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("clean source reported error: %s", root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "lambda_literal"); got != 0 {
				t.Fatalf("clean source misparsed a trailing-closure lambda_literal: %s", root.SExpr(lang))
			}
			if got := countSwiftNodeType(lang, root, "tuple_expression"); got != 0 {
				t.Fatalf("recovery pass wrapped a clean condition in tuple_expression: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftNestedIfLetWithCallParses covers issue #558 repro 1: a nested
// if-let whose inner body calls a function with an argument. Single-level
// if-let and one-line multiple bindings must stay clean.
func TestSwiftNestedIfLetWithCallParses(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("func f() {\n  if let a = a {\n    if let b = b {\n      print(a, b)\n    }\n  }\n}\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse nested if-let with call: %v", err)
	}
	defer tree.Release()
	if root := tree.RootNode(); root.HasError() {
		t.Fatalf("nested if-let with call has parse errors: %s", root.SExpr(lang))
	}
}

// TestSwiftIfLetWithAndConditionAndNestedIfParses covers issue #558 repro 2:
// an if-let containing an if whose condition uses && followed by a further
// nested if. Each pairwise combination in isolation must stay clean.
func TestSwiftIfLetWithAndConditionAndNestedIfParses(t *testing.T) {
	lang := SwiftLanguage()
	src := []byte("func f() {\n  if let limit = limit {\n    if baseIdx == nil {\n      if baseDistance > 0 && limit == endIndex {\n        if self.distance(from: i, to: limit) < distance {\n          return\n        }\n      }\n    }\n  }\n}\n")
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse if-let with && and nested if: %v", err)
	}
	defer tree.Release()
	if root := tree.RootNode(); root.HasError() {
		t.Fatalf("if-let with && and nested if has parse errors: %s", root.SExpr(lang))
	}
}

// TestSwiftNestedIfLetCleanVariantsStayClean guards the shapes the issue
// says must remain clean today: single-level if-let, one-line multiple
// bindings, and the pairwise combinations that isolate each ingredient.
func TestSwiftNestedIfLetCleanVariantsStayClean(t *testing.T) {
	lang := SwiftLanguage()
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "single level if-let",
			src:  "func f() {\n  if let a = a {\n    print(a)\n  }\n}\n",
		},
		{
			name: "one-line multiple bindings",
			src:  "func f() {\n  if let a = a, let b = b {\n    print(a, b)\n  }\n}\n",
		},
		{
			name: "nested if-let no call in inner body",
			src:  "func f() { if let a = a { if let b = b { return } } }\n",
		},
		{
			name: "nested if-let call with no args in inner body",
			src:  "func f() { if let a = a { if let b = b { g() } } }\n",
		},
		{
			name: "if-let with && nested if, no comparison",
			src:  "func f() {\n  if let a = a {\n    if b && c {\n      if x {\n        return\n      }\n    }\n  }\n}\n",
		},
		{
			name: "if-let with nested < comparison, no &&",
			src:  "func f() {\n  if let a = a {\n    if g(x) < b {\n      return\n    }\n  }\n}\n",
		},
		{
			name: "&& and < comparison without outer if-let",
			src:  "func f() {\n  if a > 0 && b < c {\n    return\n  }\n}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(test.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			if root := tree.RootNode(); root.HasError() {
				t.Fatalf("has parse errors: %s", root.SExpr(lang))
			}
		})
	}
}

// TestSwiftCustomOperatorCloseAngleRunsUnaffected is the regression test for
// the review finding on PR #567: splitSwiftWideCloseAngleToken used to split
// any external close-angle run made only of '>' characters as soon as any
// active parser state accepted a lone '>' (true in every comparison
// context), destroying Swift custom operators built from '>' runs (`>>>`,
// `>>>>`, ...) even when no generic argument list was open. Each case here
// has no unclosed '<' in scope, so the run must stay one custom_operator
// token: no ERROR node, and exactly the statement count the source implies.
func TestSwiftCustomOperatorCloseAngleRunsUnaffected(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name           string
		src            string
		wantStatements int
	}{
		{name: "triple_infix", src: "let v = x >>> y\n", wantStatements: 1},
		{name: "infix_declaration", src: "infix operator >>> : MultiplicationPrecedence\n", wantStatements: 1},
		{name: "chained_triple_infix", src: "let v = a >>> b >>> c\n", wantStatements: 1},
		{name: "quadruple_infix", src: "let v = x >>>> y\n", wantStatements: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range []struct {
				name    string
				compact bool
			}{
				{name: "production"},
				{name: "compact", compact: true},
			} {
				t.Run(route.name, func(t *testing.T) {
					parser := gotreesitter.NewParser(lang)
					parser.SetAdmissionCandidateRoute(route.compact)
					src := []byte(tc.src)
					tree, err := parser.Parse(src)
					if err != nil {
						t.Fatalf("parse %q: %v", tc.src, err)
					}
					defer tree.Release()

					root := tree.RootNode()
					if root.HasError() {
						t.Fatalf("%q has parse errors: %s", tc.src, root.SExpr(lang))
					}
					sexpr := root.SExpr(lang)
					if !strings.Contains(sexpr, "(custom_operator)") {
						t.Fatalf("%q did not keep a custom_operator token: %s", tc.src, sexpr)
					}
					if got, want := root.NamedChildCount(), tc.wantStatements; got != want {
						t.Fatalf("%q statement count = %d, want %d; tree: %s", tc.src, got, want, sexpr)
					}
				})
			}
		})
	}
}

func TestSwiftTripleNestedGenericTypeAnnotationParses(t *testing.T) {
	lang := SwiftLanguage()
	for _, src := range []string{
		"struct S { let value: A<B<C<Int>>> }",
		"func f(value: A<B<C<Int>>>) {}",
		"let value: A<B<C<Int>>> = make()",
		// Depth-4: locks in that the fix generalizes over the run length
		// instead of only handling the depth-3 shape from issue #559.
		"let value: A<B<C<D<Int>>>> = make()",
	} {
		t.Run(src, func(t *testing.T) {
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse triple nested generic type: %v", err)
			}
			defer tree.Release()
			if root := tree.RootNode(); root.HasError() {
				t.Fatalf("triple nested generic type has parse errors: %s", root.SExpr(lang))
			}
		})
	}
}

// swiftTreeHasMissingNode walks the parse tree looking for a MISSING node,
// the exact presentation issue #559 named: a wide close-angle run that a
// parser cannot split leaves a generic argument list open, and recovery
// inserts a MISSING closer instead of surfacing a plain ERROR.
func swiftTreeHasMissingNode(n *gotreesitter.Node) bool {
	if n == nil {
		return false
	}
	if n.IsMissing() {
		return true
	}
	for i := 0; i < n.ChildCount(); i++ {
		if swiftTreeHasMissingNode(n.Child(i)) {
			return true
		}
	}
	return false
}

// TestSwiftTripleNestedGenericCallParses is the direct regression test for
// issue #559: `A<B<C<Int>>>()` construction calls, not just type
// annotations, used to fail with a MISSING close-angle node inserted by
// error recovery even though HasError() could still report false.
func TestSwiftTripleNestedGenericCallParses(t *testing.T) {
	lang := SwiftLanguage()
	for _, src := range []string{
		"func f() { let v = A<B<C<Int>>>() }",
		"func f() { let v = IndexValidator<ChunksOfCountCollection<ClosedRange<Int>>>() }",
		// Depth-5: one level deeper than the issue's own repro, guarding
		// against a fix that only special-cases depth 3 or 4.
		"func f() { let v = A<B<C<D<E<Int>>>>>() }",
	} {
		t.Run(src, func(t *testing.T) {
			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse triple nested generic call: %v", err)
			}
			defer tree.Release()

			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("triple nested generic call has parse errors: %s", root.SExpr(lang))
			}
			if swiftTreeHasMissingNode(root) {
				t.Fatalf("triple nested generic call has a MISSING node: %s", root.SExpr(lang))
			}
			sexpr := root.SExpr(lang)
			if !strings.Contains(sexpr, "(constructor_expression") {
				t.Fatalf("triple nested generic call is not a constructor_expression: %s", sexpr)
			}
		})
	}
}

// swiftParseNoError parses src and fails the test if the parse errors or the
// resulting tree reports a syntax error.
func swiftParseNoError(t *testing.T, lang *gotreesitter.Language, src string) *gotreesitter.Tree {
	t.Helper()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tree.RootNode().HasError() {
		t.Fatalf("has parse errors: %s", tree.RootNode().SExpr(lang))
	}
	return tree
}

// swiftFindFirstNodeByType returns the first node of the given type in a
// pre-order walk of the tree rooted at n, or nil if none exists.
func swiftFindFirstNodeByType(lang *gotreesitter.Language, n *gotreesitter.Node, nodeType string) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	if n.Type(lang) == nodeType {
		return n
	}
	for i := 0; i < n.ChildCount(); i++ {
		if found := swiftFindFirstNodeByType(lang, n.Child(i), nodeType); found != nil {
			return found
		}
	}
	return nil
}

// TestSwiftMultilineExtensionWhereClauseParses guards issue #557: a
// constrained extension whose generic constraint list breaks across lines
// must parse identically to the same constraints written on one line.
func TestSwiftMultilineExtensionWhereClauseParses(t *testing.T) {
	lang := SwiftLanguage()

	singleLineTree := swiftParseNoError(t, lang,
		"extension X: P where Base: Q, Base.Element: Q {\n  func f() {}\n}\n")
	defer singleLineTree.Release()
	singleLineConstraints := swiftFindFirstNodeByType(lang, singleLineTree.RootNode(), "type_constraints")
	if singleLineConstraints == nil {
		t.Fatalf("single-line form: no type_constraints node: %s", singleLineTree.RootNode().SExpr(lang))
	}
	wantSExpr := singleLineConstraints.SExpr(lang)

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "where alone on its own line, constraint list split across lines",
			src:  "extension X: P\nwhere\n  Base: Q,\n  Base.Element: Q\n{\n  func f() {}\n}\n",
		},
		{
			// The split-constraint variant from the issue #557 follow-up
			// comment: `where` shares a line with the first constraint, and
			// the list still breaks across lines after the comma.
			name: "where shares a line with the first constraint, list split across lines",
			src:  "extension X: P\nwhere Base: Q,\n  Base.Element: Q\n{\n  func f() {}\n}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := swiftParseNoError(t, lang, tc.src)
			defer tree.Release()
			constraints := swiftFindFirstNodeByType(lang, tree.RootNode(), "type_constraints")
			if constraints == nil {
				t.Fatalf("no type_constraints node: %s", tree.RootNode().SExpr(lang))
			}
			if got := constraints.SExpr(lang); got != wantSExpr {
				t.Fatalf("type_constraints S-expr = %q, want %q (single-line form)", got, wantSExpr)
			}
		})
	}
}

// TestSwiftGenericFunctionMultilineWhereClauseParses guards issue #557 for a
// generic function's where clause, mirroring the extension case above.
func TestSwiftGenericFunctionMultilineWhereClauseParses(t *testing.T) {
	lang := SwiftLanguage()
	tree := swiftParseNoError(t, lang,
		"func f<T>(_ x: T) -> T where T: Q,\n  T: R {\n  return x\n}\n")
	defer tree.Release()
}

// TestSwiftCommentWithCommaDoesNotSuppressImplicitSemi guards against a
// regression where a trailing line comment containing a comma is mistaken
// for a real comma token, wrongly suppressing implicit semicolon insertion
// between two unrelated statements on consecutive lines.
func TestSwiftCommentWithCommaDoesNotSuppressImplicitSemi(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "function body, trailing line comment containing a comma",
			src:  "func f() {\n  let a = 1 // x,\n  let b = 2\n}\n",
		},
		{
			name: "class body, trailing line comment containing a comma",
			src:  "class C {\n  var a = 1 // x,\n  var b = 2\n}\n",
		},
		{
			name: "function body, trailing line comment without a comma (control)",
			src:  "func f() {\n  let a = 1 // x\n  let b = 2\n}\n",
		},
		{
			name: "class body, trailing line comment without a comma (control)",
			src:  "class C {\n  var a = 1 // x\n  var b = 2\n}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := swiftParseNoError(t, lang, tc.src)
			defer tree.Release()
		})
	}
}
