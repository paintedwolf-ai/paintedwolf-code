package policyindex

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetWorkspaceRoots supplies the same physical root mappings used by tool execution.
func (m *Service) SetWorkspaceRoots(resolve func(context.Context, *api.Session) ([]projectroot.RootRef, error)) {
	m.workspaceRoots = resolve
}

func (m *Service) sessionRoots(ctx context.Context, sess *api.Session) ([]projectroot.RootRef, error) {
	if m == nil || m.roots == nil || sess == nil {
		return nil, fmt.Errorf("policy workspace resolver unavailable")
	}
	if m.workspaceRoots != nil {
		return m.workspaceRoots(ctx, sess)
	}
	return m.roots.Roots(ctx, sess)
}

func (m *Service) sessionPath(ctx context.Context, sess *api.Session) (string, error) {
	roots, err := m.sessionRoots(ctx, sess)
	if err != nil || len(roots) == 0 {
		return "", err
	}
	root, err := projectroot.ActiveRoot(roots, sess.WorkspaceRootID)
	return root.Path, err
}
