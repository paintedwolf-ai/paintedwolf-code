package sourcesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSnapshotSkipsFloorAndIgnoredPathsWithoutGit(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	testutil.FailErr(t, "gitignore", os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n.bin/\n"), 0o644))
	writeSource(t, root, "src/main.go", "package main\n")
	writeSource(t, root, "go.mod", "module example\n")
	writeSource(t, root, "pkg/target/debug/out", "bin")
	writeSource(t, root, "node_modules/x/index.js", "1")
	writeSource(t, root, "ignored.txt", "secret")
	writeSource(t, root, ".bin/tool", "x")

	snapshot, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish snapshot", err)
	paths := entryPaths(t, store, snapshot)
	for _, want := range []string{"src/main.go", "go.mod", ".gitignore"} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("missing %s in %+v", want, paths)
		}
	}
	for _, blocked := range []string{"pkg/target/debug/out", "node_modules/x/index.js", "ignored.txt", ".bin/tool"} {
		if _, ok := paths[blocked]; ok {
			t.Fatalf("snapshot captured excluded path %s", blocked)
		}
	}
	if snapshot.AdmissionMode != AdmissionScope {
		t.Fatalf("admission = %q, want %q: nothing was left out by cost", snapshot.AdmissionMode, AdmissionScope)
	}
	reasons := map[string]string{}
	for _, b := range snapshot.Boundaries {
		reasons[b.Path] = b.Detail
	}
	if reasons["node_modules"] != sourcescope.ReasonFloor || reasons["pkg/target"] != sourcescope.ReasonFloor || reasons[".bin"] != sourcescope.ReasonIgnored {
		t.Fatalf("boundaries = %+v", snapshot.Boundaries)
	}
	if len(snapshot.Unobserved()) != 0 {
		t.Fatalf("policy boundaries are not unobserved tree: %+v", snapshot.Unobserved())
	}
	held, err := store.HasPath(t.Context(), snapshot.ID, root, "node_modules")
	testutil.FailErr(t, "check excluded directory", err)
	if held {
		t.Fatal("manifest holds a path under node_modules")
	}
	loaded, err := store.Get(t.Context(), snapshot.ID)
	testutil.FailErr(t, "reload snapshot", err)
	if len(loaded.Boundaries) != len(snapshot.Boundaries) {
		t.Fatalf("boundaries did not persist: %+v vs %+v", loaded.Boundaries, snapshot.Boundaries)
	}
}

func TestSnapshotRecordsBudgetBoundariesAsUnobserved(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "src/main.go", "package main\n")
	for i := range 8 {
		writeSource(t, root, filepath.Join("data", "f"+string(rune('a'+i))), "x")
	}
	store.SetScopes(scopeProvider{plane: sourcescope.Plane{IgnoreFiles: true, Budgets: sandbox.SurveyBudgets{
		DirectoryEntries: 4, SubtreeEntries: 100, WalkEntries: 1000,
	}}})

	snapshot, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish snapshot", err)
	if snapshot.AdmissionMode != AdmissionScopeBounded {
		t.Fatalf("admission = %q, want %q", snapshot.AdmissionMode, AdmissionScopeBounded)
	}
	unobserved := snapshot.Unobserved()
	if len(unobserved) != 1 || unobserved[0].Path != "data" || unobserved[0].Reason != string(sandbox.BoundaryDirectoryCap) || unobserved[0].Entries != 5 {
		t.Fatalf("unobserved = %+v", unobserved)
	}
	if snapshot.FileCount != 1 {
		t.Fatalf("file count = %d, want 1 (src/main.go); the bounded directory was never entered", snapshot.FileCount)
	}
}

// scopeProvider builds a scope from one plane for tests.
type scopeProvider struct{ plane sourcescope.Plane }

func (p scopeProvider) Capture(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: p.plane})
}

func TestDeltaKeepsAGenerationBoundedWhileTheBoundaryIsCarried(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "src/main.go", "package main\n")
	for i := range 8 {
		writeSource(t, root, filepath.Join("data", "f"+string(rune('a'+i))), "x")
	}
	store.SetScopes(scopeProvider{plane: sourcescope.Plane{IgnoreFiles: true, Budgets: sandbox.SurveyBudgets{
		DirectoryEntries: 4, SubtreeEntries: 100, WalkEntries: 1000,
	}}})
	request := Request{Roots: []Root{{Path: root}}}
	first, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish first generation", err)
	if first.AdmissionMode != AdmissionScopeBounded {
		t.Fatalf("first admission = %q", first.AdmissionMode)
	}

	surveys := surveyCount(t, store)
	writeSource(t, root, "src/other.go", "package main\n")
	notifyChange(root, "src/other.go")
	second, err := store.Ensure(t.Context(), request)
	testutil.FailErr(t, "publish second generation", err)
	if got := surveys(); got != 0 {
		t.Fatalf("root surveys = %d, want 0: the generation came from the delta", got)
	}
	if second.ID == first.ID || second.FileCount != 2 {
		t.Fatalf("second generation id=%s files=%d", second.ID, second.FileCount)
	}
	if second.AdmissionMode != AdmissionScopeBounded {
		t.Fatalf("delta admission = %q, want %q: data is still unobserved", second.AdmissionMode, AdmissionScopeBounded)
	}
	if unobserved := second.Unobserved(); len(unobserved) != 1 || unobserved[0].Path != "data" {
		t.Fatalf("unobserved after delta = %+v", unobserved)
	}
}
