package execution

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
)

// testSnapshots shares the scan store database.
func testSnapshots(t *testing.T, store *scanbase.SQLStore) *sourcesnapshot.Store {
	t.Helper()
	dir := t.TempDir()
	snapshots := sourcesnapshot.New(
		store.DB(),
		sourceblob.New(filepath.Join(dir, "content")),
		filepath.Join(dir, "observations.db"),
		backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
			backgroundwork.ResourceMetadata: {Total: 1},
			backgroundwork.ResourceIO:       {Total: 1},
			backgroundwork.ResourceCPU:      {Total: 1},
		}),
	)
	t.Cleanup(func() { _ = snapshots.Close() })
	return snapshots
}

// publishTestSnapshot returns the manifest ID used to resolve scan input.
func publishTestSnapshot(t *testing.T, snapshots *sourcesnapshot.Store, projectDir string) string {
	t.Helper()
	snapshot, err := snapshots.EnsurePath(t.Context(), projectDir, sourcesnapshot.VerifyStat)
	testutil.FailErr(t, "publish source snapshot", err)
	return snapshot.ID
}
