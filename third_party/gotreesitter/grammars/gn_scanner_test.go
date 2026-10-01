//go:build !grammar_subset || grammar_subset_gn

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func gnParse(t *testing.T, src string) string {
	t.Helper()
	lang := GnLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", src, err)
	}
	t.Cleanup(tree.Release)
	root := tree.RootNode()
	if root == nil {
		t.Fatalf("Parse(%q) returned nil root", src)
	}
	return root.SExpr(lang)
}

// TestGnStringScannerMatchesC locks the external scanner to the semantics of
// the pinned upstream scanner.c (tree-sitter-gn @ bc06955b):
//   - $identifier ends string content (not just ${...})
//   - a backslash only ends content when it begins \" \$ or \\ — otherwise
//     the backslash and the following char are content
//   - '$' followed by a non-identifier char is content (both chars consumed)
func TestGnStringScannerMatchesC(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "dollar identifier interpolation",
			src:  `a = "hi$name"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content (expansion (identifier)))))))`,
		},
		{
			name: "dollar brace interpolation",
			src:  `a = "hi${name}"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content (expansion (identifier)))))))`,
		},
		{
			name: "escaped dollar",
			src:  `a = "p\$q"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content (escape_sequence))))))`,
		},
		{
			name: "non-escape backslash is content",
			src:  `a = "p\dq"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content)))))`,
		},
		{
			name: "dollar followed by non-identifier is content",
			src:  `a = "1$ 2"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content)))))`,
		},
		{
			// The C scanner consumes "$0..." as content ($ not followed by
			// a letter/_/{), so no expansion node appears.
			name: "dollar digit is content",
			src:  `a = "x$0x0Ay"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content)))))`,
		},
		{
			name: "string starting with expansion",
			src:  `a = "$name"`,
			want: `(source_file (assignment_statement (identifier) (primary_expression (string (string_content (expansion (identifier)))))))`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gnParse(t, tc.src)
			if got != tc.want {
				t.Fatalf("SExpr mismatch for %q:\n got  %s\n want %s", tc.src, got, tc.want)
			}
		})
	}
}
