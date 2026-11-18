package worker

import (
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

func enqueueScope(projectID string, task *api.WorkerTask) project.ProjectScope {
	if task == nil {
		return project.ProjectScope{}
	}
	pid := strings.TrimSpace(task.ProjectID)
	if pid == "" {
		pid = strings.TrimSpace(projectID)
	}
	path := strings.TrimSpace(task.WorkspacePath)
	return project.ProjectScope{
		ProjectID:       pid,
		WorkspaceRootID: task.WorkspaceRootID,
		WorkspacePath:   path,
		HasRoots:        path != "",
	}
}
