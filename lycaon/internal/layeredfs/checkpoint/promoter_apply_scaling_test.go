package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func publishManyChanges(t *testing.T, primary string, count int) (*Store, *Promoter, *PromotionPreview) {
	t.Helper()
	core, err := layeredfs.New(layeredfs.Config{ViewID: "scale-view", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	for i := range count {
		_, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: fmt.Sprintf("f%02d.txt", i)}, []byte("worker\n"), 0o640)
		testutil.FailErr(t, "worker write", err)
	}
	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "new store", err)
	publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w1", TaskID: "t1", ProjectID: "p1"})
	testutil.FailErr(t, "publisher", err)
	_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "cp-scale", Trigger: TriggerWorkerCompletion})
	testutil.FailErr(t, "publish", err)
	promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}})
	testutil.FailErr(t, "promoter", err)
	preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"})
	testutil.FailErr(t, "preview", err)
	return store, promoter, preview
}

// Rewriting the undo record per path costs O(N^2) bytes across a promotion.
func TestPromotionJournalIsNotRewrittenPerAppliedPath(t *testing.T) {
	const count = 12
	primary := t.TempDir()
	store, promoter, preview := publishManyChanges(t, primary, count)
	dir := filepath.Join(t.TempDir(), "tx")
	var identities []string
	txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "scale", Reached: func(step TransactionStep, _ string) error {
		if step == StepTargetMutated {
			identity, err := fspath.EntryIdentity(filepath.Join(dir, transactionJournalName))
			testutil.FailErr(t, "identify journal", err)
			identities = append(identities, identity)
		}
		return nil
	}})
	testutil.FailErr(t, "begin", err)
	_, err = txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: preview})
	testutil.FailErr(t, "apply", err)
	if len(identities) != count {
		t.Fatalf("observed %d applied paths, want %d", len(identities), count)
	}
	for _, identity := range identities {
		if identity != identities[0] {
			t.Fatal("promotion journal was republished while applying paths")
		}
	}
	rec, found, err := store.LoadPromotionRecord("tx-cp-scale")
	testutil.FailErr(t, "load record", err)
	if !found || rec.State != PromotionStateApplied || len(rec.AppliedPaths) != count {
		t.Fatalf("record found=%v state=%s applied=%d", found, rec.State, len(rec.AppliedPaths))
	}
}

// A failure at any applied path restores every path, and nothing resumes it.
func TestPromotionFaultAtEachStepRestoresEveryPath(t *testing.T) {
	const count = 4
	for failAt := 1; failAt <= count; failAt++ {
		for _, crash := range []bool{false, true} {
			t.Run(fmt.Sprintf("path %d crash %v", failAt, crash), func(t *testing.T) {
				primary := t.TempDir()
				store, promoter, preview := publishManyChanges(t, primary, count)
				dir := filepath.Join(t.TempDir(), "tx")
				failure := errors.New("interrupted")
				applied := 0
				txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "fault"})
				testutil.FailErr(t, "begin", err)
				request := ApplyRequest{Preview: preview, FaultInjector: func(stage ApplyFaultStage) error {
					if stage == ApplyFaultMidFileApply {
						if applied++; applied == failAt {
							if crash {
								panic(errSimulatedCrash)
							}
							return failure
						}
					}
					return nil
				}}
				if crash {
					crashDuring(t, func() { _, _ = txn.ApplyCheckpoint(t.Context(), promoter, request) })
					found, err := RecoverTransaction(t.Context(), TransactionConfig{Dir: dir})
					testutil.FailErr(t, "recover", err)
					if !found {
						t.Fatal("crash left no record to recover")
					}
				} else if _, err := txn.ApplyCheckpoint(t.Context(), promoter, request); !errors.Is(err, failure) {
					t.Fatalf("apply = %v, want interruption", err)
				}
				for i := range count {
					// Every path was added by the promotion, so the original target has none of them.
					if _, err := os.Lstat(filepath.Join(primary, fmt.Sprintf("f%02d.txt", i))); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("f%02d survived rollback: %v", i, err)
					}
				}
				if _, found, err := store.LoadPromotionRecord("tx-cp-scale"); err != nil || found {
					t.Fatalf("record survived rollback: found=%v err=%v", found, err)
				}
				if _, err := os.Lstat(filepath.Join(dir, transactionJournalName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("journal survived rollback: %v", err)
				}
			})
		}
	}
}
