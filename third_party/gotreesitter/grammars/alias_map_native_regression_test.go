package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

type aliasMapRetirementCase struct {
	name                   string
	source                 []byte
	language               *gotreesitter.Language
	parent                 string
	child                  string
	parentStart            uint32
	parentEnd              uint32
	childStart             uint32
	childEnd               uint32
	reuseUnsupportedReason string
}

func TestAliasMapCollapsedChildrenNeedNoResultCompatibility(t *testing.T) {
	for _, test := range aliasMapRetirementCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			source := append(append([]byte(nil), test.source...), '\n')
			tree, err := gotreesitter.NewParser(test.language).
				ParseNoResultCompatibilityBenchmarkOnly(source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)
			assertAliasMapCollapsedChild(t, tree, test, source)
		})
	}
}

func TestAliasMapCollapsedChildRoutes(t *testing.T) {
	for _, test := range aliasMapRetirementCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			source := append(append([]byte(nil), test.source...), '\n')
			for _, receipt := range retiredDispatchRouteReceipts(t, test.language, test.source) {
				t.Run(receipt.name, func(t *testing.T) {
					assertAliasMapCollapsedChild(t, receipt.tree, test, source)
					if receipt.name != "incremental" {
						return
					}
					profile := receipt.incrementalProfile
					if test.reuseUnsupportedReason != "" {
						if !profile.ReuseUnsupported ||
							profile.ReuseUnsupportedReason != test.reuseUnsupportedReason {
							t.Fatalf("incremental reuse status = %+v", profile)
						}
						return
					}
					if !profile.OldTreeReuseRoute || profile.ReusedSubtrees == 0 ||
						profile.ReusedBytes == 0 {
						t.Fatalf("incremental route did not reuse the old tree: %+v", profile)
					}
				})
			}
		})
	}
}

func aliasMapRetirementCases() []aliasMapRetirementCase {
	return []aliasMapRetirementCase{
		{
			name:        "cue_value",
			source:      []byte("x: sub"),
			language:    CueLanguage(),
			parent:      "value",
			child:       "identifier",
			parentStart: 3,
			parentEnd:   6,
			childStart:  3,
			childEnd:    6,
		},
		{
			name:                   "gitcommit_message",
			source:                 []byte("subject\n\nbody line"),
			language:               GitcommitLanguage(),
			parent:                 "message",
			child:                  "message_line",
			parentStart:            9,
			parentEnd:              19,
			childStart:             9,
			childEnd:               18,
			reuseUnsupportedReason: "external_scanner_unsupported",
		},
		{
			name:                   "r_string_content",
			source:                 []byte(`"\\"`),
			language:               RLanguage(),
			parent:                 "string_content",
			child:                  "escape_sequence",
			parentStart:            1,
			parentEnd:              3,
			childStart:             1,
			childEnd:               3,
			reuseUnsupportedReason: "external_scanner_unsupported",
		},
	}
}

func assertAliasMapCollapsedChild(
	t *testing.T,
	tree *gotreesitter.Tree,
	test aliasMapRetirementCase,
	source []byte,
) {
	t.Helper()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected root: %v", root)
	}
	if test.language.NativeResultCompatibility&
		gotreesitter.ResultCompatibilityNativeCollapsedChildren == 0 {
		t.Fatal("exact grammar artifact omitted the collapsed-child receipt")
	}
	runtime := tree.ParseRuntime()
	if runtime.NormalizationPassesChecked != 0 || runtime.NormalizationPassesRun != 0 ||
		runtime.NormalizationNodesVisited != 0 || runtime.NormalizationNodesRewritten != 0 {
		t.Fatalf(
			"normalization checked/run/visited/rewritten=%d/%d/%d/%d",
			runtime.NormalizationPassesChecked,
			runtime.NormalizationPassesRun,
			runtime.NormalizationNodesVisited,
			runtime.NormalizationNodesRewritten,
		)
	}
	parents := collapsedChildRegressionNodes(root, test.language, test.parent)
	if len(parents) != 1 {
		t.Fatalf("%s count = %d, want 1: %s", test.parent, len(parents), root.SExpr(test.language))
	}
	parent := parents[0]
	if parent.ChildCount() != 1 {
		t.Fatalf("%s child count = %d, want 1", test.parent, parent.ChildCount())
	}
	child := parent.Child(0)
	if child == nil || child.Type(test.language) != test.child || !child.IsNamed() {
		t.Fatalf("%s child = %v, want named %s", test.parent, child, test.child)
	}
	if parent.StartByte() != test.parentStart || parent.EndByte() != test.parentEnd {
		t.Fatalf(
			"%s span = [%d:%d], want [%d:%d]",
			test.parent,
			parent.StartByte(),
			parent.EndByte(),
			test.parentStart,
			test.parentEnd,
		)
	}
	if child.StartByte() != test.childStart || child.EndByte() != test.childEnd {
		t.Fatalf(
			"%s span = [%d:%d], want [%d:%d]",
			test.child,
			child.StartByte(),
			child.EndByte(),
			test.childStart,
			test.childEnd,
		)
	}
	if root.EndByte() != uint32(len(source)) {
		t.Fatalf("root end = %d, want %d", root.EndByte(), len(source))
	}
}
