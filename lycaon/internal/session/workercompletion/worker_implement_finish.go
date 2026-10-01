package workercompletion

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

const workerImplementNoArtifactCode = "WORKER_IMPLEMENT_NO_ARTIFACT"

// ObserveImplementerFinishWithoutWrite publishes implementer finish facts.
func ObserveImplementerFinishWithoutWrite(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	lastAssistant string,
	projectDir string,
	wc WorkspaceChangeChecker,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || !sess.IsWorkerChild() || !isImplementerAgent(sess.AgentType) {
		return
	}
	if strings.TrimSpace(lastAssistant) == "" {
		return
	}
	proof := childHadImplementerArtifact(ctx, projectDir, history, wc)
	gc.AgentIsImplementer = true
	gc.WorkerArtifactPresent = proof == artifactPresent
	gc.WorkerArtifactMeasured = proof != artifactUndetermined
	gc.WorkerAttemptedMutation = childAttemptedFileContentMutation(history)
	// Rejection requires measured absence and an attempted mutation.
	if proof == artifactAbsent && gc.WorkerAttemptedMutation {
		gc.PutRejectData(workerImplementNoArtifactCode, nil)
	}
}

// childAttemptedFileContentMutation checks structured tool calls.
func childAttemptedFileContentMutation(msgs []api.Message) bool {
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			name := strings.TrimSpace(tc.Name)
			if toolcontract.MutatesContent(name) || name == "restore_version" {
				return true
			}
		}
	}
	return false
}
