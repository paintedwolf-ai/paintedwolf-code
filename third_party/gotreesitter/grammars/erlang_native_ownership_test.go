package grammars

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestErlangMacroReplacementElectionIsNative(t *testing.T) {
	for _, test := range erlangNativeElectionCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			language := ErlangLanguage()
			tree, err := gotreesitter.NewParser(language).ParseNoResultCompatibilityBenchmarkOnly(test.source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)
			assertErlangNativeElection(t, tree.RootNode(), language, test)
		})
	}
}

func TestErlangNativeOwnershipRoutes(t *testing.T) {
	for _, test := range erlangNativeElectionCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			language := ErlangLanguage()
			for _, receipt := range retiredDispatchRouteReceipts(t, language, test.source) {
				t.Run(receipt.name, func(t *testing.T) {
					assertErlangNativeElection(t, receipt.tree.RootNode(), language, test)
					if receipt.name == "incremental" {
						profile := receipt.incrementalProfile
						if !profile.OldTreeReuseRoute || profile.ReusedSubtrees == 0 || profile.ReusedBytes == 0 {
							t.Fatalf("incremental route did not reuse the old tree: %+v", profile)
						}
					}
				})
			}
		})
	}
}

type erlangNativeElectionCase struct {
	name   string
	source []byte
	want   string
	reject string
}

func erlangNativeElectionCases() []erlangNativeElectionCase {
	return []erlangNativeElectionCase{
		{
			name:   "function_clauses",
			source: []byte("-define(X, foo() -> ok; foo() -> not_ok)."),
			want:   "(replacement_function_clauses",
			reject: "(replacement_cr_clauses",
		},
		{
			name:   "case_receive_clauses",
			source: []byte("-define(X, [] -> ok; [_ | _] -> not_ok)."),
			want:   "(replacement_cr_clauses",
			reject: "(replacement_function_clauses",
		},
	}
}

func assertErlangNativeElection(
	t *testing.T,
	root *gotreesitter.Node,
	language *gotreesitter.Language,
	test erlangNativeElectionCase,
) {
	t.Helper()
	if root == nil {
		t.Fatal("native parse returned no root")
	}
	if root.HasError() {
		t.Fatalf("native parse failed: %s", root.SExpr(language))
	}
	got := root.SExpr(language)
	if !strings.Contains(got, test.want) || strings.Contains(got, test.reject) {
		t.Fatalf("native election mismatch:\n%s", got)
	}
}

func TestErlangTopLevelFormBoundsAreNative(t *testing.T) {
	source := []byte("% lead\n-module(sample).\nfoo() -> ok.\n")
	language := ErlangLanguage()
	tree, err := gotreesitter.NewParser(language).ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("native parse failed: %s", root.SExpr(language))
	}

	for i := 0; i < int(root.ChildCount()); i++ {
		form := root.Child(i)
		if form == nil || form.IsExtra() || form.ChildCount() == 0 {
			continue
		}
		first := firstNonExtraErlangChild(form)
		last := lastNonExtraErlangChild(form)
		if first == nil || last == nil {
			t.Fatalf("form %s has no source children", form.Type(language))
		}
		if form.StartByte() != first.StartByte() || form.EndByte() != last.EndByte() {
			t.Fatalf(
				"%s bounds = %d..%d, child envelope = %d..%d",
				form.Type(language),
				form.StartByte(),
				form.EndByte(),
				first.StartByte(),
				last.EndByte(),
			)
		}
	}
}

func firstNonExtraErlangChild(node *gotreesitter.Node) *gotreesitter.Node {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && !child.IsExtra() {
			return child
		}
	}
	return nil
}

func lastNonExtraErlangChild(node *gotreesitter.Node) *gotreesitter.Node {
	for i := int(node.ChildCount()) - 1; i >= 0; i-- {
		child := node.Child(i)
		if child != nil && !child.IsExtra() {
			return child
		}
	}
	return nil
}
