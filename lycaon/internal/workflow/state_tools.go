package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// StateToolDeps holds dependencies for state_* tools.
type StateToolDeps struct {
	Runs     *RunManager
	Sessions session.Store
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
		if err := requireSessionProject(ctx, deps.Sessions, tctx); err != nil {
			return "", err
		}
		active, err := deps.Runs.GetActive(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return "", ErrNoActiveRun
		}
		run, err := deps.Runs.Exit(ctx, tctx.SessionID, active.ID, active.Revision, stringArg(args["reason"]))
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(run)
		return string(raw), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("state_query", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if err := requireSessionProject(ctx, deps.Sessions, tctx); err != nil {
			return "", err
		}
		run, err := deps.Runs.GetActive(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if run == nil {
			return "{}", nil
		}
		vars, err := deps.Runs.ScaffoldVarsForSession(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if path := stringArg(args["path"]); path != "" {
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
		if err := requireSessionProject(ctx, deps.Sessions, tctx); err != nil {
			return "", err
		}
		path := stringArg(args["path"])
		if path == "" {
			return "", fmt.Errorf("path required")
		}
		if hostWorkflowStatePath(path) {
			return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "state_update", "field": "path", "reason": "host_managed_workflow_state", "path": path}}
		}
		value, ok := args["value"]
		if !ok {
			return "", fmt.Errorf("value required")
		}
		payload := struct {
			Path  string `json:"path"`
			Value any    `json:"value"`
		}{Path: path, Value: value}
		if _, replayed, replayErr := deps.Runs.replayCommandOperation(ctx, tctx.ToolCallID, "state_update", payload); replayErr != nil || replayed {
			if replayErr != nil {
				return "", replayErr
			}
			raw, _ := json.Marshal(map[string]any{"path": path, "value": value})
			return string(raw), nil
		}
		run, err := deps.Runs.GetActive(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if run == nil {
			return "", fmt.Errorf("no active workflow run")
		}
		rm := deps.Runs
		if rm == nil {
			return "", fmt.Errorf("workflow manager not configured")
		}
		unlockVars := rm.lockRunVars(run.ID)
		defer unlockVars()
		vars, err := rm.Store.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return "", err
		}
		vars = SetHostVar(vars, path, value)
		commandCtx := withWorkflowCommandOperation(WithExpectedRevision(ctx, run.Revision), tctx.ToolCallID)
		if err := rm.commitCommand(commandCtx, run, "state_update", payload, vars, nil, "", workflowWorkerMutation{}, nil); err != nil {
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
	case reviewRepairsKey, "fanout_plans", "fanout_coverage", "fanout_settled", "worker_cycle", "gates",
		"human_approval", "phase_skipped", "review_if_spawnable", "review_loop", "review_verdict", "review_questions",
		"user_feedback", "user_decision", "topology_stages", "topology_outputs", "orchestration_complete", "content_review",
		hostVarBaselinePosture, workflowdef.ScaffoldExecutionModeVar, workflowRequestFeedbackID, coordinatorAskVar, obligationsVarKey,
		"board", "child_run", "params", "intake", "options", HostAutoAdvancedFromKey,
		"workflow_compose_summary_id", "last_failed_leaves", "evidence_digest":
		return true
	default:
		return false
	}
}

func runStateStartTool(ctx context.Context, deps StateToolDeps, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := requireSessionProject(ctx, deps.Sessions, tctx); err != nil {
		return "", err
	}
	req := api.StartWorkflowRunRequest{
		OperationID:     strings.TrimSpace(tctx.ToolCallID),
		WorkflowID:      stringArg(args["workflow_id"]),
		WorkflowVersion: stringArg(args["workflow_version"]),
		BlueprintPath:   stringArg(args["blueprint_path"]),
		BlueprintTitle:  stringArg(args["blueprint_title"]),
	}
	if req.WorkflowID == "" || req.WorkflowVersion == "" {
		return "", fmt.Errorf("workflow_id and workflow_version required")
	}
	if err := deps.Runs.ValidateUserFacingStart(ctx, tctx.ActiveRootPath(), tctx.SessionID, req.WorkflowID, req.WorkflowVersion); err != nil {
		return "", err
	}
	run, err := deps.Runs.Start(ctx, tctx.SessionID, req)
	if err != nil {
		if errors.Is(err, ErrWorkflowStartRequiresHumanApproval) {
			_ = deps.Runs.NoteWorkflowStartProposal(ctx, tctx.SessionID, req.WorkflowID, req.WorkflowVersion)
			return "", &tools.ToolReject{
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

func requireSessionProject(ctx context.Context, store session.Store, tctx tools.ToolContext) error {
	if strings.TrimSpace(tctx.ActiveRootPath()) == "" {
		return fmt.Errorf("project_dir required")
	}
	if strings.TrimSpace(tctx.SessionID) == "" {
		return fmt.Errorf("session_id required")
	}
	if store == nil {
		return nil
	}
	sess, err := store.Get(ctx, tctx.SessionID)
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

func stringArg(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
