package closeoutassembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) CitationRoots(ctx context.Context, sess *api.Session) (evidence.CitationRoots, error) {
	refs, err := m.workspace.Roots(ctx, sess)
	if err != nil {
		return evidence.CitationRoots{}, err
	}
	roots := evidence.CitationRoots{Roots: refs}
	if sess != nil {
		roots.ActiveRootID = strings.TrimSpace(sess.WorkspaceRootID)
		roots.ProjectDir = strings.TrimSpace(sess.WorkspacePath)
	}
	if primary, primaryErr := projectroot.PrimaryRoot(refs); primaryErr == nil {
		roots.ProjectDir = strings.TrimSpace(primary.Path)
		if roots.ActiveRootID == "" {
			roots.ActiveRootID = primary.ID
		}
	}
	return roots, nil
}
