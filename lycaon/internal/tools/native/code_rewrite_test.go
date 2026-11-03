package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"
)

func TestRewriteSourceAcrossLanguages(t *testing.T) {
	cases := []struct {
		file, src, pattern, rewrite, want string
	}{
		{"main.go", "package main\n\nfunc main() {\n\tfmt.Println(\"x\")\n}\n", `fmt.Println($A)`, `log.Print($A)`, "package main\n\nfunc main() {\n\tlog.Print(\"x\")\n}\n"},
		{"app.py", "print(value)\n", `print($A)`, `log($A)`, "log(value)\n"},
		{"app.ts", "const x = foo(1);\n", `foo($A)`, `bar($A)`, "const x = bar(1);\n"},
		{"lib.rs", "fn m() {\n    foo(1);\n}\n", `foo($A)`, `bar($A)`, "fn m() {\n    bar(1);\n}\n"},
		{"Main.java", "class C {\n    void m() {\n        foo(1);\n    }\n}\n", `foo($A)`, `bar($A)`, "class C {\n    void m() {\n        bar(1);\n    }\n}\n"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got, changed, err := RewriteSource(t.Context(), c.file, c.pattern, c.rewrite, c.src)
			testutil.FailErr(t, "rewrite source", err)
			if !changed || got != c.want || !SupportsRewriteLanguage(c.file) {
				t.Fatalf("changed=%v got=%q want=%q", changed, got, c.want)
			}
			unchanged, changed, err := RewriteSource(t.Context(), c.file, c.pattern, c.rewrite, got)
			testutil.FailErr(t, "rewrite without matches", err)
			if changed || unchanged != got {
				t.Fatalf("unmatched source changed: %q", unchanged)
			}
		})
	}
}
func TestRewriteSourceRejectsInvalidAndBroadPatterns(t *testing.T) {
	for _, tc := range []struct{ path, source, pattern, rewrite, code string }{
		{"main.go", "package main\n", "func (", "func ()", "STRUCTURAL_PATTERN_INVALID"},
		{"app.py", "x = (0 << 6) | 3\ny: float | None = None\n", "$T | $B", "Union[$T, $B]", "CODE_REWRITE_PATTERN_TOO_BROAD"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, changed, err := RewriteSource(t.Context(), tc.path, tc.pattern, tc.rewrite, tc.source)
			reject := toolrejection.AsToolReject(err)
			if changed || reject == nil || reject.Code != tc.code {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
		})
	}
}
func TestRewriteSourceNarrowUnionPreservesBitwiseExpression(t *testing.T) {
	got, changed, err := RewriteSource(t.Context(), "app.py", "$T | None", "Union[$T, None]", "x = (0 << 6) | 3\ny: float | None = None\n")
	testutil.FailErr(t, "rewrite narrow union", err)
	if !changed || !strings.Contains(got, "Union[float, None]") || !strings.Contains(got, "(0 << 6) | 3") {
		t.Fatalf("rewritten=%q changed=%v", got, changed)
	}
}
func TestRewriteSourceUnsupportedLanguageIsUnchanged(t *testing.T) {
	got, changed, err := RewriteSource(t.Context(), "data.qzx9", "$A", "$A", "hello\n")
	testutil.FailErr(t, "unsupported language", err)
	if got != "hello\n" || changed || SupportsRewriteLanguage("data.qzx9") {
		t.Fatalf("got=%q changed=%v", got, changed)
	}
}
func TestRewriteSourcePreservesParserFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := RewriteSource(ctx, "a.go", "f($A)", "g($A)", "package p\nfunc run() { f(1) }\n")
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "SOURCE_PARSE_INCOMPLETE" || reject.Data["parse_reason"] != "canceled" {
		t.Fatalf("parser failure=%v", err)
	}
}
