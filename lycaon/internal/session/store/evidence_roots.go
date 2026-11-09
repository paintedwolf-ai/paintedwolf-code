package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// evidenceCitationRoots resolves the session's folder roots for evidence
// identity and citation keys. Roots come from the session's project; the
// active root is the session's workspace_root_id, falling back to the
// project's primary root. ProjectDir keeps the legacy single-directory
// anchor (primary root path) for tokens that resolve under no attached root.
func evidenceCitationRoots(ctx context.Context, q *db.Queries, sessionID string) (evidence.CitationRoots, error) {
	sess, err := q.GetSession(ctx, sessionID)
	if err != nil {
		return evidence.CitationRoots{}, fmt.Errorf("resolve evidence roots: %w", err)
	}
	if strings.TrimSpace(sess.ProjectID) == "" {
		return evidence.CitationRoots{}, nil
	}
	rows, err := q.ListProjectRoots(ctx, sess.ProjectID)
	if err != nil {
		return evidence.CitationRoots{}, fmt.Errorf("resolve evidence roots: %w", err)
	}
	refs := projectRootRefs(rows)
	out := evidence.CitationRoots{Roots: refs, ProjectDir: primaryRootPath(refs)}
	out.ActiveRootID = strings.TrimSpace(sess.WorkspaceRootID.String)
	if out.ActiveRootID == "" {
		if primary, err := projectroot.ActiveRoot(refs, ""); err == nil {
			out.ActiveRootID = primary.ID
		}
	}
	return out, nil
}

// projectRootRefs converts persisted project roots to path-resolution refs,
// primary first.
func projectRootRefs(rows []db.ProjectRoots) []projectroot.RootRef {
	refs := make([]projectroot.RootRef, 0, len(rows))
	for _, r := range rows {
		if r.IsPrimary != 0 {
			refs = append(refs, projectroot.RootRef{ID: r.ID, Label: r.Label, Path: r.Path, IsPrimary: true})
		}
	}
	for _, r := range rows {
		if r.IsPrimary == 0 {
			refs = append(refs, projectroot.RootRef{ID: r.ID, Label: r.Label, Path: r.Path})
		}
	}
	return refs
}

func primaryRootPath(refs []projectroot.RootRef) string {
	if primary, err := projectroot.ActiveRoot(refs, ""); err == nil {
		return primary.Path
	}
	return ""
}
