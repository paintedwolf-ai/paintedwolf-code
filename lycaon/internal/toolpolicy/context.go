package toolpolicy

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/pkg/api"
)

// BuildEvalContext assembles rule evaluation context for a tool invoke.
func BuildEvalContext(ctx context.Context, deps EngineDeps, sess *api.Session, toolName string, args map[string]any) rules.EvalContext {
	if frame, ok := coordinatorTurnFrameFromContext(ctx); ok {
		return buildEvalContextFromCoordinatorFrame(ctx, deps, sess, toolName, args, frame)
	}
	return buildEvalContextFromLiveState(ctx, deps, sess, toolName, args)
}

func buildEvalContextFromLiveState(ctx context.Context, deps EngineDeps, sess *api.Session, toolName string, args map[string]any) rules.EvalContext {
	phase := ""
	allowed := []string(nil)
	manifestRules := []string(nil)
	if deps.Workflows != nil && sess != nil {
		w := deps.Workflows
		phase = w.Policy.CurrentPhase(ctx, sess.ID)
		allowed = w.Policy.AllowedAgents(ctx, sess.ID)
		if manifest, ok := w.Policy.ActiveManifest(ctx, sess.ID); ok {
			manifestRules = manifest.Rules
		}
	}
	postureRules := postureRulesForSession(ctx, deps, sess)
	eval := rules.NewEvalContext(sess, toolName, args, phase, "", "")
	if agents, ok := TaskSpawnAllowlistFromContext(ctx); ok {
		// Turn roster attached: non-nil even when empty, so an all-filtered roster
		// reads as "no agents dispatchable" rather than falling open.
		eval.AllowedAgents = agents
	} else if len(allowed) > 0 {
		// Declared workflow allowlist; an absent declaration stays nil (unrestricted).
		eval.AllowedAgents = allowed
	}
	eval.PostureRules = postureRules
	eval.ManifestRules = manifestRules
	if deps.Workflows != nil && sess != nil {
		w := deps.Workflows
		eval.ReviewLoopActive = w.Policy.ActivePhaseHasReviewLoop(ctx, sess.ID)
		if run, err := w.Runs.ActiveBySession(ctx, sess.ID); err == nil && run != nil {
			eval.WorkflowID = run.WorkflowID
			eval.WorkflowRunID = run.ID
			eval.RunStatus = run.Status
		}
		if blueprintPath, content, okPlan := w.Blueprints.ActivePlan(ctx, sess.ID); okPlan {
			eval.BlueprintPath = blueprintPath
			eval.PlanContent = content
		}
		if vars, err := w.Policy.ScaffoldVarsForSession(ctx, sess.ID); err == nil {
			eval.Vars = vars
		}
	}
	if strings.TrimSpace(eval.PlanContent) != "" {
		flags := guidance.PlanEvalFlagsFromVars(eval.Vars)
		snap := guidance.DispatchFromVars(eval.Vars)
		eval.PlanProgress = guidance.ComputePlanProgress(eval.PlanContent, flags, snap)
		if sess != nil && strings.TrimSpace(eval.BlueprintPath) != "" {
			eval.PlanProgress.PlanPath = conditions.PlanPathForProject(
				sess.WorkspacePath,
				eval.BlueprintPath,
			)
		}
	}
	if deps.ProjectRootCount != nil && sess != nil {
		eval.ProjectRootCount = deps.ProjectRootCount(ctx, sess)
	}
	if deps.OverlayRootPaths != nil && sess != nil {
		eval.OverlayRootPaths = deps.OverlayRootPaths(ctx, sess)
	}
	return eval
}

func buildEvalContextFromCoordinatorFrame(
	ctx context.Context,
	deps EngineDeps,
	sess *api.Session,
	toolName string,
	args map[string]any,
	frame *inject.CoordinatorTurnFrame,
) rules.EvalContext {
	allowed := append([]string(nil), frame.RunContext.AllowedAgents...)
	if frame.Roster != nil {
		allowed = append([]string(nil), frame.Roster.Effective...)
	}
	eval := rules.NewEvalContext(sess, toolName, args, frame.RunContext.CurrentPhase, "", "")
	if agents, ok := TaskSpawnAllowlistFromContext(ctx); ok {
		eval.AllowedAgents = agents
	} else if len(allowed) > 0 {
		eval.AllowedAgents = allowed
	}
	eval.PostureRules = append([]string(nil), frame.PostureRules...)
	eval.ManifestRules = append([]string(nil), frame.ManifestRules...)
	eval.ReviewLoopActive = frame.Runtime.PhaseExit != nil && frame.Runtime.PhaseExit.ReviewLoopKey != ""
	eval.WorkflowID = frame.RunContext.WorkflowID
	eval.WorkflowRunID = frame.RunContext.RunID
	eval.RunStatus = api.WorkflowRunStatus(frame.RunContext.RunStatus)
	eval.Vars = cloneVars(frame.ScaffoldVars)
	if frame.Runtime.Blueprint != nil {
		eval.BlueprintPath = frame.Runtime.Blueprint.Path
		eval.PlanContent = frame.Runtime.BlueprintBody
	}
	if strings.TrimSpace(eval.PlanContent) != "" {
		flags := guidance.PlanEvalFlagsFromVars(eval.Vars)
		snap := guidance.DispatchFromVars(eval.Vars)
		eval.PlanProgress = guidance.ComputePlanProgress(eval.PlanContent, flags, snap)
		if sess != nil && strings.TrimSpace(eval.BlueprintPath) != "" {
			eval.PlanProgress.PlanPath = conditions.PlanPathForProject(
				sess.WorkspacePath,
				eval.BlueprintPath,
			)
		}
	}
	eval.ProjectRootCount = frame.ProjectRootCount
	eval.OverlayRootPaths = append([]string(nil), frame.OverlayRootPaths...)
	return eval
}

func postureRulesForSession(ctx context.Context, deps EngineDeps, sess *api.Session) []string {
	if deps.Postures == nil || sess == nil || sess.Posture == "" {
		return nil
	}
	postures, err := deps.Postures(ctx, sess)
	if err != nil || postures == nil {
		return nil
	}
	paths, err := postures.RulesPaths(sess.Posture)
	if err != nil {
		return nil
	}
	return append([]string(nil), paths...)
}

func cloneVars(vars map[string]any) map[string]any {
	if len(vars) == 0 {
		return nil
	}
	out := make(map[string]any, len(vars))
	for key, value := range vars {
		out[key] = value
	}
	return out
}
