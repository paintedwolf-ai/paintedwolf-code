package statetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// StateToolDeps holds dependencies for state_* tools.
type StateToolDeps struct {
	Runs     runstate.RunsRepository
	Vars     *runstate.Variables
	Journal  *runstate.Journal
	Resolver *catalog.Resolver
	Starts   *lifecycle.Admission
	Controls *lifecycle.Commands
	Scaffold *inputs.Scaffold
	Sessions toolguard.Sessions
}

// RegisterStateTools registers core state_* workflow tools.
func RegisterStateTools(reg *tools.DefaultRegistry, deps StateToolDeps) error {
	if reg == nil || deps.Runs == nil {
		return fmt.Errorf("registry and workflow manager required")
	}

	if err := reg.Register("state_start", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return runStateStartTool(ctx, deps, args, tctx)
	}); err != nil {
		return err
	}

	if err := reg.Register("state_close", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if err := toolguard.RequireSessionProject(ctx, deps.Sessions, tctx); err != nil {
			return "", err
		}
		active, err := deps.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return "", runstate.ErrNoActiveRun
		}
		run, err := deps.Controls.Exit(ctx, tctx.Identity.SessionID, active.ID, active.Revision, toolguard.StringArg(args["reason"]))
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(run)
		return string(raw), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("state_query", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if err := toolguard.RequireSessionProject(ctx, deps.Sessions, tctx); err != nil {
			return "", err
		}
		run, err := deps.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
		if err != nil {
			return "", err
		}
		if run == nil {
			return "{}", nil
		}
		vars, err := deps.Runs.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return "", err
		}
		if path := toolguard.StringArg(args["path"]); path != "" {
			val, ok := conditions.DotPathGet(vars, path)
			if !ok {
				return "", fmt.Errorf("path not found: %s", path)
			}
			raw, _ := json.Marshal(map[string]any{"path": path, "value": val})
			return string(raw), nil
		}
		payload := map[string]any{"run": run, "vars": vars}
		raw, _ := json.Marshal(payload)
		return string(raw), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("state_update", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if err := toolguard.RequireSessionProject(ctx, deps.Sessions, tctx); err != nil {
			return "", err
		}
		path := toolguard.StringArg(args["path"])
		if path == "" {
			return "", fmt.Errorf("path required")
		}
		if hostWorkflowStatePath(path) {
			return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "state_update", "field": "path", "reason": "host_managed_workflow_state", "path": path}}
		}
		value, ok := args["value"]
		if !ok {
			return "", fmt.Errorf("value required")
		}
		payload := struct {
			Path  string `json:"path"`
			Value any    `json:"value"`
		}{Path: path, Value: value}
		if _, replayed, replayErr := deps.Journal.ReplayOperation(ctx, tctx.Identity.ToolCallID, "state_update", payload); replayErr != nil || replayed {
			if replayErr != nil {
				return "", replayErr
			}
			raw, _ := json.Marshal(map[string]any{"path": path, "value": value})
			return string(raw), nil
		}
		run, err := deps.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
		if err != nil {
			return "", err
		}
		if run == nil {
			return "", fmt.Errorf("no active workflow run")
		}
		unlockVars := deps.Vars.Lock(run.ID)
		defer unlockVars()
		vars, err := deps.Runs.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return "", err
		}
		vars = runstate.SetHostVar(vars, path, value)
		commandCtx := runstate.WithCommandOperation(runstate.WithExpectedRevision(ctx, run.Revision), tctx.Identity.ToolCallID)
		if err := deps.Journal.Commit(commandCtx, run, "state_update", payload, vars, nil, "", runstate.WorkerMutation{}, nil); err != nil {
			return "", err
		}
		raw, _ := json.Marshal(map[string]any{"path": path, "value": value})
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}

func hostWorkflowStatePath(path string) bool {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "hitl_consulted:") {
		return true
	}
	root, _, _ := strings.Cut(path, ".")
	switch root {
	case runstate.ReviewRepairsKey, "fanout_plans", "fanout_coverage", "fanout_settled", "worker_cycle", "gates",
		"human_approval", "phase_skipped", "review_if_spawnable", "review_loop", "review_verdict", "review_questions",
		"user_feedback", "user_decision", "topology_stages", "topology_outputs", "orchestration_complete", "content_review",
		runstate.BaselinePostureKey, workflowdef.ScaffoldExecutionModeVar, runstate.WorkflowRequestFeedbackID, runstate.CoordinatorAskVar, runstate.ObligationsVarKey,
		"board", "child_run", "params", "intake", "options", workflowphases.HostAutoAdvancedFromKey,
		"workflow_compose_summary_id", "last_failed_leaves", "evidence_digest":
		return true
	default:
		return false
	}
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
