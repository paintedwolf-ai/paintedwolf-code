package lifecycle_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskWorkflowAdmissionCommitsOnlyAllowedModeChanges(t *testing.T) {
	mgr, sessions, _, _ := testManagerWithRegistry(t)
	ctx := workflowCaller(t, mgr)
	ask := api.SessionModeAsk
	request := api.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.1", Mode: &ask}
	if _, err := mgr.Starts.StartHuman(ctx, "sess-1", request); !errors.Is(err, runstate.ErrAskWorkflowRequiresAgent) {
		t.Fatalf("start plan with Ask intent = %v, want workflow requires Agent", err)
	}
	root, err := sessions.ResolveEffectiveMode(ctx, "sess-1")
	testutil.FailErr(t, "resolve mode after refusal", err)
	if root.Mode != api.SessionModeAgent || root.Started {
		t.Fatalf("refused workflow changed mode to %+v", root)
	}
	run, err := mgr.Store.Runs.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "look up refused workflow", err)
	if run != nil {
		t.Fatalf("refused workflow created run %+v", run)
	}

	_, err = sessions.SetMode(ctx, "sess-1", ask)
	testutil.FailErr(t, "choose Ask before starting", err)
	agent := api.SessionModeAgent
	request.Mode = &agent
	run, err = mgr.Starts.StartHuman(ctx, "sess-1", request)
	testutil.FailErr(t, "start workflow with Agent intent", err)
	if run == nil {
		t.Fatal("admitted workflow is missing")
	}
	root, err = sessions.ResolveEffectiveMode(ctx, "sess-1")
	testutil.FailErr(t, "resolve mode after workflow admission", err)
	if root.Mode != api.SessionModeAgent || !root.Started {
		t.Fatalf("workflow did not bind Agent mode: %+v", root)
	}
}
