package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestCodeRewriteMultiFileCodemod(t *testing.T) {
	tmpDir := t.TempDir()
	writeTreeFile(t, tmpDir, "one.go", "package one\n\nfunc f() { fmt.Println(\"a\") }\n")
	writeTreeFile(t, tmpDir, "two.go", "package two\n\nfunc g() { fmt.Println(\"b\") }\n")

	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths":     []any{"."},
		"recursive": true,
		"pattern":   `fmt.Println($A)`,
		"rewrite":   `log.Info($A)`,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "code_rewrite multi apply", err)

	for _, name := range []string{"one.go", "two.go"} {
		got, err := os.ReadFile(filepath.Join(tmpDir, name))
		testutil.FailErr(t, "read", err)
		if !strings.Contains(string(got), "log.Info") {
			t.Fatalf("%s not rewritten: %s", name, got)
		}
	}
}

func TestCodeRewriteMultiFileAllUnsupportedSoftResult(t *testing.T) {
	tmpDir := t.TempDir()
	writeTreeFile(t, tmpDir, "notes.qzx9", "fmt.Println(\"x\")\n")

	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths":   []any{"."},
		"pattern": `fmt.Println($A)`,
		"rewrite": `log.Info($A)`,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "code_rewrite all unsupported", err)
	if !strings.Contains(out, `"file_count":0`) || !strings.Contains(out, "no tree-sitter") {
		t.Fatalf("expected soft note, got: %s", out)
	}
}

func TestCodeRewriteMultiFileDryRunDiff(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "package main\n\nfunc main() { fmt.Println(\"x\") }\n"
	writeTreeFile(t, tmpDir, "main.go", seed)

	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths":   []any{"."},
		"pattern": `fmt.Println($A)`,
		"rewrite": `log.Info($A)`,
		"dry_run": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "code_rewrite dry run", err)
	if !strings.Contains(out, `"dry_run":true`) || !strings.Contains(out, `"changed"`) || !strings.Contains(out, "log.Info") {
		t.Fatalf("expected dry_run changed list with diff, got: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	testutil.FailErr(t, "read", err)
	if string(got) != seed {
		t.Fatal("dry_run must not mutate files")
	}
}

func TestCodeRewriteMultiFileIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	writeTreeFile(t, tmpDir, "main.go", "package main\n\nfunc main() { fmt.Println(\"x\") }\n")

	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	args := map[string]any{
		"paths":   []any{"."},
		"pattern": `fmt.Println($A)`,
		"rewrite": `log.Info($A)`,
	}
	_, err := tool.Run(context.Background(), args, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "first apply", err)
	out, err := tool.Run(context.Background(), args, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "second apply", err)
	if strings.Contains(out, `"changed":[{`) {
		t.Fatalf("second apply should be a no-op: %s", out)
	}
}

func TestCodeRewriteMultiFilePartialWriteScopeBlock(t *testing.T) {
	tmpDir := t.TempDir()
	writeTreeFile(t, tmpDir, "allowed/a.go", "package a\n\nfunc a() { fmt.Println(\"1\") }\n")
	writeTreeFile(t, tmpDir, "denied/b.go", "package b\n\nfunc b() { fmt.Println(\"2\") }\n")

	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
	}, []sandbox.ToolProfile{{
		ID:         toolprofiles.DefaultToolProfileID,
		WriteGlobs: []string{"allowed/**"},
		Tools:      map[string]bool{"code_rewrite": true, "read": true},
	}})
	tool := &CodeRewriteTool{Boundary: boundary}
	tctx := nativefixture.Context(tmpDir)
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths":     []any{"."},
		"recursive": true,
		"pattern":   `fmt.Println($A)`,
		"rewrite":   `log.Info($A)`,
	}, tctx)
	testutil.FailErr(t, "partial block apply", err)
	if !strings.Contains(out, `"blocked"`) || !strings.Contains(out, "WRITE_SCOPE_DENIED") || !strings.Contains(out, "denied/b.go") {
		t.Fatalf("expected blocked path, got: %s", out)
	}
	var result multiApplyOut
	testutil.FailErr(t, "decode partial rewrite", json.Unmarshal([]byte(out), &result))
	if len(result.Blocked) != 1 || result.Blocked[0].Details["patterns_list"] != "- `allowed/**`" {
		t.Fatalf("partial rewrite lost scope details: %+v", result.Blocked)
	}
	if !tctx.Effects.Out.Facts.Succeeded() || len(tctx.Effects.Out.Facts.Feedback) != 1 || tctx.Effects.Out.Facts.Feedback[0].Subject.ID != "denied/b.go" {
		t.Fatalf("partial result became a whole-call rejection or lost its subject: %+v", tctx.Effects.Out.Facts)
	}

	got, err := os.ReadFile(filepath.Join(tmpDir, "allowed/a.go"))
	testutil.FailErr(t, "read allowed", err)
	if !strings.Contains(string(got), "log.Info") {
		t.Fatal("allowed file should be rewritten")
	}
}

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
}

func TestCodeRewriteUnobservedFilesCannotBecomeNoMatches(t *testing.T) {
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "oversize.go"))
	testutil.FailErr(t, "create oversized source", err)
	testutil.FailErr(t, "size oversized source", f.Truncate(repomap.DefaultWalkMaxFileBytes+1))
	testutil.FailErr(t, "close oversized source", f.Close())
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	output, err := tool.Run(t.Context(), map[string]any{"paths": []any{"oversize.go", "missing.go"}, "pattern": "println($A)", "rewrite": "print($A)"}, nativefixture.Context(root))
	testutil.FailErr(t, "rewrite unobserved targets", err)
	var result multiApplyOut
	testutil.FailErr(t, "decode unobserved rewrite", json.Unmarshal([]byte(output), &result))
	if !result.Truncated || result.UnobservedFiles != 2 || len(result.Changed) != 0 || result.Note != "" {
		t.Fatalf("unobserved files became a no-match result: %+v", result)
	}
}

type failedRewriteGate struct{ err error }

func (g failedRewriteGate) GateApply(context.Context, string, string, *string, string, tools.ToolContext) (string, error) {
	return "", g.err
}

func TestCodeRewriteHostFailureIsNotAnInvalidPattern(t *testing.T) {
	root := t.TempDir()
	source := "package probe\nfunc f() { println(\"probe\") }\n"
	writeTreeFile(t, root, "source.go", source)
	failure := errors.New("write policy storage unavailable")
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t), ContentApply: failedRewriteGate{err: failure}}
	output, err := tool.Run(t.Context(), map[string]any{"paths": []any{"source.go"}, "pattern": "println($A)", "rewrite": "print($A)"}, nativefixture.Context(root))
	if !errors.Is(err, failure) || output != "" {
		t.Fatalf("host failure was replaced by pattern recovery or success: output=%s error=%v", output, err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "source.go"))
	testutil.FailErr(t, "read after failed preflight", err)
	if string(actual) != source {
		t.Fatal("failed write-policy preflight changed source")
	}
}
