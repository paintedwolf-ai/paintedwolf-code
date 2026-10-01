package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
)

// RegisterPersistTool registers workflow_persist for coordinator sessions.
func RegisterPersistTool(reg *tools.DefaultRegistry, persister *Persister) error {
	if reg == nil || persister == nil {
		return fmt.Errorf("registry and persister required")
	}
	if err := reg.Register("workflow_persist", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !isCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("workflow_persist requires coordinator role")
		}
		workflowID, _ := args["workflow_id"].(string)
		version, _ := args["version"].(string)
		confirm, _ := args["confirm"].(bool)
		if !confirm {
			return "", &PersistNotConfirmedError{}
		}
		trigger, _ := args["trigger"].(string)
		result, err := persister.Persist(ctx, PersistRequest{
			SessionID:  tctx.SessionID,
			ProjectDir: tctx.ActiveRootPath(),
			WorkflowID: workflowID,
			Version:    version,
			Confirm:    true,
			Trigger:    trigger,
			CreatedBy:  ComposeActorCoordinator,
		})
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(result)
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}
