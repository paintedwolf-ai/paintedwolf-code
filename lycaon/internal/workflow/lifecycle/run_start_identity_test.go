package lifecycle_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStartHumanRequiresExactWorkflowIdentity(t *testing.T) {
	for _, request := range []api.StartWorkflowRunRequest{
		{WorkflowID: "plan"},
		{WorkflowVersion: "1.0.0"},
		{WorkflowID: "plan", WorkflowVersion: " "},
	} {
		t.Run(request.WorkflowID+"@"+request.WorkflowVersion, func(t *testing.T) {
			mgr, _, _, _ := testManagerWithRegistry(t)
			_, err := mgr.Starts.StartHuman(t.Context(), "sess-1", request)
			if !errors.Is(err, workflowdef.ErrUnknownWorkflow) {
				t.Fatalf("start identity: got %v, want unknown workflow", err)
			}
			active, err := mgr.Store.Runs.ActiveBySession(t.Context(), "sess-1")
			testutil.FailErr(t, "read active run", err)
			if active != nil {
				t.Fatalf("invalid identity created run %s", active.ID)
			}
		})
	}
}
