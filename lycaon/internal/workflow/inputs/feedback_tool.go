package inputs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// FeedbackToolResult is returned by workflow_user_feedback.
type FeedbackToolResult struct {
	Pending bool   `json:"pending"`
	PhaseID string `json:"phase_id,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// RegisterFeedbackTool registers workflow_user_feedback for coordinator sessions.
func RegisterFeedbackTool(reg *tools.DefaultRegistry, runs *Feedback) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	if err := reg.Register("workflow_user_feedback", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("workflow_user_feedback requires coordinator role")
		}
		if len(args) > 0 {
			return "", fmt.Errorf("workflow_user_feedback takes no arguments")
		}
		active, err := runs.Runs.ActiveBySession(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			out, _ := json.Marshal(FeedbackToolResult{Pending: false})
			return string(out), nil
		}
		vars, err := runs.Runs.GetScaffoldVars(ctx, active.ID)
		if err != nil {
			return "", err
		}
		pending, ok := runstate.PendingFeedbackFromVars(vars)
		if !ok {
			out, _ := json.Marshal(FeedbackToolResult{Pending: false})
			return string(out), nil
		}
		out, _ := json.Marshal(FeedbackToolResult{
			Pending: true,
			PhaseID: pending.PhaseID,
			Prompt:  strings.TrimSpace(pending.Prompt),
		})
		return string(out), nil
	}); err != nil {
		return err
	}
	return nil
}
