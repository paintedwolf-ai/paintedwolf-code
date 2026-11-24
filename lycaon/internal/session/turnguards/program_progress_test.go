package turnguards

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type programSessions map[string]*api.Session

func (s programSessions) Get(_ context.Context, id string) (*api.Session, error) { return s[id], nil }

func TestProgramProgressUsesLiveRootStoreAndWorkerFacts(t *testing.T) {
	store := programSessions{"parent": {ID: "parent"}, "child": {ID: "child", ParentSessionID: "parent"}}
	owner := progresstest.New(t, "parent", "child")
	progresstest.SeedRows(t, owner, "parent", progress.RowSpec{Label: "Root task"})
	progresstest.SeedRows(t, owner, "child", progress.RowSpec{Label: "Child-only task"})
	svc := &Service{store: store, progress: owner}
	state, err := svc.ProgramProgressState(t.Context(), "child")
	testutil.FailErr(t, "read actual root facts", err)
	if state.SessionID != "child" || state.RootSessionID != "parent" || state.Missing || state.OpenSteps != 1 || !state.WorkerChild || state.ReviewLoopActive {
		t.Fatalf("incorrect root/session facts: %+v", state)
	}
	progresstest.UpdateRows(t, owner, "parent", progress.UpdateRowSpec{ID: "p1", State: progresstest.State(progress.ProgressStateDone)})
	state, err = svc.ProgramProgressState(t.Context(), "parent")
	testutil.FailErr(t, "read current root facts", err)
	if state.Missing || state.OpenSteps != 0 || state.WorkerChild {
		t.Fatalf("stale or invented session facts: %+v", state)
	}
}

func TestProgramProgressRejectsMissingIdentityAndCancellation(t *testing.T) {
	for _, svc := range []*Service{nil, {}, {store: programSessions{}}, {store: programSessions{"requested": {ID: "other"}}}} {
		if _, err := svc.ProgramProgressState(t.Context(), "requested"); err == nil {
			t.Fatal("missing or mismatched session facts admitted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (&Service{}).ProgramProgressState(ctx, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
