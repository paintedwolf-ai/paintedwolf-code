package sourceledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandObservationDoesNotWaitIndefinitelyForOtherProjects(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"verification", "command window"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			ledger := openDiskLedger(t)
			// An empty root never needs the content I/O lane.
			writeRootFile(t, ledger.root, "source.txt", "source to observe\n")
			broker := backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
				backgroundwork.ResourceIO: {Total: 1},
			})
			// An unrelated project's inventory holds the shared I/O resource.
			release, err := broker.Acquire(t.Context(), backgroundwork.Request{
				Key: "other-project", Resources: []backgroundwork.Resource{backgroundwork.ResourceIO},
			})
			testutil.FailErr(t, "occupy snapshot I/O", err)
			defer release()
			testutil.FailErr(t, "close original snapshot store", ledger.store.snapshots.Close())
			ledger.store.snapshots = sourcesnapshot.New(ledger.sqlDB, ledger.store.objects,
				filepath.Join(t.TempDir(), "observations.db"), broker)
			t.Cleanup(func() {
				testutil.FailErr(t, "close snapshot store", ledger.store.snapshots.Close())
			})
			// The proof is that the budget bounds the wait, whatever its length.
			ledger.store.observationBudget = testutil.Timeout(500 * time.Millisecond)
			budget := ledger.store.observationBudget
			ctx, cancel := context.WithTimeout(t.Context(), 3*budget)
			defer cancel()
			started := time.Now()
			if operation == "verification" {
				revision, root := VerificationState(ctx, ledger.store, ledger.root)
				if revision != "" || root != "" {
					t.Fatal("unobserved source produced verification evidence")
				}
			} else {
				window, err := ledger.store.OpenCommandWindow(ctx, CommandWindowInput{
					ProjectID: "p1", Roots: onDiskRoots(ledger.root), ToolName: "command",
				})
				if window != nil || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("blocked observation = %v, %v; want no window and deadline", window, err)
				}
			}
			if ctx.Err() != nil || time.Since(started) >= 2*budget {
				t.Fatal("optional observation consumed the caller's execution lifetime")
			}
			// Timing out one observation must leave both the caller and store usable.
			release()
			revision, root := VerificationState(ctx, ledger.store, ledger.root)
			if revision == "" || root == "" {
				t.Fatal("source observation did not recover after capacity became available")
			}
		})
	}
}
