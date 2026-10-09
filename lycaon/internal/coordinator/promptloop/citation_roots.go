package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// citationRoots uses the session workspace when no roots are configured.
func (l *promptContext) citationRoots(ctx context.Context, sess *api.Session) evidence.CitationRoots {
	fallback := ""
	if sess != nil {
		fallback = strings.TrimSpace(sess.WorkspacePath)
	}
	if l == nil || l.Closeout.Deps.CitationRoots == nil {
		return evidence.CitationRoots{ProjectDir: fallback}
	}
	roots := l.Closeout.Deps.CitationRoots(ctx, sess)
	if len(roots.Roots) == 0 && strings.TrimSpace(roots.ProjectDir) == "" {
		roots.ProjectDir = fallback
	}
	return roots
}
