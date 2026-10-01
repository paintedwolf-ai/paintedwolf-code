package tooloutput

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFitWireJSONPreservesFactsWithinBudget(t *testing.T) {
	for _, count := range []int{1, 2, 80, 120} {
		files := make([]any, count)
		for i := range files {
			files[i] = map[string]any{"path": "source.go", "diff": strings.Repeat("+line\n", 2000)}
		}
		raw, err := json.Marshal(map[string]any{"available": true, "files": files, "files_total": 900, "files_truncated": true, "next_offset": 80, "base_oid": "abc", "wire_spill_path": "tool-output/full.txt"})
		testutil.FailErr(t, "marshal fixture", err)
		out, changed := FitWireJSON("[git#1]\n"+string(raw), 1800)
		if !changed || len(out) > 1800 {
			t.Fatalf("count %d: changed=%v bytes=%d", count, changed, len(out))
		}
		prefix, body, _, ok := hostmarker.SplitToolJSONBody(out)
		if !ok || !strings.Contains(prefix, "[git#1]") {
			t.Fatal("lost JSON envelope")
		}
		var got map[string]any
		testutil.FailErr(t, "decode residue", json.Unmarshal([]byte(body), &got))
		if got["available"] != true || got["files_total"] != float64(900) || got["next_offset"] != float64(80) || got["base_oid"] != "abc" || got["wire_spill_path"] != "tool-output/full.txt" {
			t.Fatalf("lost facts: %v", got)
		}
	}
}

func TestFitWireJSONBoundsCommitPathsAndKeepsExactNumbers(t *testing.T) {
	paths := make([]string, 300)
	for i := range paths {
		paths[i] = strings.Repeat("nested/", 30) + "source.go"
	}
	raw, err := json.Marshal(map[string]any{"hash": "commit-id", "uncommitted_paths": paths, "uncommitted_count": 300})
	testutil.FailErr(t, "marshal receipt", err)
	out, changed := FitWireJSON(string(raw), 1200)
	if !changed || len(out) > 1200 || !strings.Contains(out, `"hash":"commit-id"`) || !strings.Contains(out, `"uncommitted_count":300`) {
		t.Fatalf("receipt projection: %s", out)
	}
	exact := `{"sequence":9007199254740993,"stdout":"` + strings.Repeat("x", 10000) + `"}`
	out, changed = FitWireJSON(InjectWireSpillPath(exact, ToolOutputSpillRelPath(exact)), 1000)
	if !changed || !strings.Contains(out, "9007199254740993") {
		t.Fatal("rounded exact numeric fact")
	}
}

func TestWireBudgetRetainsNestedSpillReferences(t *testing.T) {
	nested := ToolOutputSpillRelPath("original hunks")
	original := ToolOutputSpillRelPath("original page")
	newest := ToolOutputSpillRelPath("new page")
	content := `{"wire_spill_path":"` + original + `","files":[{"path":"a","diff_spill_path":"` + nested + `","diff":"` + strings.Repeat("x", 10000) + `"}]}`
	out, changed := FitWireJSON(InjectWireSpillPath(content, newest), 1200)
	if !changed {
		t.Fatal("did not compact")
	}
	refs := SpillPaths(out)
	if len(refs) != 3 {
		t.Fatalf("lost retained references: %v", refs)
	}
}

func TestWireSpillAcceptsAlreadyFittingStructuredResidue(t *testing.T) {
	content := "{\n" + strings.Repeat(" ", 2000) + `"available":true,"sequence":9007199254740993}`
	out := WireSpillToolOutput(t.TempDir(), Screened(content), 1000, 0)
	if out.RejectCode != "" || out.SpillPath == "" || len(out.Preview) > 1000 || !strings.Contains(out.Preview, "9007199254740993") {
		t.Fatalf("fitting residue rejected or changed facts: %+v", out)
	}
}

func TestFitWireJSONPreservesSmallBodiesAndLiteralMarkup(t *testing.T) {
	for _, content := range []string{`{"entries":[{"name":"a"}]}`, `[{"path":"a"}]`, "opaque text"} {
		out, changed := FitWireJSON(content, 100)
		if changed || out != content {
			t.Fatalf("changed an already fitting body: %s", out)
		}
	}
	content := `{"entries":[{"name":"Vec<u8>","note":"a & b","diff":"` + strings.Repeat("x", 10000) + `"}]}`
	out, changed := FitWireJSON(content, 2000)
	if !changed || !strings.Contains(out, "Vec<u8>") || !strings.Contains(out, "a & b") {
		t.Fatalf("lost literal markup: %s", out)
	}
}

func TestFitWireJSONPrefersSmallerWindowWithNonEmptyText(t *testing.T) {
	files := make([]any, 20)
	for i := range files {
		files[i] = map[string]any{"path": "source.go", "diff": strings.Repeat("+observed hunk line\n", 50)}
	}
	raw, err := json.Marshal(map[string]any{"available": true, "files": files, "files_total": 20})
	testutil.FailErr(t, "marshal fixture", err)
	out, changed := FitWireJSON(string(raw), 2500)
	if !changed || len(out) > 2500 {
		t.Fatalf("projection failed: changed=%v len=%d", changed, len(out))
	}
	var got map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &got))
	retainedFiles, _ := got["files"].([]any)
	if len(retainedFiles) == 0 {
		t.Fatal("dropped all files")
	}
	firstFile, _ := retainedFiles[0].(map[string]any)
	diffStr, _ := firstFile["diff"].(string)
	if diffStr == "" {
		t.Fatal("first file diff was wiped to empty string when a smaller window with text fit")
	}
}

// deepTree is a snapshot-like tree whose every node has fanout children.
func deepTree(depth, fanout int) map[string]any {
	node := map[string]any{"role": "div", "name": "node"}
	if depth == 0 {
		return node
	}
	children := make([]any, fanout)
	for i := range children {
		children[i] = deepTree(depth-1, fanout)
	}
	node["children"] = children
	return node
}

func TestFitWireJSONGivesUpDeepTreesBeforeTopLevelResults(t *testing.T) {
	actions := make([]any, 3)
	for i := range actions {
		actions[i] = map[string]any{"ok": true, "effect": map[string]any{"dom_changes": i}}
	}
	raw, err := json.Marshal(map[string]any{
		"action_results": actions,
		"snapshot":       map[string]any{"tree": deepTree(6, 4)},
	})
	testutil.FailErr(t, "marshal fixture", err)
	out, changed := FitWireJSON(string(raw), 3000)
	if !changed || len(out) > 3000 {
		t.Fatalf("projection failed: changed=%v len=%d", changed, len(out))
	}
	var got map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &got))
	if kept, _ := got["action_results"].([]any); len(kept) != 3 {
		t.Fatalf("action results = %v; the tree must give way first", got["action_results"])
	}
}
