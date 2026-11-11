package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The cost survey counts nothing under engine storage a root contains.
func TestSelectionSurveyLeavesEngineStorageUncounted(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	state := filepath.Join(home, ".config", "paintedwolf")
	for _, rel := range []string{"a.txt", "src/b.txt", ".config/paintedwolf/store.db", ".config/paintedwolf/worker-branches/x/c.txt"} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(home, filepath.Dir(rel)), 0o755))
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(home, rel), []byte("x"), 0o644))
	}
	count := func(hidden []string) int64 {
		roots, _, err := classifyRootClones(t.Context(), ChooseRequest{
			Grants: []RootGrant{{RootID: "home", SourcePath: home, IsPrimary: true}}, HiddenPaths: hidden,
		})
		testutil.FailErr(t, "classify", err)
		cost, err := countMaterializeCost(t.Context(), roots, RecordedBudget(BudgetClass{Platform: "linux", Filesystem: FilesystemCopyOnly}), false)
		testutil.FailErr(t, "count", err)
		return cost.Entries
	}
	// a.txt, src, src/b.txt and .config remain; the state folder and everything under it do not.
	if got := count([]string{state}); got != 4 {
		t.Fatalf("survey counted %d entries, want 4", got)
	}
	if got := count(nil); got != 9 {
		t.Fatalf("survey without hidden storage counted %d entries, want 9", got)
	}
}
