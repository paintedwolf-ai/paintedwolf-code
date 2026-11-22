package projectcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

type promotionSessions struct {
	Sessions
	mode api.SessionMode
}

func (p promotionSessions) Get(_ context.Context, id string) (*api.Session, error) {
	mode := p.mode
	if mode == "" {
		mode = api.SessionModeAgent
	}
	return &api.Session{ID: id, Mode: mode}, nil
}

func (promotionSessions) ReadSourceTurn(context.Context, string, int) (messageview.SourceTurn, error) {
	return messageview.SourceTurn{Number: 7}, nil
}

type promotionFixture struct {
	input                api.PromoteOverlayInput
	rows                 []api.Message
	rebased              []api.OverlayRebaseOutcome
	reconciles, promotes int
}

func (f *promotionFixture) Reconcile(context.Context, string)                { f.reconciles++ }
func (*promotionFixture) RoundComplete(context.Context, string) bool         { return true }
func (f *promotionFixture) MaybePromote(context.Context, string)             { f.promotes++ }
func (*promotionFixture) ActiveWorkflowRunID(context.Context, string) string { return "run" }
func (f *promotionFixture) AppendPlain(_ context.Context, _ string, m ...api.Message) error {
	f.rows = append(f.rows, m...)
	return nil
}
func (f *promotionFixture) PromoteOverlay(_ context.Context, _, _ string, in api.PromoteOverlayInput) (api.WorkerMergeResult, error) {
	f.input = in
	return api.WorkerMergeResult{Status: api.WorkerMergeStatusMerged, Rebased: f.rebased}, nil
}
func (*promotionFixture) RejectOverlay(context.Context, string, string, string) (api.OverlayRejectOutcome, error) {
	return api.OverlayRejectOutcome{}, nil
}

func TestPromotionAndRejectionRecordHostEventsAtCurrentTurn(t *testing.T) {
	f := &promotionFixture{}
	s := New(promotionSessions{}, promotionSessions{}, nil, nil, f, f, f, f)
	s.SetOverlayPromoter(f)
	result, err := s.PromoteOverlay(t.Context(), "session", "overlay", api.PromoteOverlayInput{})
	if err != nil || result.Status != api.WorkerMergeStatusMerged || f.input.UserTurn != 7 {
		t.Fatalf("promotion=%+v input=%+v err=%v", result, f.input, err)
	}
	_, err = s.RejectOverlay(t.Context(), "session", "overlay", "human refused")
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	if f.reconciles != 4 || f.promotes != 2 || len(f.rows) != 2 {
		t.Fatalf("effects=%+v", f)
	}
	for _, row := range f.rows {
		if row.WorkflowRunID != "run" || row.Role != api.MessageRoleTool || row.ToolResult == nil || row.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
			t.Fatalf("event=%+v", row)
		}
	}
	if !strings.Contains(f.rows[0].Content, `"merge_status":"merged"`) {
		t.Fatalf("promotion event=%q", f.rows[0].Content)
	}
}

func TestPromoteOverlayRebaseConflictBannersRespectSessionMode(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	t.Cleanup(func() { guidance.SetGuidanceRenderer(nil) })

	rebased := []api.OverlayRebaseOutcome{{
		OverlayID: "ov-child",
		Status:    api.WorkerMergeStatusRebasing,
		Conflicts: []string{"pkg/main.go"},
	}}
	for _, tc := range []struct {
		mode        api.SessionMode
		wantPromote bool
	}{
		{api.SessionModeAgent, true},
		{api.SessionModeAsk, false},
	} {
		f := &promotionFixture{rebased: rebased}
		sessions := promotionSessions{mode: tc.mode}
		s := New(sessions, sessions, nil, nil, f, f, f, f)
		s.SetOverlayPromoter(f)
		if _, err := s.PromoteOverlay(t.Context(), "session", "ov-parent", api.PromoteOverlayInput{}); err != nil {
			t.Fatalf("mode %s PromoteOverlay failed: %v", tc.mode, err)
		}
		if len(f.rows) != 1 || !strings.Contains(f.rows[0].Content, "`preview_overlay`") {
			t.Fatalf("mode %s missing preview_overlay banner: %+v", tc.mode, f.rows)
		}
		if strings.Contains(f.rows[0].Content, "`promote_overlay`") != tc.wantPromote {
			t.Fatalf("mode %s promote_overlay presence = %v, want %v:\n%s", tc.mode, !tc.wantPromote, tc.wantPromote, f.rows[0].Content)
		}
	}
}
