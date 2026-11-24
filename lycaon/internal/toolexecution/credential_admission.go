package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// admitCredentialFiles waits for current project evidence before either entry
// dispatches. Program output has no reliable file attribution, so it uses this
// shared index. The caller's own cancellation returns as an error.
func admitCredentialFiles(ctx context.Context, tool string, tc tools.ToolContext) (*toolrejection.ToolReject, error) {
	if tc.Effects.CredentialFiles == nil {
		return nil, nil
	}
	err := tc.Effects.CredentialFiles.Await(ctx, tools.CredentialWait{ProjectID: tc.Identity.ProjectID, SessionID: tc.Identity.SessionID})
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
