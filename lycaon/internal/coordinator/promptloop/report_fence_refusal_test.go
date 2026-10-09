package promptloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Unread members take precedence and survive document repair.
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
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	var rendered string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CheckRunReportDocument: func(context.Context, string, guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
				t.Fatal("document validation ran on an unreadable fence")
				return nil, nil
			},
			HintConfig: hints,
			RejectFmt:  guidance.NewStaticRejectFormatter(hints),
			EvaluateCloseoutBlock: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				code := guidance.ReportFenceUnreadableCode
				if gc.Rejection.RejectObservation != guidance.ReportDocumentObservation(code) {
					t.Fatalf("observation = %q, want the unreadable fence first", gc.Rejection.RejectObservation)
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
				var err error
				rendered, err = engine.RenderKick(t.Context(), "coordinator-report-document", data)
				return rendered, err
			},
		},
		Projection: ProjectionDeps{
			AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
			AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			UpdateMessage:      func(context.Context, string, string, api.Message) error { return nil },
		},
	})
	assistant := api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: content}
	st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true,
		history: []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Survey it"}, assistant}}
	out, err := loop.Closeout.handleAcceptedCloseoutReport(t.Context(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()}, "s1", "", "coordinator_security_synthesis", st, st.history, assistant, read)
	testutil.FailErr(t, "refuse the unread fence", err)

	if !out.retry || noted.code != guidance.ReportFenceUnreadableCode || len(noted.unread) != 2 {
		t.Fatalf("outcome = %+v, noted = %+v; want a document retry that keeps both unread members", out, noted)
	}
	if kick == nil || kick["run_report"] != true || !strings.Contains(kick["offenders_sample"].(string), "`findings[].ask` (2): `ask` is a top-level report field") {
		t.Fatalf("kick data = %v, want the grouped member named on a run report", kick)
	}
	for _, want := range []string{"findings[].ask"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("repair omitted %q: %s", want, rendered)
		}
	}
	var fence guidance.CoordinatorCompletionReport
	testutil.FailErr(t, "decode the retained fence", json.Unmarshal([]byte(kick["retained_document"].(string)), &fence))
	if len(fence.Findings) != 2 || fence.Findings[0].Disposition != "act" || len(fence.SetAsides) != 1 || fence.Ask != nil {
		t.Fatalf("retained fence = %+v, want every field the host read and nothing it did not", fence)
	}
}

func TestUnreadReportFencePrecedesArtifactEmbed(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CheckRunReportDocument: func(context.Context, string, guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
				t.Fatal("unread document reached semantic validation")
				return nil, nil
			},
		},
	})
	report := guidance.CoordinatorCompletionReport{Synthesis: "![report](90dcba36-946a-46c6-933d-e94207b897ab)"}
	read, ok := guidance.ReadCloseoutReport("Report narrative.\n\n```json\n{\"findings\":[{\"id\":\"c1\",\"title\":\"Question\",\"disposition\":\"act\",\"ask\":{\"do\":\"x\"}}]}\n```", "")
	if !ok || len(read.Unread) == 0 {
		t.Fatal("fixture must contain an unread report member")
	}
	observed, err := loop.Closeout.observeCloseoutReport(t.Context(), &api.Session{ID: "session"}, nil, "coordinator_security_synthesis", report, read.Unread, nil, true)
	testutil.FailErr(t, "observe unread fence with embed", err)
	if observed.facts.RejectObservation != guidance.ReportDocumentObservation(guidance.ReportFenceUnreadableCode) {
		t.Fatalf("unread fence was masked by embed: %s", observed.facts.RejectObservation)
	}
}
