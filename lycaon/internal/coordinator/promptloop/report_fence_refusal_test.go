package promptloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A closeout whose fence has members the report does not take is refused by
// name, ahead of every other check, and repaired from the fields the host
// read: the kick returns them as the fence, and the cycle keeps the unread
// members for the report stored if repair runs out.
func TestUnreadReportFenceIsRefusedByName(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	hints := loadCoordinatorTestHintConfig(t)
	content := "## Survey\n\nNo exploitable path.\n\n```json\n" + `{
		"findings": [
			{"id": "c1", "title": "Pin bump", "disposition": "act", "ask": {"do": "Bump it.", "effort": "small"}},
			{"id": "c2", "title": "Auth chain", "disposition": "held", "ask": {"do": "Nothing.", "effort": "small"}}
		],
		"set_asides": [{"scanner": "secrets", "paths": ["**/testdata/**"], "reason": "fixtures"}]
	}` + "\n```"
	read, ok := guidance.ReadCloseoutReport(content, "")
	if !ok {
		t.Fatal("the draft is a report")
	}
	var noted struct {
		code   string
		unread []jsonshape.Issue
	}
	var kick map[string]any
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: hints, RejectFmt: guidance.NewStaticRejectFormatter(hints),
		EvaluateCloseoutBlock: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
			code := guidance.ReportFenceUnreadableCode
			if gc.RejectObservation != guidance.ReportDocumentObservation(code) {
				t.Fatalf("observation = %q, want the unreadable fence first", gc.RejectObservation)
			}
			return &oar.Decision{Code: code, Data: gc.RejectData[code]}, nil
		},
		NoteCloseoutGroundingReject: func(_ context.Context, _, code, _, _ string, unread []jsonshape.Issue) (int, string) {
			noted.code, noted.unread = code, unread
			return 1, ""
		},
		RenderHostKick: func(_ context.Context, _ string, data map[string]any) (string, error) {
			if _, ok := data["retained_document"]; ok {
				kick = data
			}
			return "kick", nil
		},
		AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
		AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
		UpdateMessage:      func(context.Context, string, string, api.Message) error { return nil },
	})
	assistant := api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: content}
	st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true,
		history: []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Survey it"}, assistant}}
	out, err := loop.handleAcceptedCloseoutReport(t.Context(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()}, "s1", "", "coordinator_security_synthesis", st, st.history, assistant, read)
	testutil.FailErr(t, "refuse the unread fence", err)

	if !out.retry || noted.code != guidance.ReportFenceUnreadableCode || len(noted.unread) != 2 {
		t.Fatalf("outcome = %+v, noted = %+v; want a document retry that keeps both unread members", out, noted)
	}
	if kick == nil || kick["run_report"] != true || !strings.Contains(kick["offenders_sample"].(string), "`findings[].ask` (2): `ask` is a top-level report field") {
		t.Fatalf("kick data = %v, want the grouped member named on a run report", kick)
	}
	var fence guidance.CoordinatorCompletionReport
	testutil.FailErr(t, "decode the retained fence", json.Unmarshal([]byte(kick["retained_document"].(string)), &fence))
	if len(fence.Findings) != 2 || fence.Findings[0].Disposition != "act" || len(fence.SetAsides) != 1 || fence.Ask != nil {
		t.Fatalf("retained fence = %+v, want every field the host read and nothing it did not", fence)
	}
}
