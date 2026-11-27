package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCodeExecutorDraftsPreserveExactPOSIXPathIdentity(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir dir", os.MkdirAll(filepath.Join(dir, "dir"), 0o700))
	testutil.FailErr(t, "write note.txt", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("ordinary_note_disk_token\n"), 0o600))
	testutil.FailErr(t, "write trailing-space note", os.WriteFile(filepath.Join(dir, "note.txt "), []byte("trailing_space_disk_token\n"), 0o600))
	testutil.FailErr(t, "write dir/leaf.txt", os.WriteFile(filepath.Join(dir, "dir", "leaf.txt"), []byte("slash_leaf_disk_token\n"), 0o600))
	testutil.FailErr(t, "write literal-backslash file", os.WriteFile(filepath.Join(dir, `dir\leaf.txt`), []byte("backslash_disk_token\n"), 0o600))

	rootID := "root-posix-exact"
	projectID := "proj-posix-exact"
	testutil.FailErr(t, "settle index", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), projectID, sourcecatalog.Root{ID: rootID, Path: dir}))

	exec := NewCodeExecutorWithDrafts(decide.Reranker{}, func(_ context.Context, pid, rid string, _ int, _ int64) ([]CodeDraft, error) {
		if pid != projectID || rid != rootID {
			return nil, nil
		}
		return []CodeDraft{
			{Path: "note.txt ", Content: "trailing_space_dirty_token\n"},
			{Path: `dir\leaf.txt`, Content: "backslash_dirty_token\n"},
		}, nil
	})
	run := func(term string) ExecutorReport {
		t.Helper()
		rep, err := exec.Run(t.Context(), PlanLeg{
			Executor: ExecutorCode,
			Code: &CodePlanLeg{
				Query:     TextExpr{Text: term},
				Lines:     true,
				PathRoots: []CodeRoot{{ProjectID: projectID, RootID: rootID, Path: dir}},
				Cap:       50,
				Budget:    BudgetComplete,
			},
		})
		testutil.FailErr(t, "run code leg for "+term, err)
		return rep
	}

	// Ordinary files MUST keep their own disk hits (zero collision with trailing-space or backslash drafts).
	if r := run("ordinary_note_disk_token"); len(r.Hits) != 1 || r.Hits[0].Path != "note.txt" {
		t.Fatalf("ordinary note.txt collided with trailing-space draft: %+v", r.Hits)
	}
	if r := run("slash_leaf_disk_token"); len(r.Hits) != 1 || r.Hits[0].Path != "dir/leaf.txt" {
		t.Fatalf("dir/leaf.txt collided with literal-backslash draft: %+v", r.Hits)
	}
	// Exact POSIX draft paths match under their exact unrewritten identities.
	if r := run("trailing_space_dirty_token"); len(r.Hits) != 1 || r.Hits[0].Path != "note.txt " {
		t.Fatalf("trailing-space draft did not preserve exact path: %+v", r.Hits)
	}
	if r := run("backslash_dirty_token"); len(r.Hits) != 1 || r.Hits[0].Path != `dir\leaf.txt` {
		t.Fatalf("literal-backslash draft did not preserve exact path: %+v", r.Hits)
	}
}
