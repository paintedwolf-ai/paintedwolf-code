package toolguard

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
}

func RequireSessionProject(ctx context.Context, store Sessions, tctx tools.ToolContext) error {
	if strings.TrimSpace(tctx.ActiveRootPath()) == "" {
		return fmt.Errorf("project_dir required")
	}
	if strings.TrimSpace(tctx.Identity.SessionID) == "" {
		return fmt.Errorf("session_id required")
	}
	if store == nil {
		return nil
	}
	sess, err := store.Get(ctx, tctx.Identity.SessionID)
	if err != nil {
		return err
	}
	if sess == nil {
		return fmt.Errorf("session not found")
	}
	if strings.TrimSpace(sess.WorkspacePath) != "" && sess.WorkspacePath != tctx.ActiveRootPath() {
		return fmt.Errorf("project_dir mismatch")
	}
	return nil
}
func IsCoordinatorAgent(agent string) bool {
	return strings.TrimSpace(agent) == orchestration.ProfileCoordinator
}

func StringArg(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
