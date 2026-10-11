package hitl

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/observability"
)

// applyToolApprovalRequest persists the persisted shape of a tool-approval card.
func applyToolApprovalRequest(row *StoredCheckpoint, req CheckpointRequest) error {
	if req.ProposedAction == nil {
		return fmt.Errorf("proposed_action required for tool_approval")
	}
	row.ToolName = observability.RedactCaptureText(req.ProposedAction.Invocation.Tool)
	if scrubbed, ok := observability.RedactCaptureValue(req.ProposedAction.Invocation.Args).(map[string]any); ok {
		row.Args = scrubbed
	}
	row.Files = append([]string(nil), req.ProposedAction.Invocation.Files...)
	for i := range row.Files {
		row.Files[i] = observability.RedactCaptureText(row.Files[i])
	}
	row.ProjectDir = observability.RedactCaptureText(req.ProposedAction.Scope.ProjectDir)
	row.Description = observability.RedactCaptureText(row.Description)
	if req.ToolCallID != "" {
		row.Payload["tool_call_id"] = req.ToolCallID
	}
	if row.Title == "" {
		row.Title = fmt.Sprintf("Approve %s", row.ToolName)
	}
	row.Title = observability.RedactCaptureText(row.Title)
	if req.AIRationalePending {
		row.Payload["ai_rationale_pending"] = true
	}
	if req.JoinedCount > 0 {
		row.Payload["joined_count"] = req.JoinedCount
	}
	if req.Repeat != nil && (req.Repeat.Count > 1 || req.Repeat.Asks > 1) {
		repeat := map[string]any{
			"reason_key": observability.RedactCaptureText(req.Repeat.ReasonKey),
			"count":      req.Repeat.Count,
			"asks":       req.Repeat.Asks,
		}
		if len(req.Repeat.Subjects) > 0 {
			subjects := append([]string(nil), req.Repeat.Subjects...)
			for i := range subjects {
				subjects[i] = observability.RedactCaptureText(subjects[i])
			}
			repeat["subjects"] = subjects
		}
		if req.Repeat.SubjectsTruncated {
			repeat["subjects_truncated"] = true
		}
		if req.Repeat.SuppressedCount > 0 {
			repeat["suppressed_count"] = req.Repeat.SuppressedCount
		}
		row.Payload["repeat"] = repeat
	}
	if len(req.JoinedToolCallIDs) > 0 {
		row.Payload["joined_tool_call_ids"] = append([]string(nil), req.JoinedToolCallIDs...)
	}
	if chat := strings.TrimSpace(req.CoalesceChat); chat != "" {
		row.Payload["coalesce_chat"] = chat
	}
	if key := strings.TrimSpace(req.CoalesceGrantKey); key != "" {
		row.Payload["coalesce_grant_key"] = key
	}
	return nil
}

// seedPayloadConsequence copies the compiled plan's presentation band/code onto
// top-level payload keys so SetPendingJoined's MaxConsequence merges against the
// card's real current band rather than an empty string.
func seedPayloadConsequence(row *StoredCheckpoint, plan *ApprovalPlan) {
	if row == nil || plan == nil {
		return
	}
	if band := strings.TrimSpace(plan.Presentation.ConsequenceBand); band != "" {
		row.Payload["consequence_band"] = band
	}
	if code := strings.TrimSpace(plan.Presentation.ConsequenceCode); code != "" {
		row.Payload["consequence_code"] = code
	}
}
