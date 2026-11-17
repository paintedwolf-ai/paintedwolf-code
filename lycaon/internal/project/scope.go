package project

import (
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// ProjectScope binds child-table writes to project identity and optional active root path.
type ProjectScope struct {
	ProjectID       string
	WorkspaceRootID string
	WorkspacePath   string
	HasRoots        bool
}

// ScopeFromToolContext builds scope from tool invocation context (roots already resolved).
func ScopeFromToolContext(projectID, activeRootID string, roots []projectroot.RootRef, activePath string) ProjectScope {
	projectID = strings.TrimSpace(projectID)
	if len(roots) == 0 {
		path := strings.TrimSpace(activePath)
		if path == "" {
			return ProjectScope{ProjectID: projectID, HasRoots: false}
		}
		return ProjectScope{
			ProjectID:     projectID,
			WorkspacePath: path,
			HasRoots:      true,
		}
	}
	rootID := strings.TrimSpace(activeRootID)
	path := strings.TrimSpace(activePath)
	if path == "" {
		if r, err := projectroot.ActiveRoot(roots, activeRootID); err == nil {
			rootID = r.ID
			path = r.Path
		}
	} else if rootID == "" {
		if r, err := projectroot.ActiveRoot(roots, activeRootID); err == nil {
			rootID = r.ID
		}
	}
	return ProjectScope{
		ProjectID:       projectID,
		WorkspaceRootID: rootID,
		WorkspacePath:   path,
		HasRoots:        true,
	}
}
