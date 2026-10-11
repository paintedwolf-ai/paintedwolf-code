package blueprints

import (
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildBlueprintTranscriptMetaAwaitingApproval(t *testing.T) {
	run := &api.WorkflowRun{CurrentPhase: "approve"}
	proposal := &api.Blueprint{
		Path:    settingsoverlay.Rel("blueprints/p1.md"),
		Title:   "Feature",
		Status:  api.BlueprintStatusDraft,
		Version: 3,
	}
	meta := buildBlueprintTranscriptMeta(run, proposal, true, false)
	if meta.Status != api.BlueprintTranscriptStatusAwaitingApproval {
		t.Fatalf("status = %q", meta.Status)
	}
	if meta.Phase != api.BlueprintCardPhaseReady || !meta.ShowActions {
		t.Fatalf("meta = %+v", meta)
	}
	if !meta.CanApprove {
		t.Fatal("CanApprove must follow awaiting approval only")
	}
}
func TestBuildBlueprintTranscriptMetaEndedRunRecordsTheDecision(t *testing.T) {
	proposal := &api.Blueprint{
		Path:    settingsoverlay.Rel("blueprints/p1.md"),
		Title:   "Feature",
		Status:  api.BlueprintStatusDraft,
		Version: 3,
	}

	rejected := buildBlueprintTranscriptMeta(
		&api.WorkflowRun{CurrentPhase: "approve", Status: api.WorkflowRunStatusCanceled},
		proposal, true, false,
	)
	if rejected.Status != api.BlueprintTranscriptStatusRejected {
		t.Fatalf("status = %q want rejected", rejected.Status)
	}
	if rejected.Phase != api.BlueprintCardPhaseRejected || rejected.PhaseLabel != "Rejected" {
		t.Fatalf("meta = %+v", rejected)
	}
	// Scaffold vars can still say the phase was awaiting when the run was cut.
	if rejected.CanApprove || rejected.ShowActions {
		t.Fatalf("ended run must not offer approval: %+v", rejected)
	}
	if !rejected.Collapsed {
		t.Fatal("a decided row collapses to its record")
	}

	superseded := buildBlueprintTranscriptMeta(
		&api.WorkflowRun{
			CurrentPhase: "approve",
			Status:       api.WorkflowRunStatusCanceled,
			PauseReason:  runstate.ExitReasonSupersededByWorkflowStart,
		},
		proposal, true, false,
	)
	if superseded.Status != api.BlueprintTranscriptStatusSuperseded {
		t.Fatalf("status = %q want superseded", superseded.Status)
	}
	if superseded.PhaseLabel != "Superseded" {
		t.Fatalf("phase label = %q", superseded.PhaseLabel)
	}

	// An approved blueprint keeps its approval record whatever ended the run.
	approved := *proposal
	approved.Status = api.BlueprintStatusImplementing
	done := buildBlueprintTranscriptMeta(
		&api.WorkflowRun{CurrentPhase: "execute", Status: api.WorkflowRunStatusCanceled},
		&approved, false, false,
	)
	if done.Status != api.BlueprintTranscriptStatusApproved {
		t.Fatalf("status = %q want approved", done.Status)
	}
	if done.Phase != api.BlueprintCardPhaseBuilding {
		t.Fatalf("phase = %q", done.Phase)
	}
}
