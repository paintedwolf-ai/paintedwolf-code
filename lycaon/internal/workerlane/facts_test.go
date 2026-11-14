package workerlane_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeState struct {
	unsettled map[string]bool
	recovery  bool
	holders   bool
}

func (s fakeState) PromotionUnsettled(_ context.Context, jobID string) (bool, error) {
	return s.unsettled[jobID], nil
}

func (s fakeState) Readiness(context.Context, string) (bool, bool, error) {
	return s.recovery, s.holders, nil
}

func (f *landingFixture) facts(t *testing.T, state fakeState, a workerlane.Assignment) workerlane.Facts {
	t.Helper()
	facts, err := f.lanes.FactsFor(t.Context(), state, fakeCheckpoints{"cp-job-1": true, "cp-job-2": true}, f.actor, f.workerID, a)
	testutil.FailErr(t, "facts", err)
	return facts
}

func TestFactsFollowTheLastResult(t *testing.T) {
	f := newLandingFixture(t)
	write := workerlane.Assignment{Write: true}
	if got := workerlane.Select(f.facts(t, fakeState{}, write)); got.Action != workerlane.ActionKeep {
		t.Fatalf("after a delivered result = %+v", got)
	}
	if got := f.facts(t, fakeState{recovery: true}, write); !got.RecoveryRequired {
		t.Fatalf("recovery fact lost: %+v", got)
	}
	if got := f.facts(t, fakeState{unsettled: map[string]bool{"job-1": true}}, write); !got.PromotionUnsettled {
		t.Fatalf("pending landing lost: %+v", got)
	}

	// A second result is rejected without its reset running: the next round
	// resets to the first result's checkpoint.
	f.round(t, "job-2", "result-2", true)
	_, err := f.records.SetChange(t.Context(), f.actor, workerrecords.Op{ID: "reject-2"}, "result-2", workerrecords.Change{State: workerrecords.ChangeRejected})
	testutil.FailErr(t, "reject", err)
	got := f.facts(t, fakeState{}, write)
	if got.LastChange != workerlane.ChangeRejected || got.PriorCheckpoint != "cp-job-1" {
		t.Fatalf("after a rejection = %+v", got)
	}
	if sel := workerlane.Select(got); sel.Action != workerlane.ActionReset || sel.FromCheckpoint != "cp-job-1" {
		t.Fatalf("selection = %+v", sel)
	}
}

func TestFactsAfterALandingThatRefreshed(t *testing.T) {
	f := newLandingFixture(t)
	_, err := f.lanes.Land(t.Context(), f.promoter, f.change("land"), api.PromoteOverlayInput{})
	testutil.FailErr(t, "land", err)
	got := f.facts(t, fakeState{}, workerlane.Assignment{Write: true})
	if got.LastChange != workerlane.ChangeLanded || !got.RefreshDone {
		t.Fatalf("after landing = %+v", got)
	}
	if sel := workerlane.Select(got); sel.Action != workerlane.ActionKeep {
		t.Fatalf("a refreshed lane was refreshed again: %+v", sel)
	}
	stacked := f.facts(t, fakeState{}, workerlane.Assignment{Write: true, BaseResultID: "result-1"})
	if stacked.BaseCheckpoint != "cp-job-1" {
		t.Fatalf("stacked start = %+v", stacked)
	}
}

func TestM14StateReadsMergeAndClaimState(t *testing.T) {
	jobs := map[string]*api.WorkerTask{"applying": {MergeStatus: api.WorkerMergeStatusApplying}, "merged": {MergeStatus: api.WorkerMergeStatusMerged}}
	snaps := map[string]claim.WorkspaceStateSnapshot{
		"recovering": {Readiness: claim.StateRecoveryRequired},
		"busy":       {Readiness: claim.StateReady, Command: claim.CommandRunning},
		"ready":      {Readiness: claim.StateReady, Command: claim.CommandIdle},
	}
	state := workerlane.M14State{
		Jobs: jobLookup(jobs),
		BranchState: func(root string) (claim.WorkspaceStateSnapshot, bool) {
			snap, ok := snaps[root]
			return snap, ok
		},
	}
	for job, want := range map[string]bool{"applying": true, "merged": false, "missing": false} {
		if got, _ := state.PromotionUnsettled(t.Context(), job); got != want {
			t.Fatalf("PromotionUnsettled(%s) = %v", job, got)
		}
	}
	for root, want := range map[string][2]bool{"recovering": {true, false}, "busy": {false, true}, "ready": {false, false}, "unknown": {false, false}} {
		recovery, holders, _ := state.Readiness(t.Context(), root)
		if [2]bool{recovery, holders} != want {
			t.Fatalf("Readiness(%s) = %v %v", root, recovery, holders)
		}
	}
}

type jobLookup map[string]*api.WorkerTask

func (j jobLookup) Get(id string) (*api.WorkerTask, bool) { t, ok := j[id]; return t, ok }
