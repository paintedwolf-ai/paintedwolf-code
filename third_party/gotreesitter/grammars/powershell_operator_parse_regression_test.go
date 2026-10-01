package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestPowerShellOperatorWrappersKeepTokenChildrenViaEngine proves that the
// reduce engine preserves the anonymous token children of PowerShell's
// command_argument_sep/comparison_operator/format_operator/
// file_redirection_operator/merging_redirection_operator/
// command_invokation_operator wrapper nodes on real parses.
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so these named
// wrappers around anonymous operator/separator tokens are never collapsed to
// childless leaves in the first place.
func TestPowerShellOperatorWrappersKeepTokenChildrenViaEngine(t *testing.T) {
	lang := PowershellLanguage()

	cases := []struct {
		name        string
		src         string
		wrapperType string
		childType   string
	}{
		{"comparison_operator", "if ($a -eq $b) { 1 }\n", "comparison_operator", "-eq"},
		{"file_redirection_operator", "echo 1 > out.txt\n", "file_redirection_operator", ">"},
		{"merging_redirection_operator", "echo 1 2>&1\n", "merging_redirection_operator", "2>&1"},
		{"command_invokation_operator", "& gci\n", "command_invokation_operator", "&"},
		{"format_operator", "'a' -f 'b'\n", "format_operator", "-f"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser := ts.NewParser(lang)
			tree, err := parser.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			root := tree.RootNode()
			if root == nil {
				t.Fatal("missing root node")
			}
			if tree.ParseStopReason() != ts.ParseStopAccepted {
				t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
			}
			if root.HasError() {
				t.Fatalf("expected %q to parse cleanly, got %s", tc.src, root.SExpr(lang))
			}
			wrapper := findFirstNamedDescendantWhere(root, lang, tc.wrapperType, func(*ts.Node) bool { return true })
			if wrapper == nil {
				t.Fatalf("missing %s node; tree=%s", tc.wrapperType, root.SExpr(lang))
			}
			if got := wrapper.ChildCount(); got != 1 {
				t.Fatalf("%s child count = %d, want 1; tree=%s", tc.wrapperType, got, root.SExpr(lang))
			}
			child := wrapper.Child(0)
			if child == nil {
				t.Fatalf("%s missing token child; node=%s", tc.wrapperType, wrapper.SExpr(lang))
			}
			if child.Type(lang) != tc.childType || child.IsNamed() {
				t.Fatalf("%s child type/named = %q/%v, want %s/false; node=%s", tc.wrapperType, child.Type(lang), child.IsNamed(), tc.childType, wrapper.SExpr(lang))
			}
		})
	}
}

// TestPowerShellCommandArgumentSepKeepsTokenChildrenViaEngine proves the
// same engine-only invariant for command_argument_sep across both of its
// anonymous token alternatives (" " and ":").
func TestPowerShellCommandArgumentSepKeepsTokenChildrenViaEngine(t *testing.T) {
	lang := PowershellLanguage()
	parser := ts.NewParser(lang)
	src := []byte("gci -Path: foo\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if root.HasError() {
		t.Fatalf("expected command_argument_sep source to parse cleanly, got %s", root.SExpr(lang))
	}
	var seps []*ts.Node
	var walk func(*ts.Node)
	walk = func(n *ts.Node) {
		if n == nil {
			return
		}
		if n.Type(lang) == "command_argument_sep" {
			seps = append(seps, n)
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	if len(seps) == 0 {
		t.Fatalf("missing command_argument_sep nodes; tree=%s", root.SExpr(lang))
	}
	sawSpace := false
	sawColon := false
	for _, sep := range seps {
		if got := sep.ChildCount(); got != 1 {
			t.Fatalf("command_argument_sep child count = %d, want 1; node=%s", got, sep.SExpr(lang))
		}
		child := sep.Child(0)
		if child == nil {
			t.Fatalf("command_argument_sep missing token child; node=%s", sep.SExpr(lang))
		}
		if child.IsNamed() {
			t.Fatalf("command_argument_sep child is named; node=%s", sep.SExpr(lang))
		}
		switch child.Type(lang) {
		case " ":
			sawSpace = true
		case ":":
			sawColon = true
		default:
			t.Fatalf("command_argument_sep child type = %q, want \" \" or \":\"; node=%s", child.Type(lang), sep.SExpr(lang))
		}
	}
	if !sawSpace || !sawColon {
		t.Fatalf("expected both space and colon command_argument_sep variants; sawSpace=%v sawColon=%v tree=%s", sawSpace, sawColon, root.SExpr(lang))
	}
}
