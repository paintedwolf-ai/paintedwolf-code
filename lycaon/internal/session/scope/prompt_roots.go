package scope

import (
	"context"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) PromptRootRows(ctx context.Context, sess *api.Session) ([]map[string]any, int, string) {
	if m == nil || sess == nil {
		return nil, 0, ""
	}
	refs, err := m.Roots(ctx, sess)
	if err != nil {
		return nil, 0, ""
	}
	rows := make([]map[string]any, 0, len(refs))
	for _, r := range refs {
		rows = append(rows, map[string]any{
			"label":      r.Label,
			"path":       r.Path,
			"is_primary": r.IsPrimary,
		})
	}
	activePath := ""
	if r, err := projectroot.ActiveRoot(refs, sess.WorkspaceRootID); err == nil {
		activePath = r.Path
	} else if r, err := projectroot.PrimaryRoot(refs); err == nil {
		activePath = r.Path
	}
	return rows, len(refs), activePath
}
