package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowDeliveryRequiresAcceptedCurrentPhaseCloseout(t *testing.T) {
	for _, tc := range []struct {
		name, run, phase                                         string
		internal, assembled, draft, sessionScope, toolCall, want bool
	}{
		{name: "current", run: "run", phase: "work", want: true},
		{name: "other run", run: "old-run", phase: "work"},
		{name: "earlier phase", run: "run", phase: "boot"},
		{name: "rejected draft", run: "run", phase: "work", internal: true},
		{name: "host grounded closeout", run: "run", phase: "work", assembled: true, want: true},
		{name: "host grounded draft", run: "run", phase: "work", assembled: true, draft: true},
		{name: "session report", run: "run", phase: "work", sessionScope: true},
		{name: "tool step", run: "run", phase: "work", toolCall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, sess, _ := verifyGateHarness(t, "")
			ready, err := mgr.Transcript.DeliveredWorkflowPhase(t.Context(), sess.ID, "run", "work")
			testutil.FailErr(t, "read initial delivery state", err)
			if ready {
				t.Fatal("empty work phase completed before doing work")
			}
			msg := api.Message{ID: "report", Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport,
				Content: "Delivered, with validation limits.", WorkflowRunID: tc.run,
				Visibility: api.MessageVisibilityTranscript, CompletionReport: &api.CompletionReportMeta{Scope: api.CompletionReportScopePhase, Phase: tc.phase},
			}
			if tc.internal {
				msg.Visibility = api.MessageVisibilityInternal
			}
			if tc.assembled {
				msg.Grounding = &api.CitationGrounding{HostAssembled: true}
			}
			if tc.draft {
				msg.Kind = api.MessageKindDraft
				msg.CompletionReport = nil
			}
			if tc.sessionScope {
				msg.CompletionReport.Scope = api.CompletionReportScopeSession
			}
			if tc.toolCall {
				msg.ToolCalls = []api.ToolCall{{ID: "call", Name: "read"}}
			}
			testutil.FailErr(t, "append closeout", mgr.store.AppendMessages(t.Context(), sess.ID, msg))
			ready, err = mgr.Transcript.DeliveredWorkflowPhase(t.Context(), sess.ID, "run", "work")
			testutil.FailErr(t, "read reported delivery state", err)
			if ready != tc.want {
				t.Fatalf("ready=%v want %v", ready, tc.want)
			}
		})
	}
}
