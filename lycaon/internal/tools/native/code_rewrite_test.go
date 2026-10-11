package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestCodeRewriteApply(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(seed), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "main.go",
		"pattern": `fmt.Println($A)`,
		"rewrite": `log.Info($A)`,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "code_rewrite apply", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	testutil.FailErr(t, "read file", err)
	want := "package main\n\nfunc main() {\n\tlog.Info(\"hi\")\n}\n"
	if string(got) != want {
		t.Fatalf("content = %q want %q", got, want)
	}
}

func TestCodeRewriteRequiresRewrite(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "main.go",
		"pattern": `fmt.Println($A)`,
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "STRUCTURAL_PATTERN_INVALID" {
		t.Fatalf("want STRUCTURAL_PATTERN_INVALID when rewrite omitted, got %v", err)
	}
	if !strings.Contains(reject.Data["reason"].(string), "grep") {
		t.Fatalf("reject should point at grep for search: %+v", reject.Data)
	}
}

func TestCodeRewriteDryRunReturnsDiff(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(seed), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":    "main.go",
		"pattern": `fmt.Println($A)`,
		"rewrite": `log.Info($A)`,
		"dry_run": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "code_rewrite dry_run", err)
	if !strings.Contains(out, `"dry_run":true`) || !strings.Contains(out, "log.Info") || !strings.Contains(out, "-") {
		t.Fatalf("expected unified diff in dry_run output, got: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	testutil.FailErr(t, "read file", err)
	if string(got) != seed {
		t.Fatalf("dry_run mutated the file:\n%s", got)
	}
}

func TestCodeRewriteInvalidPattern(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "main.go",
		"pattern": "func (",
		"rewrite": "func ()",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "STRUCTURAL_PATTERN_INVALID" {
		t.Fatalf("want STRUCTURAL_PATTERN_INVALID reject, got %v", err)
	}
}

func TestCodeRewriteAcrossLanguagesViaExtension(t *testing.T) {
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
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, c.file), []byte(c.src), 0o644); err != nil {
				testutil.FailErr(t, "write file", err)
			}
			tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
			_, err := tool.Run(context.Background(), map[string]any{
				"path":    c.file,
				"pattern": c.pattern,
				"rewrite": c.rewrite,
			}, nativefixture.Context(tmpDir))
			testutil.FailErr(t, "code_rewrite "+c.file, err)
			got, err := os.ReadFile(filepath.Join(tmpDir, c.file))
			testutil.FailErr(t, "read file", err)
			if string(got) != c.want {
				t.Fatalf("[%s] got %q want %q", c.file, got, c.want)
			}
		})
	}
}

func TestCodeRewriteUnsupportedLangDegradesGracefully(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "data.qzx9"), []byte("hello world\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	msg, err := tool.Run(context.Background(), map[string]any{
		"path":    "data.qzx9",
		"pattern": "$A",
		"rewrite": "$A",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "code_rewrite unsupported apply", err)
	if !strings.Contains(msg, "skipped") {
		t.Fatalf("expected skip message, got: %s", msg)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "data.qzx9"))
	testutil.FailErr(t, "read file", err)
	if string(got) != "hello world\n" {
		t.Fatalf("unsupported apply mutated the file: %q", got)
	}
}

func TestCodeRewriteBareBinaryPatternRejected(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "x = (0 << 6) | 3\ny: float | None = None\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte(seed), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "app.py",
		"pattern": "$T | $B",
		"rewrite": "Union[$T, $B]",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CODE_REWRITE_PATTERN_TOO_BROAD" {
		t.Fatalf("want CODE_REWRITE_PATTERN_TOO_BROAD, got %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "app.py"))
	testutil.FailErr(t, "read file", err)
	if string(got) != seed {
		t.Fatalf("too-broad pattern must not apply: %q", got)
	}
}

func TestCodeRewriteNarrowUnionPatternApplies(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "x = (0 << 6) | 3\ny: float | None = None\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte(seed), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "app.py",
		"pattern": "$T | None",
		"rewrite": "Union[$T, None]",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "narrow union rewrite", err)
	got, err := os.ReadFile(filepath.Join(tmpDir, "app.py"))
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(got), "Union[float, None]") {
		t.Fatalf("narrow rewrite missed the type union: %q", got)
	}
	if !strings.Contains(string(got), "(0 << 6) | 3") {
		t.Fatalf("narrow rewrite must not touch bitwise or: %q", got)
	}
}

func TestCodeRewriteExplicitBadLangRejects(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "main.go",
		"pattern": "$A",
		"rewrite": "$A",
		"lang":    "not-a-language",
	}, nativefixture.Context(tmpDir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "STRUCTURAL_LANG_UNKNOWN" {
		t.Fatalf("want STRUCTURAL_LANG_UNKNOWN reject, got %v", err)
	}
}
