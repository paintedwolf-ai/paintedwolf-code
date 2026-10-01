package cadence

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
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

// newTestCoordinator binds scans to published source snapshots.
func newTestCoordinator(t *testing.T, store *scanbase.SQLStore, head scanbase.HeadSHAReader) *scanbase.CoordinatorImpl {
	t.Helper()
	return scanbase.NewCoordinator(store, head, testSnapshots(t, store))
}

// blockingCoordinator blocks publication until explicitly released.
type blockingCoordinator struct {
	*scanbase.CoordinatorImpl
	mu      sync.Mutex
	release chan struct{}
	waited  int
}

func (b *blockingCoordinator) PublishSourceGeneration(ctx context.Context, canonical string) (sourcesnapshot.Snapshot, string, error) {
	b.mu.Lock()
	b.waited++
	release := b.release
	b.mu.Unlock()
	select {
	case <-release:
		return b.CoordinatorImpl.PublishSourceGeneration(ctx, canonical)
	case <-ctx.Done():
		return sourcesnapshot.Snapshot{}, "", ctx.Err()
	}
}

func newBlockingCadence(t *testing.T, clock *testClock) (*Service, *scanbase.SQLStore, *blockingCoordinator) {
	t.Helper()
	cadence, store := newTestCadence(t, clock)
	coord := &blockingCoordinator{CoordinatorImpl: cadence.Coordinator.(*scanbase.CoordinatorImpl), release: make(chan struct{})}
	cadence.Coordinator = coord
	return cadence, store, coord
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

