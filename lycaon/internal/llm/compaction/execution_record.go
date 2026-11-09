package compaction

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

func continuationExecution(result *api.ToolResult) *messageview.ContinuationExecution {
	if result == nil || result.ToolCallID == "" {
		return nil
	}
	out := &messageview.ContinuationExecution{CallID: result.ToolCallID, Outcome: string(result.Outcome)}
	if receipt := result.Invocation; receipt != nil && receipt.ToolCallID == result.ToolCallID {
		out.ReceiptID, out.Status, out.Invoked = receipt.ID, string(receipt.Status), receipt.Invoked
		out.SourceRevision, out.SourceVerdict = receipt.SourceRevision, receipt.SourceVerdict
		if receipt.Failure != nil {
			out.StopCode = receipt.Failure.Code
		}
	}
	if process := result.Process; process != nil {
		out.ProcessHandle = process.Handle
	}
	if program := result.CommandProgram; program != nil && out.ReceiptID != "" && program.ParentReceiptID == out.ReceiptID {
		out.ProgramID, out.ProgramState, out.ProgramSource = program.ID, program.State, program.SourceRef
		if len(program.Activities) > 0 {
			out.Effects = &messageview.ContinuationEffects{}
		}
		for _, activity := range program.Activities {
			out.Effects.Completed += activity.CompletedEffects
			out.Effects.Uncertain += activity.UncertainEffects
			if activity.JobHandle != "" {
				out.BackgroundHandles = append(out.BackgroundHandles, activity.JobHandle)
			}
		}
		if program.StatusCode != nil {
			code := *program.StatusCode
			out.ExitCode = &code
		}
		if program.StopCode != "" {
			out.StopCode = program.StopCode
		}
	}
	return out
}

// Each fragment retains its source identity even across summary pages.
func summaryFragment(row ContextMessage, content string, part, parts int) string {
	type callIdentity struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	calls := make([]callIdentity, 0, len(row.ToolCalls))
	for _, call := range row.ToolCalls {
		calls = append(calls, callIdentity{ID: call.ID, Name: call.Name})
	}
	raw, _ := json.Marshal(struct {
		MessageID string         `json:"message_id"`
		Ord       int64          `json:"ordinal"`
		Part      int            `json:"part"`
		Parts     int            `json:"parts"`
		Calls     []callIdentity `json:"calls,omitempty"`
		Fragment  string         `json:"source_fragment"`
	}{row.ID, row.Ord, part, parts, calls, content})
	return string(raw)
}
