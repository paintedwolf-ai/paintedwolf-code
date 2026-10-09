package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewedStopTransitionPreservesRuntimeOnRejection(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted"}[accepted], func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemory()
			mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
			token, err := mgr.Gate.Capture(ctx, sess.ID)
			testutil.FailErr(t, "capture current turn", err)
			refusal := errors.New("stale workflow revision")
			err = mgr.Stops.WithSessionTreeStop(ctx, sess.ID, "reviewed exit", func(context.Context) error {
				if !accepted {
					return refusal
				}
				return nil
			})
			if accepted {
				testutil.FailErr(t, "stop accepted exit", err)
			} else if !errors.Is(err, refusal) {
				t.Fatalf("rejected exit error = %v", err)
			}
			current, err := st.Get(ctx, sess.ID)
			testutil.FailErr(t, "read session", err)
			want := api.SessionStatusBusy
			if accepted {
				want = api.SessionStatusIdle
			}
			if current.Status != want || mgr.Gate.MayDrain(token) == accepted {
				t.Fatalf("status=%s drain=%v accepted=%v", current.Status, mgr.Gate.MayDrain(token), accepted)
			}
			if mgr.Gate.InProgress(ctx, sess.ID) {
				t.Fatal("stop barrier retained after transition")
			}
			testutil.FailErr(t, "admit next turn", mgr.Gate.WithSessionTreeAdmission(ctx, sess.ID, func() error { return nil }))
		})
	}
}

func TestReviewedStopTransitionStillStopsOtherSessionWorkflows(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	workflows := &stubSessionWorkflowStop{}
	mgr.Stops.SetWorkflowStop(workflows)
	root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := st.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "exit root workflow", mgr.Stops.WithSessionTreeStop(ctx, root.ID, "reviewed exit", func(context.Context) error { return nil }))
	if len(workflows.sessions) != 1 || workflows.sessions[0] != child.ID {
		t.Fatalf("workflow stops = %v, want child only after committed root transition", workflows.sessions)
	}
	for _, id := range []string{root.ID, child.ID} {
		current, err := st.Get(ctx, id)
		testutil.FailErr(t, "read stopped session", err)
		if current.Status != api.SessionStatusIdle {
			t.Fatalf("session %s status = %s", id, current.Status)
		}
	}
}
