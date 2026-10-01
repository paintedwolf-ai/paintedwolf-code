package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestLeadingCommentSpanTightness guards a span bug (fixed 2026-05) where a
// top-level node preceded by a leading comment had its span over-extended
// backward over the comment (overlapping its own sibling) and forward over
// trailing whitespace — a divergence from tree-sitter C that affected ~117 of
// 206 grammars on the default parse path. Root cause + fix:
// parser_result_root_build.go (finalizeWrappedSubtree + the post-compatibility
// trailing re-extend). The real node must keep a tight span; the root absorbs
// the surrounding trivia.
func TestLeadingCommentSpanTightness(t *testing.T) {
	cases := []struct{ name, src string }{
		{"go", "// c\n\nfunc f() {}\n"},
		{"python", "# c\n\nx = 1\n"},
		{"rust", "// c\n\nfn f() {}\n"},
		{"java", "// c\n\nclass A {}\n"},
		{"ruby", "# c\n\nx = 1\n"},
		{"c", "// c\n\nint x;\n"},
		// yaml's document node was over-extended over the leading comment by a
		// per-language normalizer (now removed); the document must start after it.
		{"yaml", "# c\n\nkey: value\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			entry := DetectLanguageByName(tc.name)
			if entry == nil || entry.Language == nil {
				t.Skipf("language %q not registered", tc.name)
			}
			lang := entry.Language()
			tree, err := gotreesitter.NewParser(lang).Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("%s parse error: %v", tc.name, err)
			}
			defer tree.Release()
			root := tree.RootNode()

			var comment, firstReal *gotreesitter.Node
			for i := 0; i < root.NamedChildCount(); i++ {
				c := root.NamedChild(i)
				switch {
				case c.IsExtra() && comment == nil:
					comment = c
				case !c.IsExtra() && firstReal == nil:
					firstReal = c
				}
			}
			if comment == nil {
				t.Fatalf("%s: expected a leading comment child; tree: %s", tc.name, root.SExpr(lang))
			}
			if firstReal == nil {
				t.Fatalf("%s: expected a real statement after the comment; tree: %s", tc.name, root.SExpr(lang))
			}

			// The real statement must not overlap the leading comment: its start
			// must be at or after the comment's end. The bug pulled it to byte 0.
			if firstReal.StartByte() < comment.EndByte() {
				t.Errorf("%s: %s span [%d:%d] overlaps leading comment [%d:%d] — start pulled back over the comment",
					tc.name, firstReal.Type(lang), firstReal.StartByte(), firstReal.EndByte(),
					comment.StartByte(), comment.EndByte())
			}
			// The root must still cover the whole source (incl. the trailing newline).
			if int(root.EndByte()) != len(tc.src) {
				t.Errorf("%s: root EndByte=%d but source len=%d — trailing trivia dropped from the root",
					tc.name, root.EndByte(), len(tc.src))
			}
		})
	}
}
