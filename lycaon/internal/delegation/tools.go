package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RegisterDelegationTools registers delegate_* coordinator tools.
func RegisterDelegationTools(reg *tools.DefaultRegistry, mgr *Manager) error {
	if reg == nil || mgr == nil {
		return fmt.Errorf("registry and manager required")
	}
	if err := RegisterDispatchTool(reg, mgr); err != nil {
		return err
	}

	if err := reg.Register("delegate_init", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if strings.TrimSpace(tctx.ActiveRootPath()) == "" {
			return "", fmt.Errorf("project_dir required")
		}
		if strings.TrimSpace(tctx.SessionID) == "" {
			return "", fmt.Errorf("session_id required")
		}
		task, _ := args["task"].(string)
		if strings.TrimSpace(task) == "" {
			return "", fmt.Errorf("task required")
		}
		req := api.CreateDelegationRequest{
			ProjectID:       tctx.ProjectID,
			Task:            strings.TrimSpace(task),
			BlueprintPath:   stringArg(args["blueprint_path"]),
			WorkflowID:      stringArg(args["workflow_id"]),
			WorkflowVersion: stringArg(args["workflow_version"]),
			WorkflowRunID:   stringArg(args["workflow_run_id"]),
		}
		if s, ok := args["strategy"].(string); ok && strings.TrimSpace(s) != "" {
			req.Strategy = api.HuntStrategy(strings.TrimSpace(s))
		}
		out, err := mgr.Init(ctx, tctx.SessionID, req)
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(out)
		return string(raw), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("delegate_status", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		delegationID := stringArg(args["delegation_id"])
		if delegationID == "" {
			var ok bool
			delegationID, ok = mgr.Store.DelegationBySessionID(tctx.SessionID)
			if !ok {
				return "", fmt.Errorf("delegation not found for session")
			}
		}
		out, err := mgr.GetStatus(ctx, delegationID)
		if err != nil {
			return "", err
		}
		if out != nil && out.CoordinatorSessionID == tctx.SessionID {
			tctx.SetDisplaySubject(out.Task)
		}
		legs, _ := mgr.Store.ListLegs(ctx, delegationID)
		payload := map[string]any{"delegation": out, "legs": legs}
		raw, _ := json.Marshal(payload)
		return string(raw), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("delegate_decompose", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		blueprintPath := stringArg(args["blueprint_path"])
		if blueprintPath == "" {
			return "", fmt.Errorf("blueprint_path required")
		}
		if mgr.Plans == nil {
			return "", fmt.Errorf("plan reader not configured")
		}
		planDoc, err := mgr.Plans.Get(ctx, tctx.ProjectID, blueprintPath)
		if err != nil {
			return "", err
		}
		legs := LegsFromPlan(planDoc, blueprint.ExtractTasks(planDoc.Content))
		delegationID, ok := mgr.Store.DelegationBySessionID(tctx.SessionID)
		if !ok {
			raw, _ := json.Marshal(map[string]any{"legs": legs, "persisted": false})
			return string(raw), nil
		}
		for _, leg := range legs {
			if err := mgr.Store.AddLeg(ctx, delegationID, leg); err != nil {
				return "", err
			}
		}
		stored, err := mgr.Store.ListLegs(ctx, delegationID)
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(map[string]any{"delegation_id": delegationID, "legs": stored, "persisted": true})
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}

func stringArg(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
