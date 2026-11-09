package scope

import (
	"context"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptRootRows builds the workspace root rows used by prompt assembly: rows
// carry label, path, is_primary, and is_active (matched by the session's
// active root ID, so a worker branch path in WorkspacePath still marks the
// active root), capped at the prompt collection limit. The returns are the
// rows, the root count, the active root path, and the omitted row count.
func (m *Service) PromptRootRows(ctx context.Context, sess *api.Session) ([]map[string]any, int, string, int) {
	if m == nil || sess == nil {
		return nil, 0, "", 0
	}
	refs, err := m.Roots(ctx, sess)
	if err != nil {
		return nil, 0, "", 0
	}
	rows, omitted := prompts.WorkspaceRootRows(refs, sess.WorkspaceRootID)
	activePath := ""
	if r, err := projectroot.ActiveRoot(refs, sess.WorkspaceRootID); err == nil {
		activePath = r.Path
	} else if r, err := projectroot.PrimaryRoot(refs); err == nil {
		activePath = r.Path
	}
	return rows, len(refs), activePath, omitted
}
