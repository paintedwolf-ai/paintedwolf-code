package statetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	"github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"github.com/lycaon/lycaon/pkg/api"
)

// StateToolDeps holds dependencies for state_start.
type StateToolDeps struct {
	Resolver *catalog.Resolver
	Starts   *lifecycle.Admission
	Scaffold *inputs.Scaffold
	Sessions toolguard.Sessions
}

// RegisterStateTools registers state_start, which proposes a workflow run.
func RegisterStateTools(reg *tools.DefaultRegistry, deps StateToolDeps) error {
	if reg == nil || deps.Starts == nil {
		return fmt.Errorf("registry and workflow admission required")
	}
	return reg.Register("state_start", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return runStateStartTool(ctx, deps, args, tctx)
	})
}

func runStateStartTool(ctx context.Context, deps StateToolDeps, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := toolguard.RequireSessionProject(ctx, deps.Sessions, tctx); err != nil {
		return "", err
	}
	req := api.StartWorkflowRunRequest{
		OperationID:     strings.TrimSpace(tctx.Identity.ToolCallID),
		WorkflowID:      toolguard.StringArg(args["workflow_id"]),
		WorkflowVersion: toolguard.StringArg(args["workflow_version"]),
		BlueprintPath:   toolguard.StringArg(args["blueprint_path"]),
		BlueprintTitle:  toolguard.StringArg(args["blueprint_title"]),
	}
	if req.WorkflowID == "" || req.WorkflowVersion == "" {
		return "", fmt.Errorf("workflow_id and workflow_version required")
	}
	if err := deps.Resolver.ValidateUserFacingStart(ctx, tctx.ActiveRootPath(), tctx.Identity.SessionID, req.WorkflowID, req.WorkflowVersion); err != nil {
		return "", err
	}
	run, err := deps.Starts.Start(ctx, tctx.Identity.SessionID, req)
	if err != nil {
		if errors.Is(err, runstate.ErrWorkflowStartRequiresHumanApproval) {
			_ = deps.Scaffold.NoteWorkflowStartProposal(ctx, tctx.Identity.SessionID, req.WorkflowID, req.WorkflowVersion)
			return "", &toolrejection.ToolReject{
				Code: "WORKFLOW_START_REQUIRES_HUMAN_APPROVAL",
				Data: map[string]any{"workflow_id": req.WorkflowID},
			}
		}
		return "", err
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return "", fmt.Errorf("marshal run: %w", err)
	}
	return string(raw), nil
}
