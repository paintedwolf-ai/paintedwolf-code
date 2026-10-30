package turnguards

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type programSessions map[string]*api.Session

func (s programSessions) Get(_ context.Context, id string) (*api.Session, error) { return s[id], nil }

func TestProgramProgressUsesLiveRootStoreAndWorkerFacts(t *testing.T) {
	store := programSessions{"parent": {ID: "parent"}, "child": {ID: "child", ParentSessionID: "parent"}}
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set root progress", owner.Set("parent", "- [ ] Root task"))
	testutil.FailErr(t, "set distinct child progress", owner.Set("child", "- [ ] Child-only task"))
	svc := &Service{store: store, progress: owner}
	state, err := svc.ProgramProgressState(t.Context(), "child")
	testutil.FailErr(t, "read actual root facts", err)
	if state.SessionID != "child" || state.RootSessionID != "parent" || state.Content != "- [ ] Root task" || !state.WorkerChild || state.ReviewLoopActive {
		t.Fatalf("incorrect root/session facts: %+v", state)
	}
	testutil.FailErr(t, "replace root progress", owner.Set("parent", "- [x] Root task"))
	state, err = svc.ProgramProgressState(t.Context(), "parent")
	testutil.FailErr(t, "read current root facts", err)
	if state.Content != "- [x] Root task" || state.WorkerChild {
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
