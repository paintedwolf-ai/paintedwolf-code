package toolexecution

import (
	"context"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// admitCredentialFiles reconciles project evidence before either entry dispatches.
// Program output has no reliable file attribution, so it uses this shared index.
func admitCredentialFiles(ctx context.Context, tool string, tc tools.ToolContext) *toolrejection.ToolReject {
	enterAt := time.Now().UTC()
	dispatchedAt := awaitstore.DispatchTimeFrom(ctx)
	var preAdmissionMS int64
	if !dispatchedAt.IsZero() {
		preAdmissionMS = enterAt.Sub(dispatchedAt).Milliseconds()
	}
	awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
		SessionID:   tc.Identity.SessionID,
		ToolCallID:  tc.Identity.ToolCallID,
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
			Tool:       tool,
			Phase:      "tool_admission_exit",
			RecordedAt: enterAt,
		})
		return nil
	}
	err := tc.Effects.CredentialFiles.EnsureProject(ctx, tc.Identity.ProjectID)
	exitAt := time.Now().UTC()
	awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
		SessionID:  tc.Identity.SessionID,
		ToolCallID: tc.Identity.ToolCallID,
		Tool:       tool,
		Phase:      "tool_admission_exit",
		DurationMS: exitAt.Sub(enterAt).Milliseconds(),
		RecordedAt: exitAt,
		Failed:     err != nil,
	})
	if err != nil {
		return &toolrejection.ToolReject{
			Code: toolrejection.OutboundSecretScreenFailedCode,
			Data: map[string]any{
				"surface":     tool,
				"fault_stage": secretmatch.FaultStageManagedSecretStore,
				"reason":      err.Error(),
			},
		}
	}
	return nil
}
