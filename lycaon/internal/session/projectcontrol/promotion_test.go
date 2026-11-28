package projectcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/toolreach"
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

func TestPromoteOverlayHostEventBannerRecipientAuthority(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	t.Cleanup(func() { guidance.SetGuidanceRenderer(nil) })

	rebased := []api.OverlayRebaseOutcome{{
		OverlayID: "ov-child",
		Status:    api.WorkerMergeStatusRebasing,
		Conflicts: []string{"pkg/main.go"},
	}}

	t.Run("without recipient projection retains occurrence details and omits unestablished calls", func(t *testing.T) {
		f := &promotionFixture{rebased: rebased}
		s := New(promotionSessions{}, promotionSessions{}, nil, nil, f, f, f, f)
		s.SetOverlayPromoter(f)
		if _, err := s.PromoteOverlay(t.Context(), "session", "ov-parent", api.PromoteOverlayInput{}); err != nil {
			t.Fatalf("PromoteOverlay failed: %v", err)
		}
		if len(f.rows) != 1 {
			t.Fatalf("expected 1 recorded host-event message, got %d", len(f.rows))
		}
		delivered := f.rows[0].Content
		for _, want := range []string{"Result ov-child (stacked on ov-parent)", "`pkg/main.go`", "The parent landing remains accepted.", "Code: OVERLAY_REBASE_CONFLICT"} {
			if !strings.Contains(delivered, want) {
				t.Fatalf("host-event message lost occurrence detail %q:\n%s", want, delivered)
			}
		}
		for _, unestablished := range []string{"`preview_overlay`", "`promote_overlay`", "`instruct_worker`"} {
			if strings.Contains(delivered, unestablished) {
				t.Fatalf("host-event message without recipient projection leaked %s:\n%s", unestablished, delivered)
			}
		}
	})

	t.Run("admitted recipient projection delivers only projected tools", func(t *testing.T) {
		f := &promotionFixture{rebased: rebased}
		s := New(promotionSessions{}, promotionSessions{}, nil, nil, f, f, f, f)
		s.SetOverlayPromoter(f)
		ctx := toolreach.WithRequest(t.Context(), promptstest.StockToolReach(t, "preview_overlay", "instruct_worker"))
		if _, err := s.PromoteOverlay(ctx, "session", "ov-parent", api.PromoteOverlayInput{}); err != nil {
			t.Fatalf("PromoteOverlay with recipient reach failed: %v", err)
		}
		if len(f.rows) != 1 {
			t.Fatalf("expected 1 recorded host-event message, got %d", len(f.rows))
		}
		delivered := f.rows[0].Content
		if !strings.Contains(delivered, "`preview_overlay`") || !strings.Contains(delivered, "`instruct_worker`") {
			t.Fatalf("host-event message missing admitted tool instructions:\n%s", delivered)
		}
		if strings.Contains(delivered, "`promote_overlay`") {
			t.Fatalf("host-event message leaked unoffered promote_overlay:\n%s", delivered)
		}
	})
}
