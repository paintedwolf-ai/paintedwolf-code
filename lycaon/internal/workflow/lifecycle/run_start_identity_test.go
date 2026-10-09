package lifecycle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
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

// A run pinned to a version the catalog no longer holds stays an unknown
// workflow to callers while naming the exact version it cannot resume.
func TestWorkflowVersionUnavailableNamesThePinnedVersion(t *testing.T) {
	var err error = &runstate.WorkflowVersionUnavailableError{WorkflowID: "security-survey", Version: "1.0.0"}
	if !errors.Is(err, workflowdef.ErrUnknownWorkflow) {
		t.Fatalf("%v must remain an unknown workflow", err)
	}
	if !strings.Contains(err.Error(), "security-survey@1.0.0") {
		t.Fatalf("message %q must name the pinned version", err.Error())
	}
	var unavailable *runstate.WorkflowVersionUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatal("the version error must be recoverable from the chain")
	}
	if got := unavailable.RejectionCode(); got != "WORKFLOW_VERSION_UNAVAILABLE" {
		t.Fatalf("rejection code = %q", got)
	}
	if got := unavailable.NoticeCode(); got != api.NoticeCodeWorkflowVersionUnavailable {
		t.Fatalf("notice code = %q", got)
	}
}
