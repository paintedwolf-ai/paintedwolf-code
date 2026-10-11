package verification

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) WorkerRevision(ctx context.Context, task *api.WorkerTask) workercompletion.SourceRevision {
	if task == nil {
		return workercompletion.SourceRevision{}
	}
	revision, root := m.Revision(ctx, task.WorkspaceRoot)
	return workercompletion.SourceRevision{Revision: revision, RootDigest: root}
}
