package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// OverlaySourceProof binds worker receipts to the overlay's current source content.
func OverlaySourceProof(ctx context.Context, task *api.WorkerTask, childMessages []api.Message, declaredCommand string, current workercompletion.SourceRevision) workercompletion.WorkerCompletionProof {
	if task == nil {
		return workercompletion.WorkerCompletionProof{}
	}
	proof := workercompletion.BuildWorkerCompletionProof(
		childMessages, OverlayWorkspaceChangedPaths(ctx, task, nil),
		current,
	)
	declaredCommand = strings.TrimSpace(declaredCommand)
	proof.DeclaredCommand = &declaredCommand
	return proof
}

// WorkerVerificationRevision returns the current worker source identity.
