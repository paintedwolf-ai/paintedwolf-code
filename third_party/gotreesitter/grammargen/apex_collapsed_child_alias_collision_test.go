package grammargen

import (
	"errors"
	"os"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
)

// TestGeneratedApexCollapsedChildAliasCollisionDoesNotRecurse covers the real
// Apex grammar's generated alias surface: every ledger display name is shared
// by one named wrapper and one anonymous raw token. Exact namedness-aware
// ownership must select the raw-token pair without recursively wrapping the
// same-named alias in the generated result.
func TestGeneratedApexCollapsedChildAliasCollisionDoesNotRecurse(t *testing.T) {
	const grammarJSON = "/tmp/grammar_parity/apex/apex/src/grammar.json"
	source, err := os.ReadFile(grammarJSON)
	if errors.Is(err, os.ErrNotExist) {
		t.Skipf("Apex grammar checkout unavailable: %s", grammarJSON)
	}
	if err != nil {
		t.Fatal(err)
	}
	grammar, err := ImportGrammarJSON(source)
	if err != nil {
		t.Fatal(err)
	}
	lang, err := GenerateLanguage(grammar)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"after_delete", "after_insert", "after_undelete", "after_update", "before_delete", "before_insert", "before_update", "delete", "insert", "super", "system", "undelete", "upsert", "user"} {
		named, anonymous := generatedApexSymbolsNamed(lang, name)
		if named != 1 || anonymous != 1 {
			t.Fatalf("generated Apex alias-collision inventory %s named=%d anonymous=%d, want 1/1", name, named, anonymous)
		}
	}

	tree, err := gts.NewParser(lang).Parse([]byte("public class C { public C(){ super(); } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("generated Apex parse returned nil root")
	}
	if nodes, depth, ok := boundedGeneratedApexTree(root, 0); !ok {
		t.Fatalf("generated Apex result exceeded finite alias bound: nodes=%d depth=%d", nodes, depth)
	}
}

func generatedApexSymbolsNamed(lang *gts.Language, name string) (named, anonymous int) {
	for i, symbolName := range lang.SymbolNames {
		if symbolName != name || i >= len(lang.SymbolMetadata) {
			continue
		}
		if lang.SymbolMetadata[i].Named {
			named++
		} else {
			anonymous++
		}
	}
	return named, anonymous
}

func boundedGeneratedApexTree(node *gts.Node, depth int) (nodes, maxDepth int, ok bool) {
	if node == nil {
		return 0, depth, true
	}
	if depth > 512 {
		return 1, depth, false
	}
	nodes, maxDepth = 1, depth
	for i := 0; i < node.ChildCount(); i++ {
		childNodes, childDepth, childOK := boundedGeneratedApexTree(node.Child(i), depth+1)
		nodes += childNodes
		if childDepth > maxDepth {
			maxDepth = childDepth
		}
		if !childOK || nodes > 1_000_000 {
			return nodes, maxDepth, false
		}
	}
	return nodes, maxDepth, true
}
