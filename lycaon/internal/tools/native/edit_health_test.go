package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

const cleanGoFixture = "package p\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n"

// A splice that breaks a clean baseline is rejected and not applied.
func TestMutationHealthFlagsRegression(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(cleanGoFixture), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.go", "start_line": 4, "end_line": 4, "new_content": "\treturn (1",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "MUTATION_BROKE_PARSE" {
		t.Fatalf("err = %v want MUTATION_BROKE_PARSE", err)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "a.go"))
	testutil.FailErr(t, "read", err)
	if string(got) != cleanGoFixture {
		t.Fatalf("broken splice must not be applied: %q", got)
	}
}

// Editing an already-broken file must strictly reduce syntax burden.
func TestMutationHealthRejectsNonImprovingBrokenEdit(t *testing.T) {
	tmpDir := t.TempDir()
	broken := "package p\n\nfunc A( {\n\treturn 1\n}\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(broken), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.go", "start_line": 4, "end_line": 4, "new_content": "\treturn 2",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "MUTATION_REPAIR_NOT_IMPROVED" {
		t.Fatalf("err = %v want MUTATION_REPAIR_NOT_IMPROVED", err)
	}
}

func TestMutationHealthAllowsImprovingBrokenEdit(t *testing.T) {
	tmpDir := t.TempDir()
	broken := "package p\n\nfunc A( {\n\treturn 1\n}\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(broken), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.go", "start_line": 3, "end_line": 3, "new_content": "func A() {",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "replace_lines", err)
}

func TestMutationHealthRejectsBrokenNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "new.py", "content": "def broken(:\n    pass\n",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "MUTATION_BROKE_PARSE" {
		t.Fatalf("err = %v want MUTATION_BROKE_PARSE", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "new.py")); !os.IsNotExist(statErr) {
		t.Fatalf("broken new file was applied: %v", statErr)
	}
}

func TestMutationHealthPythonRejectIncludesIndentContext(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "\"\"\"module\"\"\"\n\ndef run():\n    try:\n        work()\n    except Exception:\n        recover()\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "pipeline.py"), []byte(seed), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "pipeline.py", "start_line": 6, "end_line": 7, "new_content": "    cleanup()",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "MUTATION_BROKE_PARSE" {
		t.Fatalf("err = %v want MUTATION_BROKE_PARSE", err)
	}
	contextRows, ok := reject.Data["python_indent_context"].([]syntaxhealth.LineContext)
	if !ok || len(contextRows) == 0 {
		t.Fatalf("Python indentation context missing: %+v", reject.Data)
	}
	if parseError, _ := reject.Data["parse_error"].(string); parseError == "" || strings.Contains(parseError, "line 1") {
		t.Fatalf("parse_error = %q, want localized seam fault", parseError)
	}
	got, readErr := os.ReadFile(filepath.Join(tmpDir, "pipeline.py"))
	testutil.FailErr(t, "read", readErr)
	if string(got) != seed {
		t.Fatalf("rejected Python mutation changed file: %q", got)
	}
}

// A clean edit on a clean file stays quiet.
func TestMutationHealthSilentOnCleanEdit(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(cleanGoFixture), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "a.go", "start_line": 4, "end_line": 4, "new_content": "\treturn 42",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "replace_lines", err)
	if strings.Contains(out, "⚠") {
		t.Fatalf("clean edit should not warn, got:\n%s", out)
	}
}

// Non-grammar files (no parser) make no health claim.
func TestMutationHealthSilentForNonCode(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte("one\ntwo\nthree\n"), 0o644))
	tool := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "notes.txt", "start_line": 2, "end_line": 2, "new_content": "TWO ((",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "replace_lines", err)
	if strings.Contains(out, "⚠") {
		t.Fatalf("text file should not warn, got:\n%s", out)
	}
}

func TestWriteGoWithoutFinalNewlinePreservesSourceAndParseGuard(t *testing.T) {
	for _, fixture := range []string{"embed.go", "models.go"} {
		t.Run(fixture, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "syntaxhealth", "testdata", "go-eof", fixture))
			testutil.FailErr(t, "load rejected session payload", err)
			if len(source) == 0 || source[len(source)-1] == '\n' {
				t.Fatal("regression fixture must omit the final newline")
			}
			root := t.TempDir()
			tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
			_, err = tool.Run(t.Context(), map[string]any{"path": fixture, "content": string(source)}, nativefixture.Context(root))
			testutil.FailErr(t, "write valid Go source", err)
			got, err := os.ReadFile(filepath.Join(root, fixture))
			testutil.FailErr(t, "read landed source", err)
			if string(got) != string(source) {
				t.Fatal("write changed the source bytes")
			}
			_, err = tool.Run(t.Context(), map[string]any{"path": fixture, "content": "package p\ntype Broken struct {"}, nativefixture.Context(root))
			var reject *tools.ToolReject
			if !errors.As(err, &reject) || reject.Code != "MUTATION_BROKE_PARSE" {
				t.Fatalf("invalid source rejection = %v", err)
			}
			got, err = os.ReadFile(filepath.Join(root, fixture))
			testutil.FailErr(t, "read preserved source", err)
			if string(got) != string(source) {
				t.Fatal("rejected overwrite changed valid source")
			}
		})
	}
}
