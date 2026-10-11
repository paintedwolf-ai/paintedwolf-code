package sourcecontracts

import (
	"os"
	"path/filepath"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeletingAProjectReleasesItsSourceSnapshots(t *testing.T) {
	srv := contractfixture.NewSnapshotTestServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "write source",
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644))

	p := contractfixture.SeedProjectWithSnapshot(t, srv, root)
	if contractfixture.SnapshotCount(t, srv) == 0 {
		t.Fatal("no snapshot to release")
	}
	contractfixture.DeleteProject(t, srv, p.ID)

	if got := contractfixture.SnapshotCount(t, srv); got != 0 {
		t.Fatalf("snapshots after project delete = %d want 0", got)
	}
}

// Two projects over one tree share its snapshots. Deleting one must not take
// the other's history with it.

func TestDeletingOneProjectKeepsASharedRootsSnapshots(t *testing.T) {
	srv := contractfixture.NewSnapshotTestServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "write source",
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644))

	first := contractfixture.SeedProjectWithSnapshot(t, srv, root)
	second := contractfixture.CreateProjectForTest(t, srv, root)
	if second.ID == first.ID {
		t.Fatal("expected a second project over the same root")
	}

	contractfixture.DeleteProject(t, srv, first.ID)

	if got := contractfixture.SnapshotCount(t, srv); got == 0 {
		t.Fatal("shared root lost its snapshots when one of its projects was deleted")
	}
}
