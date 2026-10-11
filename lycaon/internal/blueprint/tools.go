package blueprint

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// RegisterPlanTools registers plan_* agent tools.
func RegisterPlanTools(reg *tools.DefaultRegistry, mgr *Manager) error {
	if reg == nil || mgr == nil {
		return fmt.Errorf("registry and blueprint manager required")
	}
	if err := reg.Register("plan_append_review_evidence", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if strings.TrimSpace(tctx.ActiveRootPath()) == "" {
			return "", fmt.Errorf("project_dir required")
		}
		path, _ := args["blueprint_path"].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			return "", fmt.Errorf("blueprint_path required")
		}
		evidence, _ := args["evidence"].(string)
		if strings.TrimSpace(evidence) == "" {
			return "", fmt.Errorf("evidence required")
		}
		blueprintDoc, err := mgr.Get(ctx, tctx.Identity.ProjectID, path)
		if err != nil {
			return "", err
		}
		if blueprintDoc.ProjectID != tctx.Identity.ProjectID {
			return "", fmt.Errorf("blueprint not in session project")
		}
		if err := mgr.AppendCriticEvidence(ctx, tctx.Identity.ProjectID, path, []byte(evidence)); err != nil {
			return "", err
		}
		raw, _ := json.Marshal(map[string]any{"blueprint_path": path, "ok": true})
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}
