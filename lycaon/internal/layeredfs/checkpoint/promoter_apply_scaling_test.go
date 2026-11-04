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

// Rewriting the whole record per path costs O(N^2) bytes across a promotion.
func TestPromotionRecordIsNotRewrittenPerAppliedPath(t *testing.T) {
	const count = 12
	primary := t.TempDir()
	store, promoter, preview := publishManyChanges(t, primary, count)
	recordPath := filepath.Join(store.promotionsDir, "tx-cp-scale.json")
	var identities []string
	_, err := promoter.Apply(t.Context(), ApplyRequest{Preview: preview, FaultInjector: func(stage ApplyFaultStage) error {
		if stage == ApplyFaultMidFileApply {
			identity, err := fspath.EntryIdentity(recordPath)
			testutil.FailErr(t, "identify record", err)
			identities = append(identities, identity)
		}
		return nil
	}})
	testutil.FailErr(t, "apply", err)
	if len(identities) != count {
		t.Fatalf("observed %d applied paths, want %d", len(identities), count)
	}
	for _, identity := range identities {
		if identity != identities[0] {
			t.Fatal("promotion record was republished while applying paths")
		}
	}
	rec, found, err := store.LoadPromotionRecord("tx-cp-scale")
	testutil.FailErr(t, "load record", err)
	if !found || rec.State != PromotionStateApplied || len(rec.AppliedPaths) != count {
		t.Fatalf("record found=%v state=%s applied=%d", found, rec.State, len(rec.AppliedPaths))
	}
}

func TestPromotionResumeHonorsPathsAppliedBeforeInterruption(t *testing.T) {
	const count = 6
	primary := t.TempDir()
	store, promoter, preview := publishManyChanges(t, primary, count)
	failure := errors.New("interrupted")
	applied := 0
	_, err := promoter.Apply(t.Context(), ApplyRequest{Preview: preview, FaultInjector: func(stage ApplyFaultStage) error {
		if stage == ApplyFaultMidFileApply {
			if applied++; applied == 2 {
				return failure
			}
		}
		return nil
	}})
	if !errors.Is(err, failure) {
		t.Fatalf("apply = %v, want interruption", err)
	}
	rec, found, err := store.LoadPromotionRecord("tx-cp-scale")
	testutil.FailErr(t, "load interrupted record", err)
	if !found || len(rec.AppliedPaths) != 2 {
		t.Fatalf("interrupted record found=%v applied=%v", found, rec.AppliedPaths)
	}
	// An applied path differs from its preview observation; only the durable fact excuses it.
	reopened, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}})
	testutil.FailErr(t, "reopen promoter", err)
	resumed := 0
	_, err = reopened.Apply(t.Context(), ApplyRequest{Preview: preview, FaultInjector: func(stage ApplyFaultStage) error {
		if stage == ApplyFaultMidFileApply {
			resumed++
		}
		return nil
	}})
	testutil.FailErr(t, "resume", err)
	if resumed != count-2 {
		t.Fatalf("resume applied %d paths, want %d", resumed, count-2)
	}
	for i := range count {
		raw, err := os.ReadFile(filepath.Join(primary, fmt.Sprintf("f%02d.txt", i)))
		testutil.FailErr(t, "read promoted", err)
		if string(raw) != "worker\n" {
			t.Fatalf("f%02d = %q", i, raw)
		}
	}
	rec, _, err = store.LoadPromotionRecord("tx-cp-scale")
	testutil.FailErr(t, "load completed record", err)
	if rec.State != PromotionStateApplied || len(rec.AppliedPaths) != count {
		t.Fatalf("completed record state=%s applied=%d", rec.State, len(rec.AppliedPaths))
	}
	if entries, err := os.ReadDir(filepath.Join(store.promotionsDir, "tx-cp-scale.applied")); err == nil && len(entries) != 0 {
		t.Fatalf("progress markers survived completion: %v", entries)
	}
}
