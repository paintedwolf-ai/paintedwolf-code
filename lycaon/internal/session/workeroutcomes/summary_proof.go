package workeroutcomes

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Summaries) Proof(ctx context.Context, task *api.WorkerTask, messages []api.Message, projectDir string) (workercompletion.WorkerCompletionProof, workercompletion.WorkspaceChangeObservation) {
	changed, changeErr := m.inspectChanges(ctx, task, nil)
	observation := workercompletion.WorkspaceChangeObservation{Paths: changed, Err: changeErr}
	var source workercompletion.SourceRevision
	if task != nil && len(changed) > 0 {
		source.Revision, source.RootDigest = m.verification.Revision(ctx, task.WorkspaceRoot)
	}
	proof := workercompletion.BuildWorkerCompletionProof(messages, changed, source)
	command := strings.TrimSpace(m.verification.SourceVerifyCommand(ctx, projectDir))
	proof.DeclaredCommand = &command
	if changeErr != nil {
		slog.WarnContext(ctx, "worker baseline unavailable", "error", changeErr)
		proof.WorkspaceDirty = true
		proof.Verification = &verification.Assessment{Method: verification.Blocked, Reason: "Workspace changes could not be determined from the worker baseline."}
		return proof, observation
	}
	if task == nil {
		return proof, observation
	}
	runs, err := m.verification.WorkerSourceRuns(ctx, task.ChildSessionID)
	if err != nil {
		slog.WarnContext(ctx, "worker validation evidence unavailable", "session_id", task.ChildSessionID, "error", err)
		proof.Verification = &verification.Assessment{Method: verification.Blocked, Reason: "Validation results could not be loaded; their outcome is unavailable."}
		return proof, observation
	}
	return proof.WithSourceRuns(runs), observation
}
