package presentation

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestReportAvailabilityRequiresEnabledDeliveredTerminalRun(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, delivered := range []bool{false, true} {
			for _, state := range []struct {
				status   api.WorkflowRunStatus
				terminal bool
			}{
				{api.WorkflowRunStatusRunning, false},
				{api.WorkflowRunStatusPaused, false},
				{api.WorkflowRunStatusPausedOnChild, false},
				{api.WorkflowRunStatusComplete, true},
				{api.WorkflowRunStatusFailed, true},
				{api.WorkflowRunStatusCanceled, true},
				{api.WorkflowRunStatusInterrupted, true},
			} {
				manifest := workflowdef.Manifest{Controls: workflowdef.ManifestControls{Report: &workflowdef.ReportControls{Enabled: enabled}}}
				vars := runstate.SetGateSatisfied(nil, "topology_report_delivered", delivered)
				got := reportAvailable(&api.WorkflowRun{Status: state.status}, manifest, vars)
				want := enabled && delivered && state.terminal
				if got != want {
					t.Fatalf("enabled=%v delivered=%v status=%s: got %v want %v", enabled, delivered, state.status, got, want)
				}
			}
		}
		// A run failed on a report the host did not accept still offers it.
		notAccepted := &api.WorkflowRun{Status: api.WorkflowRunStatusFailed, Failure: &api.WorkflowFailure{Code: runstate.ReportNotAcceptedFailureCode}}
		otherFailure := &api.WorkflowRun{Status: api.WorkflowRunStatusFailed, Failure: &api.WorkflowFailure{Code: "TOPOLOGY_EXECUTION_FAILED"}}
		manifest := workflowdef.Manifest{Controls: workflowdef.ManifestControls{Report: &workflowdef.ReportControls{Enabled: enabled}}}
		if got := reportAvailable(notAccepted, manifest, nil); got != enabled {
			t.Fatalf("enabled=%v not accepted: got %v", enabled, got)
		}
		if reportAvailable(otherFailure, manifest, nil) {
			t.Fatalf("enabled=%v: another failure offered a report", enabled)
		}
	}
}
