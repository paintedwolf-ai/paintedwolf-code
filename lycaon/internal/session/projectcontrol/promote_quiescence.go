package projectcontrol

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectPromoteQuiescent reports whether every session on the project has finished
// its coordinator round and no root-bound workers or overlays remain in flight.
func (m *Service) ProjectPromoteQuiescent(ctx context.Context, projectID string) (bool, error) {
	if m == nil {
		return false, fmt.Errorf("session manager not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, fmt.Errorf("project_id required")
	}
	deps, err := m.ProjectDependents(ctx, projectID)
	if err != nil {
		return false, err
	}
	if deps.HasAny() {
		return false, nil
	}
	if m.store == nil {
		return true, nil
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return false, err
	}
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		if sess.Status == api.SessionStatusBusy {
			return false, nil
		}
		if !m.admission.RoundComplete(ctx, sess.ID) {
			return false, nil
		}
	}
	return true, nil
}
