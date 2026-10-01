package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectScope binds child-table writes to project identity and optional active root path.
type ProjectScope struct {
	ProjectID       string
	WorkspaceRootID string
	WorkspacePath   string
	HasRoots        bool
}

// RootsReader loads project metadata for scope resolution.
type RootsReader interface {
	Get(ctx context.Context, projectID string) (*Project, error)
}

// ScopeForSession resolves project_id, active root id, and workspace path from a session.
func ScopeForSession(ctx context.Context, sess *api.Session, roots RootsReader) (ProjectScope, error) {
	if sess == nil {
		return ProjectScope{}, fmt.Errorf("session required")
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	if projectID == "" {
		return ProjectScope{}, fmt.Errorf("project_id required")
	}
	if roots == nil {
		return ProjectScope{ProjectID: projectID}, nil
	}
	p, err := roots.Get(ctx, projectID)
	if err != nil {
		return ProjectScope{}, err
	}
	refs := RootRefsFrom(p)
	if len(refs) == 0 {
		return ProjectScope{ProjectID: projectID, HasRoots: false}, nil
	}
	active, err := projectroot.ActiveRoot(refs, sess.WorkspaceRootID)
	if err != nil {
		return ProjectScope{}, err
	}
	return ProjectScope{
		ProjectID:       projectID,
		WorkspaceRootID: active.ID,
		WorkspacePath:   active.Path,
		HasRoots:        true,
	}, nil
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
