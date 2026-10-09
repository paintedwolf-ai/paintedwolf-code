package lifecycle_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRootWorkflowExitReleasesVisibleTurn(t *testing.T) {
	for _, workflowID := range []string{"implement", "plan", "recon-pack"} {
		for _, paused := range []bool{false, true} {
			t.Run(workflowID+map[bool]string{false: "/running", true: "/paused"}[paused], func(t *testing.T) {
				ctx := t.Context()
				mgr, st, _, _ := testManagerWithRegistry(t)
				runtime := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
				runtime.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: mgr.Store.Runs, Approvals: mgr.Policy, Obligations: mgr.Obligations})
				runtime.SetSessionWorkflowStop(mgr.Controls)
				mgr.Starts.Barrier = runtime
				mgr.Controls.SessionExit = runtime
				var run *api.WorkflowRun
				var err error
				if workflowID == "implement" {
					run, err = mgr.Ambient.StartAmbient(ctx, "sess-1", workflowID, "1.0.0")
				} else {
					run, err = startRun(ctx, mgr, "sess-1", workflowID, "1.0.0")
				}
				testutil.FailErr(t, "start workflow", err)
				if paused {
					run, err = mgr.Controls.Pause(ctx, run.ID, "pause for review")
					testutil.FailErr(t, "pause workflow", err)
				}
				testutil.FailErr(t, "mark visible turn busy", st.SetSessionStatus(ctx, "sess-1", api.SessionStatusBusy))
				if _, err := mgr.Controls.Exit(ctx, "sess-1", run.ID, run.Revision+1, "stale review"); !errors.Is(err, runstate.ErrRevisionConflict) {
					t.Fatalf("stale exit error = %v", err)
				}
				current, err := st.Get(ctx, "sess-1")
				testutil.FailErr(t, "read rejected exit", err)
				if current.Status != api.SessionStatusBusy {
					t.Fatal("stale exit stopped the current turn")
				}
				_, err = mgr.Controls.Exit(ctx, "sess-1", run.ID, run.Revision, "reviewed exit")
				testutil.FailErr(t, "exit workflow", err)
				current, err = st.Get(ctx, "sess-1")
				testutil.FailErr(t, "read settled session", err)
				if current.Status != api.SessionStatusIdle {
					t.Fatalf("session status = %s after exit", current.Status)
				}
				active, err := mgr.Store.Runs.ActiveBySession(ctx, "sess-1")
				testutil.FailErr(t, "read replacement ambient", err)
				if workflowID == "implement" {
					if active != nil {
						t.Fatalf("ambient exit left active workflow: %+v", active)
					}
				} else if active == nil || !runstate.IsAmbientRun(active) {
					t.Fatalf("active workflow = %+v, want ambient", active)
				}
				_, err = startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
				testutil.FailErr(t, "start next workflow", err)
			})
		}
	}
}
