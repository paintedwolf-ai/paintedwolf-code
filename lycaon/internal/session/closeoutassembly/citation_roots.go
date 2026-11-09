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
	// The session folder resolves by root ID, so a worker branch path in
	// WorkspacePath never displaces the active root's project directory.
	if active, activeErr := projectroot.ActiveRoot(refs, roots.ActiveRootID); activeErr == nil {
		roots.ProjectDir = strings.TrimSpace(active.Path)
		roots.ActiveRootID = active.ID
	} else if primary, primaryErr := projectroot.PrimaryRoot(refs); primaryErr == nil {
		roots.ProjectDir = strings.TrimSpace(primary.Path)
		roots.ActiveRootID = primary.ID
	}
	return roots, nil
}
