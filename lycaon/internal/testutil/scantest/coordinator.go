// Package scantest builds coordinators backed by source publications.
package scantest

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// Snapshots shares the scan database so publications and scans reference one store.
func Snapshots(t *testing.T, store *scan.SQLStore) *sourcesnapshot.Store {
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

// Coordinator builds a coordinator whose scans are pinned to real publications.
func Coordinator(t *testing.T, store *scan.SQLStore, head scan.HeadSHAReader) *scan.CoordinatorImpl {
	t.Helper()
	return scan.NewCoordinator(store, head, Snapshots(t, store))
}
