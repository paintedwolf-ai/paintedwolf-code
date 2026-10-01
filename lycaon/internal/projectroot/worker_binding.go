package projectroot

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// RootRefByID returns the root with the given id, when present.
func RootRefByID(roots []RootRef, rootID string) (RootRef, bool) {
	for _, r := range roots {
		if r.ID == rootID {
			return r, true
		}
	}
	return RootRef{}, false
}

// WorkerTaskBindsRoot reports whether a task needs the root.
func WorkerTaskBindsRoot(task api.WorkerTask, detached RootRef, projectRoots []RootRef) bool {
	if len(projectRoots) > 1 {
		for _, r := range projectRoots {
			if r.ID == detached.ID {
				return true
			}
		}
		return false
	}
	if strings.TrimSpace(task.WorkspaceRootID) == detached.ID {
		return true
	}
	if strings.TrimSpace(task.WorkspacePath) == detached.Path {
		return true
	}
	for _, r := range projectRoots {
		if r.ID == detached.ID || r.Path == detached.Path {
			return true
		}
	}
	return false
}
