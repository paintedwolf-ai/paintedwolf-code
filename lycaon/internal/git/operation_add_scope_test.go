package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestNamedAddPreparationVisitsDoNotGrowWithUnrelatedTree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		unrelated int
		tracked   bool
	}{{"empty", 0, false}, {"untracked", 128, false}, {"tracked-empty", 0, true}, {"tracked-full", 128, true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			var err error
			root, err = filepath.EvalSymlinks(root)
			testutil.FailErr(t, "resolve repository", err)
			gittest.Init(t, root)
			testutil.FailErr(t, "write selected file", os.WriteFile(filepath.Join(root, "README.txt"), []byte("selected\n"), 0o644))
			for i := range tc.unrelated {
				name := filepath.Join(root, fmt.Sprintf("unrelated-%03d.txt", i))
				testutil.FailErr(t, "write unrelated file", os.WriteFile(name, []byte("other\n"), 0o644))
			}
			if tc.tracked {
				gittest.Run(t, root, "add", ".")
				gittest.Run(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "baseline")
				testutil.FailErr(t, "modify selected file", os.WriteFile(filepath.Join(root, "README.txt"), []byte("changed\n"), 0o644))
			}
			repo, ok := gitrepo.Discover(root)
			if !ok {
				t.Fatal("repository unavailable")
			}
			visitedEntries := 0
			ctx := context.WithValue(t.Context(), operationVisitKey{}, func(count int) { visitedEntries += count })
			prepared, err := prepareOperation(ctx, repo, OperationRequest{Kind: "add", Paths: []string{"README.txt"}})
			testutil.FailErr(t, "prepare named add", err)
			defer prepared.close()
			if tc.tracked && visitedEntries != 5 || !tc.tracked && visitedEntries != 3 {
				t.Fatalf("named add visited %d preparation entries with %d unrelated files", visitedEntries, tc.unrelated)
			}
		})
	}
}
