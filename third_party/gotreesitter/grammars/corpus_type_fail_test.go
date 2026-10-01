package grammars

import (
	"os"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// TestCorpusTypeFailDiag investigates why Go parser produces error trees
// (no_stacks_alive) on small real-world files that C parses successfully.
func TestCorpusTypeFailDiag(t *testing.T) {
	cases := []struct {
		lang string
		file string
	}{
		// Pure DFA (no external scanner) — isolates issue to lexer/parser core
		{"jq", "../harness_out/corpus_real_205_noscala_bounded/jq/small__func-expr.jq"},
		{"sparql", "../harness_out/corpus_real_205_noscala_bounded/sparql/small__indents.sparql"},
		{"pascal", "../harness_out/corpus_real_205_noscala_bounded/pascal/medium__foo.pas"},
		{"meson", "../harness_out/corpus_real_205_noscala_bounded/meson/medium__meson.build.sway"},
		// With external scanner — for comparison
		{"earthfile", "../harness_out/corpus_real_205_noscala_bounded/earthfile/small__Earthfile"},
		{"bash", "../harness_out/corpus_real_205_noscala_bounded/bash/small__release.sh"},
	}

	byName := map[string]LangEntry{}
	for _, e := range AllLanguages() {
		byName[e.Name] = e
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			entry, ok := byName[tc.lang]
			if !ok {
				t.Skipf("language %q not found", tc.lang)
				return
			}

			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Skipf("file not found: %v", err)
				return
			}
			t.Logf("source: %d bytes", len(src))

			lang := entry.Language()
			parser := gotreesitter.NewParser(lang)

			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if tree == nil {
				t.Fatal("Parse returned nil")
			}

			root := tree.RootNode()
			if root == nil {
				t.Fatal("RootNode is nil")
			}

			rootType := root.Type(lang)
			t.Logf("root: symbol=%d type=%q hasError=%v children=%d startByte=%d endByte=%d",
				root.Symbol(), rootType, root.HasError(), root.ChildCount(),
				root.StartByte(), root.EndByte())
			t.Logf("stopReason: %v", tree.ParseStopReason())

			if rootType == "" {
				t.Logf("ERROR TREE: root symbol %d is out of SymbolNames range (len=%d)", root.Symbol(), len(lang.SymbolNames))
			}

			// Print first 10 children
			for i := 0; i < root.ChildCount() && i < 10; i++ {
				child := root.Child(i)
				if child != nil {
					t.Logf("  child[%d]: symbol=%d type=%q bytes=%d..%d hasError=%v",
						i, child.Symbol(), child.Type(lang), child.StartByte(), child.EndByte(), child.HasError())
				}
			}

			// Now try parsing just the first line to see if that works
			firstNewline := -1
			for i, b := range src {
				if b == '\n' {
					firstNewline = i
					break
				}
			}
			if firstNewline > 0 {
				shortSrc := src[:firstNewline+1]
				tree2, _ := parser.Parse(shortSrc)
				if tree2 != nil {
					root2 := tree2.RootNode()
					if root2 != nil {
						t.Logf("first-line-only: type=%q hasError=%v children=%d stop=%v",
							root2.Type(lang), root2.HasError(), root2.ChildCount(), tree2.ParseStopReason())
					}
				}
			}

			// Try progressively larger prefixes to find where it breaks
			breakPoint := len(src)
			for lineEnd := 0; lineEnd < len(src); {
				nextEnd := lineEnd
				for nextEnd < len(src) && src[nextEnd] != '\n' {
					nextEnd++
				}
				if nextEnd < len(src) {
					nextEnd++ // include the \n
				}

				prefix := src[:nextEnd]
				treeP, _ := parser.Parse(prefix)
				if treeP != nil {
					rootP := treeP.RootNode()
					if rootP != nil && rootP.Type(lang) == "" {
						breakPoint = nextEnd
						t.Logf("BREAKS at byte %d (line ending): %q...", nextEnd,
							truncateStr(string(prefix[lineEnd:]), 60))
						break
					}
				}
				lineEnd = nextEnd
			}

			if breakPoint == len(src) {
				t.Logf("progressive prefix: no breakpoint found before EOF (full file fails)")
			}

			// Also try the smoke sample to confirm it works
			smoke := ParseSmokeSample(tc.lang)
			if smoke != "" {
				treeSm, _ := parser.Parse([]byte(smoke))
				if treeSm != nil {
					rootSm := treeSm.RootNode()
					if rootSm != nil {
						t.Logf("smoke sample: type=%q hasError=%v children=%d",
							rootSm.Type(lang), rootSm.HasError(), rootSm.ChildCount())
					}
				}
			}
		})
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

