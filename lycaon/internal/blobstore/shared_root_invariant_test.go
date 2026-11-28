package blobstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestSharedDataRootConcurrentStoresInvariant proves GB10 I7 (first case
// GB03-F1 / #2924; see also TestConcurrentCrossProcessWritesShareStagingDir
// in staging_race_test.go for the cross-process subprocess case per Decision 101):
// two independently constructed stores on one shared data root interleaving
// Put, PutAt, Open, and Maintain never fail a commit, never lose a readable
// blob, and never remove the shared .staging directory.
func TestSharedDataRootConcurrentStoresInvariant(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	storeA := Store{Root: root, Dir: "model-content", StagedTTL: time.Hour}
	storeB := Store{Root: root, Dir: "model-content", StagedTTL: time.Hour}
	ctx := context.Background()

	// Seed a baseline blob and verify .staging survives commits without being
	// removed out from under peer processes.
	seedPayload := []byte("seed-blob-for-shared-root-invariant")
	seedBlob, err := storeA.Put("seed.txt", bytes.NewReader(seedPayload), bytebound.Materialization(len(seedPayload)+64))
	testutil.FailErr(t, "seed Put", err)
	stagePath := filepath.Join(storeA.dataRoot(), stagingDir)
	if _, statErr := os.Stat(stagePath); os.IsNotExist(statErr) {
		t.Fatalf("shared %s directory was removed after commit; concurrent peer processes will race on CreateTemp", stagingDir)
	}

	const workers = 4
	const opsPerWorker = 25

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		committed []struct {
			blob    Blob
			payload []byte
		}
		errs []error
	)

	recordErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	for w := range workers {
		s := storeA
		if w%2 == 1 {
			s = storeB
		}
		wg.Add(1)
		go func(workerID int, st Store) {
			defer wg.Done()
			for i := range opsPerWorker {
				payload := []byte(fmt.Sprintf("worker-%d-op-%d-shared-root-payload", workerID, i))
				limit := bytebound.Materialization(len(payload) + 64)
				if i%3 == 0 {
					rel := fmt.Sprintf("model-content/w%d/item_%d.bin", workerID, i)
					b, err := st.PutAt(ctx, rel, bytes.NewReader(payload), limit)
					if err != nil {
						recordErr(fmt.Errorf("worker %d op %d PutAt: %w", workerID, i, err))
						continue
					}
					mu.Lock()
					committed = append(committed, struct {
						blob    Blob
						payload []byte
					}{blob: b, payload: payload})
					mu.Unlock()
				} else {
					b, err := st.Put(fmt.Sprintf("f_%d_%d.txt", workerID, i), bytes.NewReader(payload), limit)
					if err != nil {
						recordErr(fmt.Errorf("worker %d op %d Put: %w", workerID, i, err))
						continue
					}
					mu.Lock()
					committed = append(committed, struct {
						blob    Blob
						payload []byte
					}{blob: b, payload: payload})
					mu.Unlock()
				}
				if i%5 == 0 {
					recordErr(st.Maintain())
				}
			}
		}(w, s)
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("concurrent shared-root commits failed (%d errors); first: %v", len(errs), errs[0])
	}

	// Verify seed and every committed blob is readable from the peer store with exact bytes.
	checkReadable := func(st Store, b Blob, want []byte) {
		t.Helper()
		rc, err := st.Open(b)
		testutil.FailErr(t, "Open "+b.Rel, err)
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		testutil.FailErr(t, "ReadAll "+b.Rel, err)
		if !bytes.Equal(got, want) {
			t.Fatalf("blob %s bytes mismatch: got %q, want %q", b.Rel, got, want)
		}
	}
	checkReadable(storeB, seedBlob, seedPayload)
	for _, item := range committed {
		checkReadable(storeA, item.blob, item.payload)
		checkReadable(storeB, item.blob, item.payload)
	}
}
