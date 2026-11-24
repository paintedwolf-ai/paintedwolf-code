package toolexecution

import (
	"context"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// admitCredentialFiles waits for current project evidence before either entry
// dispatches. Program output has no reliable file attribution, so it uses this
// shared index. The caller's own cancellation returns as an error.
func admitCredentialFiles(ctx context.Context, tool string, programEntry bool, tc tools.ToolContext) (*toolrejection.ToolReject, error) {
	enterAt := time.Now().UTC()
	dispatchedAt := awaitstore.DispatchTimeFrom(ctx)
	var preAdmissionMS int64
	if !dispatchedAt.IsZero() {
		preAdmissionMS = enterAt.Sub(dispatchedAt).Milliseconds()
	}
	program := strings.TrimSpace(tc.Invocation.EntryTool)
	if program == "" && programEntry {
		program = tool
	}
	receiptID := strings.TrimSpace(tc.Invocation.ID)
	awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
		SessionID:   tc.Identity.SessionID,
		ToolCallID:  tc.Identity.ToolCallID,
		Handle:      receiptID,
		Outcome:     program,
		Tool:        tool,
		Phase:       "tool_admission_enter",
		DurationMS:  preAdmissionMS,
		RecordedAt:  enterAt,
		ReferenceAt: dispatchedAt,
	})
	if tc.Effects.CredentialFiles == nil {
		awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
			SessionID:  tc.Identity.SessionID,
			ToolCallID: tc.Identity.ToolCallID,
			Handle:     receiptID,
			Outcome:    program,
			Tool:       tool,
			Phase:      "tool_admission_exit",
			Status:     "skipped",
			RecordedAt: enterAt,
		})
		return nil, nil
	}
	err := tc.Effects.CredentialFiles.Await(ctx, tools.CredentialWait{
		ProjectID: tc.Identity.ProjectID,
		SessionID: tc.Identity.SessionID,
	})
	exitAt := time.Now().UTC()
	status := "admitted"
	if err != nil {
		if ctx.Err() != nil {
			status = "canceled"
		} else {
			status = "rejected"
		}
	}
	awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
		SessionID:  tc.Identity.SessionID,
		ToolCallID: tc.Identity.ToolCallID,
		Handle:     receiptID,
		Outcome:    program,
		Tool:       tool,
		Phase:      "tool_admission_exit",
		Status:     status,
		DurationMS: exitAt.Sub(enterAt).Milliseconds(),
		RecordedAt: exitAt,
		Failed:     err != nil,
	})
	if err == nil {
		return nil, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return &toolrejection.ToolReject{
		Code: toolrejection.OutboundSecretScreenFailedCode,
		Data: map[string]any{
			"surface":     tool,
			"fault_stage": secretmatch.FaultStageManagedSecretStore,
			"reason":      err.Error(),
		},
	}, nil
}
