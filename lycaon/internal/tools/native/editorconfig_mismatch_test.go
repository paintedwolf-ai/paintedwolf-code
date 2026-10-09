package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func writeEditorConfig(t *testing.T, dir, body string) {
	t.Helper()
	testutil.FailErr(t, "write .editorconfig", os.WriteFile(filepath.Join(dir, ".editorconfig"), []byte(body), 0o644))
}

func mismatchDetails(t *testing.T, out *tools.ToolInvocationOut) map[string]any {
	t.Helper()
	if !out.Facts.HasCode(toolrejection.EditorConfigMismatchCode) {
		t.Fatalf("facts = %+v, want %s", out.Facts, toolrejection.EditorConfigMismatchCode)
	}
	if !out.Facts.Succeeded() {
		t.Fatalf("a mismatch must not change the write outcome: %+v", out.Facts)
	}
	return out.Facts.FeedbackFor(toolrejection.EditorConfigMismatchCode).Details
}

func TestWriteReportsDeclaredEditorConfigMismatchWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	writeEditorConfig(t, dir, "root = true\n[*.py]\nindent_style = space\nindent_size = 4\ntrim_trailing_whitespace = true\n")
	tctx := nativefixture.Context(dir)
	tctx.Out = &tools.ToolInvocationOut{}
	content := "def f():\n    x = 1\n\t\n    return x\n"
	_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "app.py", "content": content,
	}, tctx)
	testutil.FailErr(t, "write", err)

	raw, err := os.ReadFile(filepath.Join(dir, "app.py"))
	testutil.FailErr(t, "read written file", err)
	if string(raw) != content {
		t.Fatalf("the host rewrote agent text: %q", raw)
	}
	got := mismatchDetails(t, tctx.Out)
	want := map[string]any{
		"path":               "app.py",
		"editorconfig_rules": []string{"trim_trailing_whitespace", "indent_style"},
		"editorconfig_lines": "app.py:3 trim_trailing_whitespace; app.py:3 indent_style",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("details = %#v, want %#v", got, want)
	}
}

func TestEditReportsOnlyLinesItWrote(t *testing.T) {
	dir := t.TempDir()
	writeEditorConfig(t, dir, "[*]\ntrim_trailing_whitespace = true\n")
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old  \nkeep\n"), 0o644))
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}

	clean := nativefixture.Context(dir)
	clean.Out = &tools.ToolInvocationOut{}
	_, err := edit.Run(context.Background(), map[string]any{"path": "a.txt", "old_string": "keep", "new_string": "kept"}, clean)
	testutil.FailErr(t, "clean edit", err)
	if clean.Out.Facts.HasCode(toolrejection.EditorConfigMismatchCode) {
		t.Fatalf("trailing whitespace the edit left alone was attributed to it: %+v", clean.Out.Facts)
	}

	dirty := nativefixture.Context(dir)
	dirty.Out = &tools.ToolInvocationOut{}
	_, err = edit.Run(context.Background(), map[string]any{"path": "a.txt", "old_string": "kept", "new_string": "kept "}, dirty)
	testutil.FailErr(t, "dirty edit", err)
	if got := mismatchDetails(t, dirty.Out)["editorconfig_lines"]; got != "a.txt:2 trim_trailing_whitespace" {
		t.Fatalf("lines = %v", got)
	}
}

func TestWriteWithoutEditorConfigReportsNothing(t *testing.T) {
	dir := t.TempDir()
	tctx := nativefixture.Context(dir)
	tctx.Out = &tools.ToolInvocationOut{}
	_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "notes.md", "content": "line with hard break  \n\t\n",
	}, tctx)
	testutil.FailErr(t, "write", err)
	if tctx.Out.Facts.HasCode(toolrejection.EditorConfigMismatchCode) {
		t.Fatalf("no rule is declared, so nothing may be reported: %+v", tctx.Out.Facts)
	}
}

// The open document serializes its own line endings, so the agent's LF text
// is not measured against end_of_line.
func TestOpenDocumentEditLeavesLineEndingsToTheDocument(t *testing.T) {
	dir := t.TempDir()
	writeEditorConfig(t, dir, "[*]\nend_of_line = crlf\ntrim_trailing_whitespace = true\n")
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package disk\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.go": openDoc("doc-a", "package draft\n", true)}}
	tctx := editorCtx(dir, docs)
	tctx.Out = &tools.ToolInvocationOut{}
	_, err := (&EditTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "a.go", "old_string": "package draft", "new_string": "package agent ",
	}, tctx)
	testutil.FailErr(t, "edit", err)
	got := mismatchDetails(t, tctx.Out)
	if rules := got["editorconfig_rules"]; !reflect.DeepEqual(rules, []string{"trim_trailing_whitespace"}) {
		t.Fatalf("rules = %#v, want only trim_trailing_whitespace", rules)
	}
}
