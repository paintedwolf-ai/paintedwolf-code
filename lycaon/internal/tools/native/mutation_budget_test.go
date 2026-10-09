package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// Native mutations replace whole-file states; every replaced state must fit
// what version history and checkpoints retain.
func TestMutationBudgetIsTheRetentionBound(t *testing.T) {
	if readcaps.MaxMutationBytes != sourceblob.MaxRevisionContentBytes {
		t.Fatalf("MaxMutationBytes = %d, want MaxRevisionContentBytes %d", readcaps.MaxMutationBytes, sourceblob.MaxRevisionContentBytes)
	}
	if readcaps.MaxMutationBytes >= readcaps.MaxFileBytes {
		t.Fatalf("MaxMutationBytes = %d, want below the read budget %d", readcaps.MaxMutationBytes, readcaps.MaxFileBytes)
	}
}

// padTo grows prefix with a trailing comment line to exactly size bytes.
func padTo(prefix, comment string, size int) string {
	return prefix + comment + strings.Repeat("x", size-len(prefix)-len(comment)-1) + "\n"
}

func requireSizeReject(t *testing.T, err error, minSize int) {
	t.Helper()
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "EDIT_FILE_TOO_LARGE" {
		t.Fatalf("err = %v, want EDIT_FILE_TOO_LARGE", err)
	}
	if got, _ := reject.Data["max_file_bytes"].(int64); got != readcaps.MaxMutationBytes {
		t.Fatalf("max_file_bytes = %v, want %d", reject.Data["max_file_bytes"], readcaps.MaxMutationBytes)
	}
	if size, _ := reject.Data["size"].(int64); size < int64(minSize) {
		t.Fatalf("size = %v, want at least %d", reject.Data["size"], minSize)
	}
}

func requireUnchanged(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read unchanged file", err)
	if string(got) != want {
		t.Fatalf("%s changed on a rejected mutation", filepath.Base(path))
	}
}

func TestNativeEditsRejectABaseOverTheBudget(t *testing.T) {
	over := readcaps.MaxMutationBytes + 1
	for _, tc := range []struct {
		name string
		file string
		body string
		run  func(boundary *sandbox.Boundary, dir string) error
	}{
		{"edit", "big.txt", padTo("target\n", "", over), func(b *sandbox.Boundary, dir string) error {
			_, err := (&EditTool{Boundary: b}).Run(context.Background(), map[string]any{"path": "big.txt", "old_string": "target", "new_string": "done"}, nativefixture.Context(dir))
			return err
		}},
		{"replace_lines", "big.txt", padTo("target\n", "", over), func(b *sandbox.Boundary, dir string) error {
			_, err := (&ReplaceLinesTool{Boundary: b}).Run(context.Background(), map[string]any{"path": "big.txt", "start_line": 1, "end_line": 1, "new_content": "done"}, nativefixture.Context(dir))
			return err
		}},
		{"write replaces", "big.txt", padTo("target\n", "", over), func(b *sandbox.Boundary, dir string) error {
			_, err := (&WriteTool{Boundary: b}).Run(context.Background(), map[string]any{"path": "big.txt", "content": "small\n"}, nativefixture.Context(dir))
			return err
		}},
		{"jq_edit", "big.json", `{"port":1,"pad":"` + strings.Repeat("x", over) + "\"}\n", func(b *sandbox.Boundary, dir string) error {
			_, err := (&JqEditTool{Boundary: b}).Run(context.Background(), map[string]any{"path": "big.json", "query": ".port = 2"}, nativefixture.Context(dir))
			return err
		}},
		{"code_rewrite", "big.go", padTo("package p\n\nfunc f() { fmt.Println(1) }\n", "// ", over), func(b *sandbox.Boundary, dir string) error {
			_, err := (&CodeRewriteTool{Boundary: b}).Run(context.Background(), map[string]any{"path": "big.go", "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A)`}, nativefixture.Context(dir))
			return err
		}},
		{"code_rewrite dry run", "big.go", padTo("package p\n\nfunc f() { fmt.Println(1) }\n", "// ", over), func(b *sandbox.Boundary, dir string) error {
			_, err := (&CodeRewriteTool{Boundary: b}).Run(context.Background(), map[string]any{"path": "big.go", "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A)`, "dry_run": true}, nativefixture.Context(dir))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(tc.body), 0o644))
			requireSizeReject(t, tc.run(nativefixture.Boundary(t), dir), over)
			requireUnchanged(t, path, tc.body)
		})
	}
}

// A base under the budget cannot be grown past it; the file keeps its state.
func TestNativeEditsRejectGrowthPastTheBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "near.txt")
	body := padTo("target\n", "", readcaps.MaxMutationBytes-8)
	testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(body), 0o644))
	grown := strings.Repeat("y", 64)
	_, err := (&EditTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "near.txt", "old_string": "target", "new_string": grown,
	}, nativefixture.Context(dir))
	requireSizeReject(t, err, readcaps.MaxMutationBytes+1)
	requireUnchanged(t, path, body)

	_, err = (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "created.txt", "content": strings.Repeat("z", readcaps.MaxMutationBytes+1),
	}, nativefixture.Context(dir))
	requireSizeReject(t, err, readcaps.MaxMutationBytes+1)
	if _, statErr := os.Stat(filepath.Join(dir, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected create left a file: %v", statErr)
	}
}

// Typing can grow an open document past what a mutation base may be.
func TestOpenDocumentOverTheBudgetRejectsEdits(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("target\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{
		"r1/a.txt": openDoc("doc-a", padTo("target\n", "", readcaps.MaxMutationBytes+1), true),
	}}
	_, err := (&EditTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "a.txt", "old_string": "target", "new_string": "done",
	}, editorCtx(dir, docs))
	requireSizeReject(t, err, readcaps.MaxMutationBytes+1)
	if len(docs.applied) != 0 {
		t.Fatalf("applied %d edits to an over-budget document", len(docs.applied))
	}
}

// code_rewrite into an open document lands without the file write path; the
// content guard still holds it to the budget.
func TestCodeRewriteCannotGrowAnOpenDocumentPastTheBudget(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644))
	draft := padTo("package p\n\nfunc f() { fmt.Println(1) }\n", "// ", readcaps.MaxMutationBytes-8)
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.go": openDoc("doc-a", draft, true)}}
	_, err := (&CodeRewriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "a.go", "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A, $A, $A, $A, $A, $A)`,
	}, editorCtx(dir, docs))
	requireSizeReject(t, err, readcaps.MaxMutationBytes+1)
	if len(docs.applied) != 0 {
		t.Fatalf("applied %d rewrites past the budget", len(docs.applied))
	}
}

// A multi-file rewrite blocks only the file it would grow past the budget.
func TestCodeRewriteMultiBlocksTheOverBudgetFile(t *testing.T) {
	root := t.TempDir()
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{}, unsaved: true}
	writeTreeFile(t, root, "small.go", "package p\nfunc f() { fmt.Println(1) }\n")
	writeTreeFile(t, root, "near.go", "package p\n")
	docs.docs["r1/near.go"] = openDoc("near", padTo("package p\n\nfunc g() { fmt.Println(2) }\n", "// ", readcaps.MaxMutationBytes-8), true)
	raw, err := (&CodeRewriteTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{
		"paths": []any{"."}, "recursive": true, "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A, $A, $A, $A, $A, $A)`,
	}, editorCtx(root, docs))
	testutil.FailErr(t, "multi rewrite", err)
	var result multiApplyOut
	testutil.FailErr(t, "decode result", json.Unmarshal([]byte(raw), &result))
	if len(result.Blocked) != 1 || result.Blocked[0].Path != "near.go" || result.Blocked[0].Code != "EDIT_FILE_TOO_LARGE" {
		t.Fatalf("blocked = %+v, want near.go EDIT_FILE_TOO_LARGE", result.Blocked)
	}
	if len(result.Changed) != 1 || result.Changed[0].Path != "small.go" {
		t.Fatalf("changed = %+v, want only small.go", result.Changed)
	}
	for _, batch := range docs.batches {
		for _, edit := range batch {
			if edit.DocumentID == "near" {
				t.Fatal("over-budget rewrite reached the document transaction")
			}
		}
	}
}

// Reads keep the larger budget: files between the two stay readable.
func TestReadServesFilesBetweenTheBudgets(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "big.txt"), []byte(padTo("first\n", "", readcaps.MaxMutationBytes+1)), 0o644))
	_, err := (&surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "big.txt", "offset": 1, "limit": 1,
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "read between budgets", err)
}

func TestGuardMutationContent(t *testing.T) {
	testutil.FailErr(t, "text content", guardMutationContent("write", "a.py", "ok"))
	var reject *toolrejection.ToolReject
	if err := guardMutationContent("write", "a.bin", "a\x00b"); !errors.As(err, &reject) || reject.Code != "WRITE_BINARY_DENIED" {
		t.Fatalf("binary content = %v, want WRITE_BINARY_DENIED", err)
	}
	requireSizeReject(t, guardMutationContent("write", "big.txt", strings.Repeat("x", readcaps.MaxMutationBytes+1)), readcaps.MaxMutationBytes+1)
}
