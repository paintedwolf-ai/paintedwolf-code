package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/testutil"
)

type reconciledProgramProgress struct {
	store progress.Store
	owner *progressclosure.Service
}

func (p reconciledProgramProgress) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id), Reconciliation: p.owner.Reconciliation(id)}, nil
}

func TestComposedMutationRequiresAcknowledgementOfKnownWorkerFinishes(t *testing.T) {
	fixture := newComposedProgramFixture(t)
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed valid pending plan", store.Set(fixture.Session, "- [ ] Deliver\n- [ ] Verify"))
	owner := progressclosure.New()
	owner.SetProgress(store)
	owner.Arm(t.Context(), fixture.Session, "worker-one")
	fixture.Runtime.Progress = reconciledProgramProgress{store, owner}
	outcome := callComposedProgram(t, fixture, "printf 'payload\\n' > result.txt")
	if outcome.Refusal == nil || outcome.Refusal.Code != "PROGRESS_RECONCILIATION_REQUIRED" {
		t.Fatalf("pending finish admitted mutation: %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked program published a file: %v", err)
	}
	readonly := callComposedProgram(t, fixture, "printf 'inspection\\n'")
	if readonly.Status != 0 || readonly.Stdout != "inspection\n" {
		t.Fatalf("finish obligation blocked read-only work: %+v", readonly)
	}
	snapshot, err := store.Snapshot(t.Context(), fixture.Session)
	testutil.FailErr(t, "observe current checklist", err)
	update, err := progress.PrepareAuthorUpdate(snapshot.Content)
	testutil.FailErr(t, "prepare current checklist", err)
	result, err := update.ExpectRevision(snapshot.Revision).ExpectReconciliation(owner.Reconciliation(fixture.Session).Token).Apply(t.Context(), store, fixture.Session, owner)
	testutil.FailErr(t, "acknowledge reviewed finish", err)
	if result.ReconciliationStatus != "acknowledged" || result.Updated {
		t.Fatalf("acknowledgement rewrote pending work: %+v", result)
	}
	outcome = callComposedProgram(t, fixture, "printf 'payload\\n' > result.txt")
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("acknowledged mutation refused: %+v", outcome)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.Dir, "result.txt"))
	testutil.FailErr(t, "read committed native write", err)
	if string(contents) != "payload\n" {
		t.Fatalf("native write contents: %q", contents)
	}
}
