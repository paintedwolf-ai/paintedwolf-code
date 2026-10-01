package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func decodeListDirOut(t *testing.T, out string) listDirResponse {
	t.Helper()
	var resp listDirResponse
	testutil.FailErr(t, "decode list_dir out", json.Unmarshal([]byte(out), &resp))
	return resp
}

func listDirOutJSON(t *testing.T, out string) string {
	t.Helper()
	resp := decodeListDirOut(t, out)
	raw, err := surveyjson.Marshal(resp)
	testutil.FailErr(t, "marshal list_dir resp", err)
	return string(raw)
}

func TestListDirAltitudeUnboundedMap(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc Main() {}\n"), 0o644))
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "."}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir", err)
	resp := decodeListDirOut(t, out)
	if resp.View == "" {
		t.Fatalf("view empty want map or tags; resp=%+v", resp)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("entries = %d want omitted on unbounded map", len(resp.Entries))
	}
	if resp.Coverage == nil || resp.Coverage.EntriesReturned != 1 {
		t.Fatalf("map coverage=%+v", resp.Coverage)
	}

}

func TestListDirAltitudeTargetedLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hi"), 0o644))
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir", err)
	resp := decodeListDirOut(t, out)
	if resp.View != "" {
		t.Fatalf("view = %q want empty for literal listing", resp.View)
	}
	if len(resp.Entries) == 0 {
		t.Fatal("expected entries for targeted listing")
	}
	if evidence.ListEvidenceSurvey(map[string]any{"path": ".", "max_depth": 1}, listDirOutJSON(t, out)) {
		t.Fatal("targeted list_dir must not be survey-grade")
	}
}

func TestListDirAltitudeSubpathTargeted(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Join(tmpDir, "pkg"), 0o755))
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "pkg"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir", err)
	resp := decodeListDirOut(t, out)
	if resp.View != "" {
		t.Fatalf("view = %q want literal subpath listing", resp.View)
	}
}

func TestListDirEmptyRootStatesEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "."}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir", err)
	resp := decodeListDirOut(t, out)
	if resp.Diagnostics == nil || resp.Diagnostics.HintCode != "REPO_MAP_NO_FILES" {
		t.Fatalf("diagnostics = %+v want hint_code REPO_MAP_NO_FILES", resp.Diagnostics)
	}
	if len(resp.Entries) != 0 || resp.Diagnostics.Hint == "" {
		t.Fatalf("empty view lost its counts or recovery: %+v", resp)
	}
}

func TestListDirEmptyLiteralStatesEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write hidden", os.WriteFile(filepath.Join(tmpDir, ".hidden"), []byte("x"), 0o644))
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir", err)
	resp := decodeListDirOut(t, out)
	if len(resp.Entries) != 0 {
		t.Fatalf("entries = %d want 0", len(resp.Entries))
	}
	if !strings.Contains(resp.Note, "Empty") || !strings.Contains(resp.Note, "hidden") {
		t.Fatalf("note = %q want empty statement with hidden qualifier", resp.Note)
	}
}

func TestListDirHasExpressedScopeClassifier(t *testing.T) {
	if !listDirHasExpressedScope(map[string]any{"offset": 0}, ".") {
		t.Fatal("offset should express scope")
	}
	if !listDirHasExpressedScope(map[string]any{"max_depth": 1}, ".") {
		t.Fatal("max_depth should express scope")
	}
	if listDirHasExpressedScope(map[string]any{}, ".") {
		t.Fatal("bare root should not express scope")
	}
	if !listDirHasExpressedScope(map[string]any{}, "src") {
		t.Fatal("subpath should express scope")
	}
}
