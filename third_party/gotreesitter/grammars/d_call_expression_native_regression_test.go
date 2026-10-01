//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestDCallExpressionNeedsNoResultCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		source         string
		targetType     string
		targetTail     string
		targetChildren int
	}{
		{
			name:           "qualified",
			source:         "void f() { a.b.c(); }\n",
			targetType:     "type",
			targetTail:     "identifier",
			targetChildren: 5,
		},
		{
			name:           "qualified template",
			source:         "void f() { a.b.foo!int(); }\n",
			targetType:     "type",
			targetTail:     "template_instance",
			targetChildren: 5,
		},
		{
			name:           "template",
			source:         "void f() { foo!int(); }\n",
			targetType:     "type",
			targetTail:     "template_instance",
			targetChildren: 1,
		},
		{
			name:       "simple type name",
			source:     "void f() { Type(); }\n",
			targetType: "identifier",
		},
	}

	language := DLanguage()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tree, err := gotreesitter.NewParser(language).
				ParseNoResultCompatibilityBenchmarkOnly([]byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)

			assertDCallExpressionTarget(
				t,
				"compatibility-free",
				tree,
				language,
				test.targetType,
				test.targetTail,
				test.targetChildren,
			)
		})
	}
}

func TestDCallExpressionRoutes(t *testing.T) {
	language := DLanguage()
	tests := []struct {
		name           string
		source         string
		targetType     string
		targetTail     string
		targetChildren int
	}{
		{
			name:           "qualified",
			source:         "void f() { a.b.c(); }",
			targetType:     "type",
			targetTail:     "identifier",
			targetChildren: 5,
		},
		{
			name:           "qualified template",
			source:         "void f() { a.b.foo!int(); }",
			targetType:     "type",
			targetTail:     "template_instance",
			targetChildren: 5,
		},
		{
			name:           "template",
			source:         "void f() { foo!int(); }",
			targetType:     "type",
			targetTail:     "template_instance",
			targetChildren: 1,
		},
		{
			name:       "simple type name",
			source:     "void f() { Type(); }",
			targetType: "identifier",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, receipt := range retiredDispatchRouteReceiptsAllowCompactFallback(
				t,
				language,
				[]byte(test.source),
			) {
				t.Run(receipt.name, func(t *testing.T) {
					assertDCallExpressionTarget(
						t,
						receipt.name,
						receipt.tree,
						language,
						test.targetType,
						test.targetTail,
						test.targetChildren,
					)
				})
			}
		})
	}
}

func assertDCallExpressionTarget(
	t *testing.T,
	route string,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
	targetType string,
	targetTail string,
	targetChildren int,
) {
	t.Helper()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("%s root = %v, want a clean tree", route, root)
	}
	call := findFirstNamedDescendantWhere(
		root,
		language,
		"call_expression",
		func(*gotreesitter.Node) bool { return true },
	)
	if call == nil {
		t.Fatalf("%s has no call_expression: %s", route, root.SExpr(language))
	}
	assertDCallTarget(
		t,
		route,
		call,
		language,
		targetType,
		targetTail,
		targetChildren,
	)
}

func assertDCallTarget(
	t *testing.T,
	route string,
	call *gotreesitter.Node,
	language *gotreesitter.Language,
	targetType string,
	targetTail string,
	targetChildren int,
) {
	t.Helper()
	if got, want := call.ChildCount(), 2; got != want {
		t.Fatalf("%s call child count = %d, want %d: %s", route, got, want, call.SExpr(language))
	}
	target := call.Child(0)
	if target == nil || target.Type(language) != targetType {
		t.Fatalf("%s call target = %v, want %s: %s", route, target, targetType, call.SExpr(language))
	}
	if targetTail == "" {
		if got := target.ChildCount(); got != 0 {
			t.Fatalf("%s call target child count = %d, want 0: %s", route, got, target.SExpr(language))
		}
		return
	}
	if got, want := target.ChildCount(), targetChildren; got != want {
		t.Fatalf("%s call target child count = %d, want %d: %s", route, got, want, target.SExpr(language))
	}
	tail := target.Child(targetChildren - 1)
	if tail == nil || tail.Type(language) != targetTail {
		t.Fatalf("%s call target tail = %v, want %s: %s", route, tail, targetTail, target.SExpr(language))
	}
}
