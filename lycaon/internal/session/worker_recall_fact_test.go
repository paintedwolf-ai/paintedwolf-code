package session_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func renderViewRows(id string) []api.Message {
	return []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: id, Name: "render_view", Args: map[string]any{"markup": "<div/>"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"mime":"image/png"}`}},
	}
}

func surfaceClaimReport(handle string) api.Message {
	return completeLegToolRow(map[string]any{
		"leg_status": "complete",
		"findings":   []any{map[string]any{"evidence": handle, "note": "The dialog renders centered"}},
		"brief":      "The dialog renders centered",
	})
}

// The worker repair names recall only when the worker's latest turn could call it.
func TestWorkerGroundingRepairRecallFollowsOfferedTools(t *testing.T) {
	cases := []struct {
		name         string
		workerTurn   []string
		retryTurn    []string
		wantRecall   []bool
		wantAttempts int
	}{
		{name: "offered on the worker turn", workerTurn: []string{"read", "recall", "complete_leg"}, retryTurn: []string{"read", "recall", "complete_leg"}, wantRecall: []bool{true, true}, wantAttempts: 2},
		{name: "absent after restart, offered on the retry turn", workerTurn: nil, retryTurn: []string{"recall", "complete_leg"}, wantRecall: []bool{false, true}, wantAttempts: 2},
		{name: "never offered", workerTurn: []string{"read", "complete_leg"}, retryTurn: []string{"read", "complete_leg"}, wantRecall: []bool{false, false}, wantAttempts: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := loadWorkerSummaryTestFinalizeOpts(t)
			opts.ProjectDir = t.TempDir()
			opts.AgentType = "path-explorer"
			opts.MaxGroundingRetries = 2
			opts.OfferedTools = tc.workerTurn
			opts.RenderWorkerKick = prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}).RenderKick

			resolver := &stubSummaryResolver{msgs: append(renderViewRows("rv1"), surfaceClaimReport("render#1"))}
			var retries []string
			resolver.promptFunc = func(_ context.Context, _ string, text string) (*promptresult.Result, error) {
				retries = append(retries, text)
				// Each retry renders again, so the offender set changes and stuck detection keeps retrying.
				resolver.msgs = append(resolver.msgs, renderViewRows(fmt.Sprintf("rv%d", len(retries)+1))...)
				resolver.msgs = append(resolver.msgs, surfaceClaimReport(fmt.Sprintf("render#%d", len(retries)+1)))
				resolver.stampEvidenceHandles()
				return &promptresult.Result{MessageID: "retry", OfferedTools: tc.retryTurn}, nil
			}

			_, err := workercloseout.FinalizeWorkerSummaryForChild(context.Background(), resolver, resolver, "child", "path-explorer", finalizeOpts(resolver, "child", opts))
			testutil.FailErr(t, "finalize worker summary", err)
			if len(retries) != tc.wantAttempts {
				t.Fatalf("retries = %d want %d:\n%s", len(retries), tc.wantAttempts, strings.Join(retries, "\n---\n"))
			}
			for i, text := range retries {
				if !strings.Contains(text, "Code: SURFACE_CLAIM_UNGROUNDED") {
					t.Fatalf("retry %d is not the surface claim repair:\n%s", i+1, text)
				}
				header, reject, ok := strings.Cut(text, "\n\nRejected: ")
				if !ok {
					t.Fatalf("retry %d has no rejection after the kick:\n%s", i+1, text)
				}
				if got := strings.Contains(header, `recall(query="handle:`); got != tc.wantRecall[i] {
					t.Errorf("retry %d kick names recall = %v want %v:\n%s", i+1, got, tc.wantRecall[i], header)
				}
				if got := strings.Contains(reject, `recall(query="handle:`); got != tc.wantRecall[i] {
					t.Errorf("retry %d repair names recall = %v want %v:\n%s", i+1, got, tc.wantRecall[i], reject)
				}
				if !tc.wantRecall[i] && strings.Contains(text, "recall") {
					t.Errorf("retry %d names recall while it is not callable:\n%s", i+1, text)
				}
			}
		})
	}
}
