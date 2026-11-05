package wiring

import (
	"testing"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A report phase with nothing to cite must still end: a reply that cites nothing is not
// the report, and the run reaches a host-assembled or refused report within its budgets
// rather than idling with the gate open.
func TestNothingCitedReplyInAReportPhaseStillEndsInAReport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reply    string
		controls workflowdef.ManifestControls
		// maxCalls bounds model turns: none beyond the reply unless a document is owed.
		maxCalls  int
		assembled bool
		verified  string
	}{
		{name: "plain reply", reply: "Nothing to report.", maxCalls: 1, assembled: true},
		{name: "honest blocked reply", reply: "Nothing could be checked.\n[verify: blocked - No source was reachable]", maxCalls: 1, assembled: true, verified: "blocked"},
		{
			name: "report document owed", reply: "Nothing to report.", maxCalls: limits.DefaultReportDocumentRetries + 1,
			controls: workflowdef.ManifestControls{Report: &workflowdef.ReportControls{Enabled: true}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controls := tc.controls
			controls.PhaseAdvance = workflowdef.PhaseAdvanceHost
			manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
				ID: "report-only", Version: "1.0.0", Controls: controls,
				PhaseDefs: []workflowdef.PhaseDef{
					{ID: "report", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"topology_report_delivered"}},
					{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
				},
			})
			reply := func(modelcall.CompletionRequest) string { return tc.reply }
			script := &citableScriptLLM{}
			for range 8 {
				script.steps = append(script.steps, citableStep{text: reply})
			}
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
			var last *wire.Message
			for i := range msgs {
				if msgs[i].Kind == wire.MessageKindCompletionReport {
					last = &msgs[i]
				}
			}
			script.mu.Lock()
			calls := script.next
			script.mu.Unlock()
			if !runstate.IsTerminal(current.Status) || calls > tc.maxCalls {
				t.Fatalf("status=%s phase=%s after %d model calls, want a terminal run within %d", current.Status, current.CurrentPhase, calls, tc.maxCalls)
			}
			if !tc.assembled {
				return
			}
			if current.Status != wire.WorkflowRunStatusComplete || last == nil || last.Grounding == nil {
				t.Fatalf("status=%s report=%+v", current.Status, last)
			}
			g := last.Grounding
			if !g.HostAssembled || g.Traced || g.EvidenceOptional {
				t.Fatalf("grounding = %+v, want an honest host assembly", g)
			}
			if tc.verified != "" && (g.Verification == nil || string(g.Verification.Method) != tc.verified) {
				t.Fatalf("verification = %+v, want %s", g.Verification, tc.verified)
			}
		})
	}
}
