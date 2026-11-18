package adapter

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func releaseFixture(t *testing.T) (*EagerCopyAdapter, *PreparedView, *eagerViewState) {
	t.Helper()
	source := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, "keep.txt"), []byte("original"), 0o600))
	gittest.InitCommit(t, source, "initial")
	adp := NewEagerCopyAdapter()
	view, err := adp.Prepare(t.Context(), PrepareRequest{ViewID: t.Name(), RuntimeDir: t.TempDir(), Grants: []RootGrant{{RootID: "root", SourcePath: source, IsPrimary: true}}})
	testutil.FailErr(t, "prepare materialized view", err)
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), view.ViewID) })
	state, ok := adp.lookupViewState(view.ViewID)
	if !ok || state.gitStore == nil || len(state.gitLeases) == 0 {
		t.Fatal("prepared view has no retained Git lease")
	}
	return adp, view, state
}

func TestMaterializedReleaseCancellationRetainsRetryAcrossAdapters(t *testing.T) {
	adp, view, state := releaseFixture(t)
	leases := state.gitStore.ActiveLeases()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	receipt, err := adp.Release(ctx, view.ViewID)
	if err == nil || receipt.Settled || receipt.ReasonCode != ReasonCleanupFailed {
		t.Fatalf("canceled release = %+v, %v", receipt, err)
	}
	if got := state.gitStore.ActiveLeases(); got != leases {
		t.Fatalf("canceled release dropped Git leases: got %d want %d", got, leases)
	}
	if _, err := os.Stat(view.RuntimeViewDir); err != nil {
		t.Fatalf("canceled release removed recovery tree: %v", err)
	}
	replacement := NewEagerCopyAdapter()
	if got, ok := replacement.LoadedView(view.ViewID); !ok || got != view {
		t.Fatal("replacement adapter cannot retry retained view")
	}
	if _, err := adp.CommandAccess(view.ViewID); err == nil {
		t.Fatal("retiring view admitted new command access")
	}
	receipt, err = replacement.Release(t.Context(), view.ViewID)
	testutil.FailErr(t, "retry release", err)
	if !receipt.Settled {
		t.Fatalf("retry did not settle: %+v", receipt)
	}
	if _, ok := adp.LoadedView(view.ViewID); ok {
		t.Fatal("original adapter retained a stale released view")
	}
	if _, err := os.Stat(view.RuntimeViewDir); !os.IsNotExist(err) {
		t.Fatalf("settled runtime tree remains: %v", err)
	}
	if got := state.gitStore.ActiveLeases(); got != 0 {
		t.Fatalf("settled view retained %d Git leases", got)
	}
}

func TestMaterializedReleasePreservesRecoveryOnJournalPublicationFailure(t *testing.T) {
	adp, view, state := releaseFixture(t)
	working := filepath.Join(view.PrimaryWorkPath, "keep.txt")
	testutil.FailErr(t, "change working file", os.WriteFile(working, []byte("retained change"), 0o600))
	_, release, err := adp.CheckpointBarrier(t.Context(), view.ViewID)
	testutil.FailErr(t, "capture working state", err)
	_, err = view.Core.SnapshotJournal()
	testutil.FailErr(t, "persist initial journal", err)
	testutil.FailErr(t, "end capture barrier", release())
	journal := filepath.Join(view.BackingDir, "journal", "journal.json")
	restore := obstructReleaseFile(t, journal)
	leases := state.gitStore.ActiveLeases()
	receipt, err := adp.Release(t.Context(), view.ViewID)
	if err == nil || receipt.Settled || receipt.ReasonCode != ReasonCleanupFailed {
		t.Fatalf("failed journal publication settled release: %+v, %v", receipt, err)
	}
	raw, err := os.ReadFile(working)
	testutil.FailErr(t, "read retained working state", err)
	if string(raw) != "retained change" {
		t.Fatalf("working state = %q", raw)
	}
	if got := state.gitStore.ActiveLeases(); got != leases {
		t.Fatalf("failed durability dropped Git leases: got %d want %d", got, leases)
	}
	if _, ok := adp.LoadedView(view.ViewID); !ok {
		t.Fatal("failed durability discarded retry state")
	}
	restore()
	receipt, err = adp.Release(t.Context(), view.ViewID)
	testutil.FailErr(t, "retry journal publication and cleanup", err)
	if !receipt.Settled {
		t.Fatalf("retry did not settle: %+v", receipt)
	}
}

func TestMaterializedReleaseRetainsFailedGitLeaseCleanupForRetry(t *testing.T) {
	adp, view, state := releaseFixture(t)
	leaseFile := filepath.Join(state.gitStore.RootDir(), "leases", state.gitLeases[0]+".json")
	restore := obstructReleaseFile(t, leaseFile)
	receipt, err := adp.Release(t.Context(), view.ViewID)
	if err == nil || receipt.Settled || receipt.ReasonCode != ReasonCleanupFailed {
		t.Fatalf("failed lease cleanup settled release: %+v, %v", receipt, err)
	}
	if _, ok := NewEagerCopyAdapter().LoadedView(view.ViewID); !ok {
		t.Fatal("failed lease cleanup discarded retry state")
	}
	restore()
	receipt, err = adp.Release(t.Context(), view.ViewID)
	testutil.FailErr(t, "retry Git lease cleanup", err)
	if !receipt.Settled || state.gitStore.ActiveLeases() != 0 {
		t.Fatalf("lease cleanup retry = %+v, leases=%d", receipt, state.gitStore.ActiveLeases())
	}
}

func TestMaterializedReleaseSerializesConcurrentAdapterRetries(t *testing.T) {
	adp, view, _ := releaseFixture(t)
	token, err := view.Core.BeginCheckpointBarrier(t.Context())
	testutil.FailErr(t, "hold Core barrier", err)
	end := sync.OnceFunc(func() { testutil.FailErr(t, "end Core barrier", view.Core.EndCheckpointBarrier(token)) })
	t.Cleanup(end)
	type result struct {
		receipt ReleaseReceipt
		err     error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { r, e := adp.Release(t.Context(), view.ViewID); first <- result{r, e} }()
	testutil.WaitFor(t, "release closes command admission", func() bool { _, err := adp.CommandAccess(view.ViewID); return err != nil })
	go func() { r, e := NewEagerCopyAdapter().Release(t.Context(), view.ViewID); second <- result{r, e} }()
	end()
	for _, ch := range []chan result{first, second} {
		got := testutil.Receive(t, "concurrent release", ch)
		testutil.FailErr(t, "concurrent release", got.err)
		if !got.receipt.Settled {
			t.Fatalf("concurrent release did not settle: %+v", got.receipt)
		}
	}
}

// A nonempty directory makes replacement or removal fail without permission assumptions.
func obstructReleaseFile(t *testing.T, filename string) func() {
	t.Helper()
	saved := filename + ".retained"
	testutil.FailErr(t, "retain durable record", os.Rename(filename, saved))
	testutil.FailErr(t, "obstruct record publication", os.Mkdir(filename, 0o700))
	testutil.FailErr(t, "keep obstruction nonempty", os.WriteFile(filepath.Join(filename, "block"), []byte("blocked"), 0o600))
	restore := sync.OnceFunc(func() {
		testutil.FailErr(t, "remove record obstruction", os.RemoveAll(filename))
		testutil.FailErr(t, "restore durable record", os.Rename(saved, filename))
	})
	t.Cleanup(restore)
	return restore
}
