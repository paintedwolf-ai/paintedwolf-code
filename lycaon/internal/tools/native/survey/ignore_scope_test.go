package survey

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func writeIgnoredTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755))
	testutil.FailErr(t, "write .gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\nsecret.env\n"), 0o644))
	testutil.FailErr(t, "write dep", os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write ignored", os.WriteFile(filepath.Join(dir, "secret.env"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write src", os.WriteFile(filepath.Join(dir, "main.go"), []byte("// needle\n"), 0o644))
	return dir
}

func TestGrepPrunesIgnoredEntriesOnlyWhenAsked(t *testing.T) {
	dir := writeIgnoredTree(t)
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	run := func(args map[string]any) map[string]bool {
		out, err := tool.Run(context.Background(), args, nativefixture.Context(dir))
		testutil.FailErr(t, "grep", err)
		paths := map[string]bool{}
		for _, m := range nativefixture.GrepMatches(t, out) {
			paths[m["path"].(string)] = true
		}
		return paths
	}
	if paths := run(map[string]any{"pattern": "needle"}); !paths["secret.env"] || !paths["node_modules/pkg/index.js"] {
		t.Fatalf("default search must reach ignored files, got %v", paths)
	}
	pruned := run(map[string]any{"pattern": "needle", "include_ignored": false})
	if pruned["secret.env"] || pruned["node_modules/pkg/index.js"] || !pruned["main.go"] {
		t.Fatalf("include_ignored=false kept ignored files: %v", pruned)
	}
	inside := run(map[string]any{"pattern": "needle", "include_ignored": false, "path": "node_modules"})
	if !inside["node_modules/pkg/index.js"] {
		t.Fatalf("a named ignored root must still be searched: %v", inside)
	}
}

func TestFindPrunesIgnoredEntriesOnlyWhenAsked(t *testing.T) {
	dir := writeIgnoredTree(t)
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	run := func(args map[string]any) map[string]bool {
		out, err := tool.Run(context.Background(), args, nativefixture.Context(dir))
		testutil.FailErr(t, "find", err)
		paths := map[string]bool{}
		for _, r := range surveyFindResults(t, out) {
			paths[r["path"].(string)] = true
		}
		return paths
	}
	if paths := run(map[string]any{"name_glob": "**/*", "type": "file"}); !paths["secret.env"] {
		t.Fatalf("default walk must list ignored files, got %v", paths)
	}
	pruned := run(map[string]any{"name_glob": "**/*", "type": "file", "include_ignored": false})
	if pruned["secret.env"] || pruned["node_modules/pkg/index.js"] || !pruned["main.go"] {
		t.Fatalf("include_ignored=false kept ignored entries: %v", pruned)
	}
}

func TestGrepDeadlineNamesTheLargestSubtree(t *testing.T) {
	search := &grepSearch{}
	for _, rel := range []string{"vendor/a.go", "vendor/b.go", "src/c.go", "top.go"} {
		search.noteSubtree(rel)
	}
	reject := tools.AsToolReject(grepExecutionError(search, context.DeadlineExceeded))
	if reject == nil || reject.Data["largest_subtree"] != "vendor (2 files)" {
		t.Fatalf("largest subtree = %v", reject.Data["largest_subtree"])
	}
}
