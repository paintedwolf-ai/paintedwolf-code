package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestCleanTrailingExtraOwnershipDoesNotNeedResultCompatibility(t *testing.T) {
	for _, test := range []struct {
		name   string
		lang   *gotreesitter.Language
		source string
	}{
		{name: "rst", lang: RstLanguage(), source: "Title\n=====\n\nbody\n"},
		{name: "comment", lang: CommentLanguage(), source: "hello\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			productionParser := gotreesitter.NewParser(test.lang)
			productionParser.SetAdmissionCandidateRoute(false)
			production, err := productionParser.ParseNoResultCompatibilityBenchmarkOnly(source)
			if err != nil {
				t.Fatal(err)
			}
			defer production.Release()
			assertCleanTrailingExtraTree(t, "production", production, test.lang, source)

			compactParser := gotreesitter.NewParser(test.lang)
			compactParser.SetAdmissionCandidateRoute(true)
			compact, err := compactParser.ParseNoResultCompatibilityBenchmarkOnly(source)
			if err != nil {
				t.Fatal(err)
			}
			defer compact.Release()
			assertCleanTrailingExtraTree(t, "compact", compact, test.lang, source)
			if got, want := compact.RootNode().SExpr(test.lang), production.RootNode().SExpr(test.lang); got != want {
				t.Fatalf("compact tree = %s, want production %s", got, want)
			}

			forestParser := gotreesitter.NewParser(test.lang)
			forest, ok := forestParser.ParseForestExperimental(source)
			if !ok || forest == nil {
				offset, symbol, reason, _ := forestParser.ForestDeclineInfo()
				t.Fatalf("forest route declined at %d symbol=%d reason=%s", offset, symbol, reason)
			}
			defer forest.Release()
			assertCleanTrailingExtraTree(t, "forest", forest, test.lang, source)

			oldTree, err := productionParser.Parse(source)
			if err != nil {
				t.Fatal(err)
			}
			defer oldTree.Release()
			nextSource := append([]byte(nil), source...)
			nextSource[0] = 'X'
			oldTree.Edit(gotreesitter.InputEdit{
				StartByte: 0, OldEndByte: 1, NewEndByte: 1,
				StartPoint: gotreesitter.Point{}, OldEndPoint: gotreesitter.Point{Column: 1}, NewEndPoint: gotreesitter.Point{Column: 1},
			})
			incremental, _, err := productionParser.ParseIncrementalProfiled(nextSource, oldTree)
			if err != nil {
				t.Fatal(err)
			}
			defer incremental.Release()
			assertCleanTrailingExtraTree(t, "incremental", incremental, test.lang, nextSource)
			fresh, err := gotreesitter.NewParser(test.lang).Parse(nextSource)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Release()
			if got, want := incremental.RootNode().SExpr(test.lang), fresh.RootNode().SExpr(test.lang); got != want {
				t.Fatalf("incremental tree = %s, want fresh %s", got, want)
			}
		})
	}
}

func assertCleanTrailingExtraTree(t *testing.T, route string, tree *gotreesitter.Tree, lang *gotreesitter.Language, source []byte) {
	t.Helper()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("%s root = %v, want clean tree", route, root)
	}
	if root.EndByte() != uint32(len(source)) {
		t.Fatalf("%s root end = %d, want %d", route, root.EndByte(), len(source))
	}
	count := root.ChildCount()
	if count == 0 {
		t.Fatalf("%s root has no children", route)
	}
	last := root.Child(count - 1)
	if symbol := int(last.Symbol()); last.IsExtra() && symbol < len(lang.SymbolMetadata) && !lang.SymbolMetadata[symbol].Visible {
		t.Fatalf("%s retained hidden trailing extra: %s", route, root.SExpr(lang))
	}
}
