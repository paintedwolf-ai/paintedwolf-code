package execution_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// newTestCoordinator uses published source snapshots.
func newTestCoordinator(t *testing.T, store *scan.SQLStore, head scan.HeadSHAReader) *scan.CoordinatorImpl {
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
	return scan.NewCoordinator(store, head, snapshots)
}
