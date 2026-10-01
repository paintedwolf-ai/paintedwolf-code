package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestYAMLSingleQuotedMultilineScalarWithoutIndent(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		scalarType    string
		wantDocuments int
	}{
		{
			name: "root scalar",
			source: `root: 'a
b
c'` + "\n",
			scalarType:    "single_quote_scalar",
			wantDocuments: 1,
		},
		{
			name: "nested scalar",
			source: `outer:
  key: 'a
sibling: b'
after: c` + "\n",
			scalarType:    "single_quote_scalar",
			wantDocuments: 1,
		},
		{
			name: "single-quoted closing delimiter on dedented line",
			source: `root:
  key: 'line1
'` + "\n",
			scalarType:    "single_quote_scalar",
			wantDocuments: 1,
		},
		{
			name: "double-quoted closing delimiter on dedented line",
			source: `root:
  key: "line1
"` + "\n",
			scalarType:    "double_quote_scalar",
			wantDocuments: 1,
		},
		{
			name: "Kubernetes document stream",
			source: `apiVersion: v1
kind: Service
metadata:
  annotations:
    pod.alpha.kubernetes.io/init-containers: '[
        {
          "name": "bootstrap"
        }
    ]'
  selector:
    app: test
---
apiVersion: v1
kind: Service
metadata:
  name: second-service` + "\n",
			scalarType:    "single_quote_scalar",
			wantDocuments: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			language := YamlLanguage()
			parser := gotreesitter.NewParser(language)
			tree, err := parser.Parse(source)
			if err != nil {
				t.Fatalf("parse YAML: %v", err)
			}
			if tree == nil || tree.RootNode() == nil {
				t.Fatal("parse YAML returned a nil root")
			}
			t.Cleanup(tree.Release)

			root := tree.RootNode()
			runtime := tree.ParseRuntime()
			if root.HasError() || root.EndByte() != uint32(len(source)) ||
				runtime.StopReason != gotreesitter.ParseStopAccepted || runtime.Truncated {
				t.Fatalf(
					"YAML parse failed: hasError=%t end=%d wantEnd=%d runtime=%s tree=%s",
					root.HasError(),
					root.EndByte(),
					len(source),
					runtime.Summary(),
					root.SExpr(language),
				)
			}
			if got := root.NamedChildCount(); got != test.wantDocuments {
				t.Fatalf(
					"document count = %d, want %d: %s",
					got,
					test.wantDocuments,
					root.SExpr(language),
				)
			}
			for i := 0; i < root.NamedChildCount(); i++ {
				if got := root.NamedChild(i).Type(language); got != "document" {
					t.Fatalf("root child %d type = %q, want document", i, got)
				}
			}
			if count := countYAMLNodesByType(root, language, test.scalarType); count != 1 {
				t.Fatalf("%s count = %d, want 1: %s", test.scalarType, count, root.SExpr(language))
			}
		})
	}
}

func countYAMLNodesByType(node *gotreesitter.Node, language *gotreesitter.Language, nodeType string) int {
	if node == nil {
		return 0
	}
	count := 0
	if node.Type(language) == nodeType {
		count++
	}
	for i := 0; i < node.ChildCount(); i++ {
		count += countYAMLNodesByType(node.Child(i), language, nodeType)
	}
	return count
}
