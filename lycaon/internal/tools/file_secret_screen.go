package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// screenFileSecrets checks managed-secret references before a file tool commits bytes.
func (e *DefaultToolExecutor) screenFileSecrets(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc ToolContext,
) error {
	if e == nil {
		return nil
	}
	if !tc.Invocation.Contract.SecretReferenceSurface.IsFile() {
		return nil
	}
	if resolveRef, ok := args["resolve_secret_references"].(bool); ok && !resolveRef {
		return nil
	}
	if tc.Secrets == nil {
		return nil
	}
	known, err := tc.Secrets.Matches(e.secretMatcher, func(string) bool { return true })
	if err != nil {
		return argvSecretFaultReject(ctx, e, tool, tc, secretmatch.SurfaceFile, secretmatch.Match{}, secretmatch.NewAskFault(secretmatch.FaultStageScreenUnwired, err))
	}
	if len(known) == 0 {
		return nil
	}

	filePath, _ := args["path"].(string)
	finding := argvSecretFinding(secretmatch.SurfaceFile, tool, tc, known[0], known)
	finding.DestinationID, finding.DestinationLabel = fileSecretDestination(filePath)
	finding.SecretNames = secretmatch.ManagedNames(known)
	finding.ChatGenerated = tc.Secrets.GeneratedForChat(known)
	finding.RecipientsLocal = true

	attribution := secretmatch.AskAttributionFrom(ctx)
	attribution.ProjectID = tc.ProjectID
	attribution.SessionID = tc.SessionID
	attribution.ToolCallID = tc.ToolCallID
	ctx = secretmatch.WithAskAttribution(ctx, attribution)

	return e.resolveArgumentSecretFinding(ctx, tool, tc, secretmatch.SurfaceFile, known[0], finding)
}

// fileSecretDestination is the recipient identity and label for a file write.
func fileSecretDestination(path string) (id, label string) {
	return "file:" + path, "Local file: " + path
}
