package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func TestGrepToolSubstring(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "findme.txt"), []byte("line1\nneedle here\nline3"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 || matches[0]["line"].(float64) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestGrepToolRegex(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("func Foo()\nfunc Bar()\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": `func \w+\(`,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep regex", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0]["match"] == nil || matches[0]["match"].(string) == "" {
		t.Fatalf("expected match field in regex mode: %+v", matches[0])
	}
}

func TestGrepToolStructuralMatchesAcrossTree(t *testing.T) {
	tmpDir := t.TempDir()
	files := map[string]string{
		"a.go":      "package main\n\nfunc main() {\n\tfmt.Println(\"a\")\n}\n",
		"sub/b.go":  "package sub\n\nfunc Run() {\n\tfmt.Println(\"b\")\n}\n",
		"notes.txt": "fmt.Println(\"text not code\")\n",
	}
	for rel, body := range files {
		full := filepath.Join(tmpDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":    `fmt.Println($A)`,
		"structural": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep structural", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 2 {
		t.Fatalf("expected 2 structural matches (txt skipped), got %+v", matches)
	}
	bindings, ok := matches[0]["bindings"].(map[string]any)
	if !ok || bindings["A"] == nil {
		t.Fatalf("expected $A binding in structural match: %+v", matches[0])
	}
}

func TestGrepToolMetavarAutoEnablesStructural(t *testing.T) {
	tmpDir := t.TempDir()
	files := map[string]string{
		"a.go":      "package main\n\nfunc main() {\n\tfmt.Println(\"a\")\n}\n",
		"notes.txt": "fmt.Println(\"text not code\")\n",
	}
	for rel, body := range files {
		full := filepath.Join(tmpDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	// The metavariable selects structural matching without an explicit flag.
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": `fmt.Println($A)`,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep metavar auto-structural", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 {
		t.Fatalf("expected 1 structural match (txt skipped), got %+v", matches)
	}
	if bindings, ok := matches[0]["bindings"].(map[string]any); !ok || bindings["A"] == nil {
		t.Fatalf("expected $A binding from auto-structural match: %+v", matches[0])
	}
}

func TestGrepToolDollarAnchorStaysText(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("ends with foo\nfoo bar\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	// An end anchor is not a structural metavariable.
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": `foo$`,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep dollar anchor", err)
	if matches := nativefixture.GrepMatches(t, out); len(matches) != 1 {
		t.Fatalf("expected 1 text match on foo$, got %+v", matches)
	}
}

func TestGrepToolAnchorsDoNotMatchPhantomLines(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "line.txt"), []byte("one\n"), 0o644); err != nil {
		testutil.FailErr(t, "write terminated line", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "empty.txt"), nil, 0o644); err != nil {
		testutil.FailErr(t, "write empty file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	for _, contextLines := range []int{0, 1} {
		out, err := tool.Run(context.Background(), map[string]any{
			"pattern": `$`, "path": "line.txt", "context_lines": contextLines,
		}, nativefixture.Context(tmpDir))
		testutil.FailErr(t, "grep terminated line", err)
		if matches := nativefixture.GrepMatches(t, out); len(matches) != 1 || matches[0]["line"] != float64(1) {
			t.Fatalf("context_lines=%d matches = %+v, want only line 1", contextLines, matches)
		}

		out, err = tool.Run(context.Background(), map[string]any{
			"pattern": `^$`, "path": "empty.txt", "context_lines": contextLines,
		}, nativefixture.Context(tmpDir))
		testutil.FailErr(t, "grep empty file", err)
		if matches := nativefixture.GrepMatches(t, out); len(matches) != 0 {
			t.Fatalf("context_lines=%d empty-file matches = %+v", contextLines, matches)
		}
	}
}

func TestGrepToolStructuralInvalidPatternRejects(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern":    "func (",
		"structural": true,
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "STRUCTURAL_PATTERN_INVALID" {
		t.Fatalf("want STRUCTURAL_PATTERN_INVALID reject, got %v", err)
	}
}

func TestGrepToolStructuralUnknownLangRejects(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern":    "$A",
		"structural": true,
		"lang":       "klingon",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "STRUCTURAL_LANG_UNKNOWN" {
		t.Fatalf("want STRUCTURAL_LANG_UNKNOWN reject, got %v", err)
	}
}

func TestGrepToolStructuralNoMatchNote(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":    `fmt.Println($A)`,
		"structural": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep structural", err)
	if len(nativefixture.GrepMatches(t, out)) != 0 {
		t.Fatalf("expected no matches: %s", out)
	}
	var resp struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		testutil.FailErr(t, "decode note", err)
	}
	if !strings.Contains(resp.Note, "no structural matches") {
		t.Fatalf("expected structural note, got %q", resp.Note)
	}
}

func TestGrepToolCaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "x.txt"), []byte("Needle\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":          "needle",
		"case_insensitive": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep case_insensitive", err)
	if len(nativefixture.GrepMatches(t, out)) != 1 {
		t.Fatalf("out = %q", out)
	}
}

func TestGrepToolCaseInsensitiveUsesUnicodeSimpleFold(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "units.txt"), []byte("temperature in \u212a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write Unicode fixture", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "k", "path": "units.txt", "case_insensitive": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep Unicode fold", err)
	if matches := nativefixture.GrepMatches(t, out); len(matches) != 1 || matches[0]["line"] != float64(1) {
		t.Fatalf("Unicode fold matches = %+v", matches)
	}
}

func TestGrepToolRegexIsDefault(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("password=1\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "password|secret",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep regex default", err)
	if len(nativefixture.GrepMatches(t, out)) != 1 {
		t.Fatalf("alternation should match by default: out = %q", out)
	}
}

// Alternation syntax always uses regex semantics.
func TestGrepToolAlternationHasNoLiteralMode(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("password=1\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "password|secret",
		"regex":   false,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep alternation", err)
	if len(nativefixture.GrepMatches(t, out)) != 1 {
		t.Fatalf("alternation must match regardless of a stale regex arg: out = %q", out)
	}
}

func TestGrepToolEmptyResult(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "missing"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep empty", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 0 {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestGrepToolEmptyTextNote(t *testing.T) {
	tmpDir := t.TempDir()
	body := "func (s *Server) mcpProjectDir(w http.ResponseWriter) bool {\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "h.go"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}

	noteFor := func(t *testing.T, args map[string]any) string {
		t.Helper()
		out, err := tool.Run(context.Background(), args, nativefixture.Context(tmpDir))
		testutil.FailErr(t, "grep", err)
		var resp struct {
			Matches []map[string]any `json:"matches"`
			Note    string           `json:"note"`
		}
		if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &resp); err != nil {
			testutil.FailErr(t, "decode", err)
		}
		if len(resp.Matches) != 0 {
			t.Fatalf("expected zero matches, got %+v", resp.Matches)
		}
		return resp.Note
	}

	// Unescaped operators change the literal pattern's meaning.
	note := noteFor(t, map[string]any{"pattern": "func (s *Server) mcpProjectDirX"})
	for _, want := range []string{"operators", "`(`", "`*`", "`)`"} {
		if !strings.Contains(note, want) {
			t.Fatalf("operator note = %q, want %q", note, want)
		}
	}

	// Empty scope: path_glob matched no file, so the pattern was never tried.
	note = noteFor(t, map[string]any{"pattern": "mcpProjectDir", "path_glob": "*.rs"})
	if !strings.Contains(note, "no text files were searched") {
		t.Fatalf("scope note = %q", note)
	}

	// Absence applies only to the text files actually searched.
	note = noteFor(t, map[string]any{"pattern": "nowhereToBeFound"})
	for _, want := range []string{"literal text was not found", "Skipped files are not evidence of absence"} {
		if !strings.Contains(note, want) {
			t.Fatalf("absent note = %q, want %q", note, want)
		}
	}
}

// Matches before the offset suppress empty-search guidance.
func TestGrepToolNoEmptyNoteWhenOffsetConsumed(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hit\nhit\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "hit",
		"offset":  float64(50),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep offset", err)
	var resp struct {
		Matches []map[string]any `json:"matches"`
		Note    string           `json:"note"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &resp); err != nil {
		testutil.FailErr(t, "decode", err)
	}
	if len(resp.Matches) != 0 {
		t.Fatalf("matches = %+v", resp.Matches)
	}
	if resp.Note != "" {
		t.Fatalf("offset-exhausted page must not carry an empty-result note: %q", resp.Note)
	}
}

func TestGrepToolTruncatedAtMaxMatches(t *testing.T) {
	tmpDir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 250; i++ {
		b.WriteString("hit\n")
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "many.txt"), []byte(b.String()), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "hit",
		"max_matches": float64(50),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep truncated", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 50 {
		t.Fatalf("matches len = %d want 50", len(matches))
	}
	body := nativefixture.SurveyContent(t, out)
	var wrap struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(body), &wrap); err != nil {
		testutil.FailErr(t, "decode truncated", err)
	}
	if !wrap.Truncated {
		t.Fatal("expected truncated true")
	}
}

func TestGrepToolContextLines(t *testing.T) {
	tmpDir := t.TempDir()
	content := "before1\nbefore2\nneedle\nafter1\nafter2\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "ctx.txt"), []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":       "needle",
		"context_lines": float64(2),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep context", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	before, _ := matches[0]["context_before"].([]any)
	after, _ := matches[0]["context_after"].([]any)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("context = before %v after %v", before, after)
	}
}

func TestGrepToolSkipsGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git", "objects"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "objects", "secret"), []byte("secret-token\n"), 0o644); err != nil {
		testutil.FailErr(t, "write git file", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("secret-token\n"), 0o644); err != nil {
		testutil.FailErr(t, "write visible", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "secret-token"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep skip git", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v want only visible.txt", matches)
	}
}

func TestGrepToolSkipsBinary(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "bin.dat"), []byte("text\x00binary"), 0o644); err != nil {
		testutil.FailErr(t, "write binary", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "text.txt"), []byte("text\n"), 0o644); err != nil {
		testutil.FailErr(t, "write text", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "text"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep binary skip", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestGrepToolPathGlobNested(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "lycaon", "internal"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "lycaon", "internal", "tools.go"), []byte("package internal\n"), 0o644); err != nil {
		testutil.FailErr(t, "write nested go", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte("package internal\n"), 0o644); err != nil {
		testutil.FailErr(t, "write txt", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":   "package",
		"path_glob": "*.go",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep nested path_glob", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 || matches[0]["path"].(string) != "lycaon/internal/tools.go" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestGrepToolPathGlob(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write go", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("package a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write txt", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":   "package",
		"path_glob": "*.go",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep path_glob", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 || matches[0]["path"].(string) != "a.go" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestGrepToolPathEscape(t *testing.T) {
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern": "x",
		"path":    "../etc/passwd",
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestGrepToolInvalidRegex(t *testing.T) {
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern": "[unclosed",
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil {
		t.Fatal("expected regex invalid error")
	}
	if !errors.As(err, &reject) || reject.Code != "GREP_REGEX_INVALID" {
		t.Fatalf("err = %v want GREP_REGEX_INVALID", err)
	}
}

func TestGrepToolPatternTooLong(t *testing.T) {
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern": strings.Repeat("a", safecmd.GrepMaxPatternLen+1),
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil {
		t.Fatal("expected pattern budget error")
	}
	if !errors.As(err, &reject) || reject.Code != "GREP_MATCH_BUDGET" {
		t.Fatalf("err = %v want GREP_MATCH_BUDGET", err)
	}
}

func TestGrepToolNestedQuantifierRejected(t *testing.T) {
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern": `(a+)+`,
	}, nativefixture.Context(t.TempDir()))
	var reject *tools.ToolReject
	if err == nil {
		t.Fatal("expected nested quantifier reject")
	}
	if !errors.As(err, &reject) || reject.Code != "GREP_MATCH_BUDGET" {
		t.Fatalf("err = %v want GREP_MATCH_BUDGET", err)
	}
	if reject.Data["grep_nested_repeat"] != true {
		t.Fatalf("nested-repeat cause missing: %v", reject.Data)
	}

}

func TestGrepToolOffsetPagination(t *testing.T) {
	tmpDir := t.TempDir()
	for i := range 3 {
		name := filepath.Join(tmpDir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(name, []byte("needle\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "needle",
		"max_matches": float64(1),
		"offset":      float64(1),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep offset", err)
	var resp struct {
		Matches    []map[string]any `json:"matches"`
		Offset     float64          `json:"offset"`
		Truncated  bool             `json:"truncated"`
		NextOffset *float64         `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &resp); err != nil {
		testutil.FailErr(t, "decode grep offset response", err)
	}
	if len(resp.Matches) != 1 || !resp.Truncated || resp.NextOffset == nil || int(*resp.NextOffset) != 2 || int(resp.Offset) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestGrepToolFilesBytesTruncated(t *testing.T) {
	prev := hostGrepMaxFileBytes
	hostGrepMaxFileBytes = 32 // tiny cap — avoid multi-MiB fixtures on disk
	t.Cleanup(func() { hostGrepMaxFileBytes = prev })

	tmpDir := t.TempDir()
	// 32-byte prefix searched; needle-tail sits past the cap.
	body := "needle-head\n" + strings.Repeat("x", 20) + "needle-tail\n"
	if len(body) <= hostGrepMaxFileBytes {
		t.Fatalf("fixture must exceed cap: len=%d cap=%d", len(body), hostGrepMaxFileBytes)
	}
	testutil.FailErr(t, "write huge.log", os.WriteFile(filepath.Join(tmpDir, "huge.log"), []byte(body), 0o644))

	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "needle-head",
		"path":        "huge.log",
		"max_matches": float64(10),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep head", err)
	var headResp struct {
		Matches             []map[string]any `json:"matches"`
		FilesBytesTruncated int              `json:"files_bytes_truncated"`
		TruncationBanner    string           `json:"truncation_banner"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &headResp); err != nil {
		testutil.FailErr(t, "decode head response", err)
	}
	if len(headResp.Matches) != 1 {
		t.Fatalf("head matches = %+v", headResp.Matches)
	}
	if headResp.FilesBytesTruncated != 1 {
		t.Fatalf("files_bytes_truncated = %d want 1", headResp.FilesBytesTruncated)
	}
	if !strings.Contains(headResp.TruncationBanner, "grep cap") {
		t.Fatalf("banner = %q want grep cap notice", headResp.TruncationBanner)
	}

	tailOut, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "needle-tail",
		"path":        "huge.log",
		"max_matches": float64(10),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep tail", err)
	var tailResp struct {
		Matches             []map[string]any `json:"matches"`
		FilesBytesTruncated int              `json:"files_bytes_truncated"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, tailOut)), &tailResp); err != nil {
		testutil.FailErr(t, "decode tail response", err)
	}
	if len(tailResp.Matches) != 0 {
		t.Fatalf("tail past cap must not match; got %+v", tailResp.Matches)
	}
	if tailResp.FilesBytesTruncated != 1 {
		t.Fatalf("files_bytes_truncated = %d want 1 for oversized file", tailResp.FilesBytesTruncated)
	}
}

// Declaration patterns retain the function name when wrapped for parsing.
func TestGrepToolStructuralFuncDeclarationByName(t *testing.T) {
	tmpDir := t.TempDir()
	body := "package p\n\nfunc Run(a int) string { return \"x\" }\n\n" +
		"func other(t *T) {\n\tf := func(t *T) { println(\"closure\") }\n\t_ = f\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":    `func Run($$$ARGS) $RET { $$$BODY }`,
		"structural": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep structural", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for the named function, got %+v", matches)
	}
	content, _ := matches[0]["content"].(string)
	if strings.Contains(content, "closure") {
		t.Fatalf("named function pattern matched a closure: %q", content)
	}
	bindings, ok := matches[0]["bindings"].(map[string]any)
	if !ok || bindings["RET"] != "string" {
		t.Fatalf("expected RET=string binding, got %+v", matches[0])
	}
}

func TestGrepToolStructuralNoMatchNoteBlamesPatternWhenFilesParsed(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": `fmt.Println($A)`, "structural": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep structural", err)
	var resp struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		testutil.FailErr(t, "decode note", err)
	}
	if !strings.Contains(resp.Note, "1 file(s) were searched") {
		t.Fatalf("note should report what was searched, got %q", resp.Note)
	}
	if strings.Contains(resp.Note, "no file in scope has a tree-sitter grammar") {
		t.Fatalf("note must not blame grammars for a parsed file, got %q", resp.Note)
	}
}

func TestEmptyStructuralNoteBranches(t *testing.T) {
	if got := emptyStructuralNote(t.Context(), 0); !strings.Contains(got, "no file in scope has a tree-sitter grammar") {
		t.Fatalf("zero-parsed note = %q", got)
	}
	if got := emptyStructuralNote(t.Context(), 3); !strings.Contains(got, "3 file(s) were searched") {
		t.Fatalf("parsed note = %q", got)
	}
}
