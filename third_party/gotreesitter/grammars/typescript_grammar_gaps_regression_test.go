package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestTypeScriptGrammarGapRegressions(t *testing.T) {
	languages := []struct {
		name string
		load func() *gotreesitter.Language
	}{
		{name: "typescript", load: TypescriptLanguage},
		{name: "tsx", load: TsxLanguage},
	}
	tests := []struct {
		name                string
		source              string
		wantVariance        int
		wantCalls           int
		wantCallExpressions int
	}{
		{
			name: "variance_annotations",
			source: "type Covariant<out T> = T;\n" +
				"type Contravariant<in T> = (value: T) => void;\n" +
				"type Invariant<in out T> = T;\n" +
				"type Combined<const out T extends object = {}, in U, in out V = U> = T;\n",
			wantVariance: 6,
		},
		{
			name:      "two_newline_call_signatures",
			source:    "type Calls = {\n  <T>(value: T): T\n  <U>(value: U): U\n}\n",
			wantCalls: 2,
		},
		{
			name:      "three_newline_call_signatures",
			source:    "type Calls = {\n  <T>(): T\n  <U>(): U\n  <V>(): V\n}\n",
			wantCalls: 3,
		},
		{
			name:      "nested_property_calls",
			source:    "type Holder = { nested: {\n  <T>(): T\n  <U>(): U\n} }\n",
			wantCalls: 2,
		},
		{
			name:      "explicit_semicolons",
			source:    "type Calls = { <T>(): T; <U>(): U; }\n",
			wantCalls: 2,
		},
		{
			name:      "single_signature",
			source:    "type Calls = { <T>(): T }\n",
			wantCalls: 1,
		},
		{
			name:   "constructor_overloads",
			source: "type Factory = {\n  new <T>(): T\n  new <U>(): U\n}\n",
		},
		{
			name:   "index_signatures",
			source: "type Index = {\n  [key: string]: string\n  [key: number]: string\n}\n",
		},
		{
			name:   "named_methods",
			source: "type Methods = {\n  first<T>(): T\n  second<U>(): U\n}\n",
		},
		{
			name:                "ordinary_generic_call_after_newline",
			source:              "foo\n<number>(1)",
			wantCallExpressions: 1,
		},
		{
			name:      "mixed_members",
			source:    "type Mixed = {\n  value: string;\n  <T>(): T\n  create<U>(): U;\n  [key: number]: string\n}",
			wantCalls: 1,
		},
	}

	for _, language := range languages {
		t.Run(language.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					lang := language.load()
					parser := gotreesitter.NewParser(lang)
					tree, err := parser.Parse([]byte(test.source))
					if err != nil {
						t.Fatal(err)
					}
					defer tree.Release()
					root := tree.RootNode()
					if root == nil {
						t.Fatal("parse returned a nil root")
					}
					if root.HasError() || root.EndByte() != uint32(len(test.source)) {
						t.Fatalf("parse is incomplete: error=%t end=%d want=%d tree=%s", root.HasError(), root.EndByte(), len(test.source), root.SExpr(lang))
					}
					if got := countTypeScriptNodes(root, lang, "variance"); got != test.wantVariance {
						t.Fatalf("variance count = %d, want %d: %s", got, test.wantVariance, root.SExpr(lang))
					}
					if got := countTypeScriptNodes(root, lang, "call_signature"); got != test.wantCalls {
						t.Fatalf("call signature count = %d, want %d: %s", got, test.wantCalls, root.SExpr(lang))
					}
					if got := countTypeScriptNodes(root, lang, "call_expression"); got != test.wantCallExpressions {
						t.Fatalf("call expression count = %d, want %d: %s", got, test.wantCallExpressions, root.SExpr(lang))
					}
				})
			}
		})
	}
}

func countTypeScriptNodes(node *gotreesitter.Node, lang *gotreesitter.Language, nodeType string) int {
	if node == nil {
		return 0
	}
	count := 0
	if node.Type(lang) == nodeType {
		count++
	}
	for index := 0; index < node.ChildCount(); index++ {
		count += countTypeScriptNodes(node.Child(index), lang, nodeType)
	}
	return count
}
