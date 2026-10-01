//go:build !grammar_subset

package grammars

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestObjcAtStringLiteralsNeedNoResultCompatibility(t *testing.T) {
	tests := []struct {
		name         string
		source       []byte
		wantType     string
		wantChildren int
	}{
		{
			name:         "single",
			source:       []byte("int main() { NSLog(@\"one\"); }\n"),
			wantType:     "string_literal",
			wantChildren: 4,
		},
		{
			name:         "concatenated",
			source:       []byte("int main() { NSLog(@\"one\" @\"two\"); }\n"),
			wantType:     "concatenated_string",
			wantChildren: 2,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			lang := ObjcLanguage()
			tree, err := gotreesitter.NewParser(lang).
				ParseNoResultCompatibilityBenchmarkOnly(test.source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)

			root := tree.RootNode()
			sexpr := root.SExpr(lang)
			if strings.Contains(sexpr, "at_expression") {
				t.Fatalf("Objective-C string retained at_expression: %s", sexpr)
			}
			node := firstObjcAtStringNodeByType(root, lang, test.wantType)
			if node == nil {
				t.Fatalf("missing %s: %s", test.wantType, sexpr)
			}
			if got := node.ChildCount(); got != test.wantChildren {
				t.Fatalf("%s child count = %d, want %d: %s", test.wantType, got, test.wantChildren, sexpr)
			}
			literal := node
			if test.wantType == "concatenated_string" {
				literal = node.Child(0)
			}
			if got := literal.Child(0).Type(lang); got != "@" {
				t.Fatalf("first literal child = %q, want @: %s", got, sexpr)
			}
			if got, want := literal.StartByte(), uint32(19); got != want {
				t.Fatalf("first literal start = %d, want %d: %s", got, want, sexpr)
			}
		})
	}
}

func firstObjcAtStringNodeByType(
	node *gotreesitter.Node,
	lang *gotreesitter.Language,
	typ string,
) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.Type(lang) == typ {
		return node
	}
	for i := 0; i < node.ChildCount(); i++ {
		if found := firstObjcAtStringNodeByType(node.Child(i), lang, typ); found != nil {
			return found
		}
	}
	return nil
}
