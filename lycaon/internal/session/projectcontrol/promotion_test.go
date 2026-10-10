package projectcontrol

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

type promotionSessions struct{ Sessions }

func (promotionSessions) UserTurnOrdinal(context.Context, string) (int, error) { return 7, nil }

type promotionFixture struct {
	input                api.PromoteOverlayInput
	rows                 []api.Message
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
	return api.WorkerMergeResult{Status: api.WorkerMergeStatusMerged}, nil
}
func (*promotionFixture) RejectOverlay(context.Context, string, string, string) (api.OverlayRejectOutcome, error) {
	return api.OverlayRejectOutcome{}, nil
}

func TestPromotionAndRejectionRecordHostEventsAtCurrentTurn(t *testing.T) {
	f := &promotionFixture{}
	s := New(promotionSessions{}, nil, nil, f, f, f, f)
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
