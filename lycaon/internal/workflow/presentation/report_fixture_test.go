package presentation_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func topologyReportTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "reporttest",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "report",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"topology_report_delivered"},
				Next:         "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func seedTopologyCompletion(t *testing.T, mgr *workflow.RunManager, run *api.WorkflowRun, document bool, edit func(*api.Message)) string {
	t.Helper()
	scope := api.CompletionReportScopePhase
	if document {
		scope = api.CompletionReportScopeRun
	}
	msg := api.Message{
		ID: uuid.NewString(), Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport,
		Content: "Completed assessment.", CreatedAt: time.Now().UTC(), WorkflowRunID: run.ID,
		Visibility: api.MessageVisibilityTranscript, DraftStatus: api.DraftStatusCommitted,
		Grounding:        &api.CitationGrounding{Traced: true},
		CompletionReport: &api.CompletionReportMeta{Scope: scope, Phase: run.CurrentPhase},
	}
	if edit != nil {
		edit(&msg)
	}
	testutil.FailErr(t, "persist phase completion", mgr.Verdicts.Sessions.AppendMessages(t.Context(), run.SessionID, msg))
	return msg.ID
}
