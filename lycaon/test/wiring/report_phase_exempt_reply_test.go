package wiring

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A report phase with nothing to cite must still end: a reply that cites nothing is not
// the report, and the run reaches a delivered, host-assembled or blocked report within
// its budgets rather than idling with the gate open.
func TestNothingCitedReplyInAReportPhaseStillEndsInAReport(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "report-only", Version: "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "report", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"topology_report_delivered"}},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	nothingCited := func(modelcall.CompletionRequest) string { return "Nothing to report." }
	script := &citableScriptLLM{steps: []citableStep{{text: nothingCited}, {text: nothingCited}, {text: nothingCited}, {text: nothingCited}, {text: nothingCited}}}
	h := BuildForTest(t, WithLLMClient(script), WithManifestRegistry(map[string]workflowdef.Manifest{manifest.ID + "@" + manifest.Version: manifest}))
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, Request: "Report."})
	testutil.FailErr(t, "start run", err)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Deliver it.")
	testutil.FailErr(t, "prompt", err)

	current, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "read run", err)
	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	var reports []wire.Message
	for _, msg := range msgs {
		if msg.Kind == wire.MessageKindCompletionReport {
			reports = append(reports, msg)
		}
		t.Logf("row role=%s kind=%s grounding=%+v content=%.200q", msg.Role, msg.Kind, msg.Grounding, msg.Content)
	}
	script.mu.Lock()
	calls := script.next
	script.mu.Unlock()
	t.Logf("run status=%s phase=%s model calls=%d", current.Status, current.CurrentPhase, calls)
	if current.Status != wire.WorkflowRunStatusComplete {
		t.Fatalf("run stranded: status=%s phase=%s after %d model calls", current.Status, current.CurrentPhase, calls)
	}
	if len(reports) == 0 {
		t.Fatal("run ended with no report")
	}
	last := reports[len(reports)-1]
	if last.Grounding == nil || last.Grounding.EvidenceOptional || (!last.Grounding.HostAssembled && !last.Grounding.Traced) {
		t.Fatalf("last report grounding = %+v, want evidence or an honest host assembly", last.Grounding)
	}
}
