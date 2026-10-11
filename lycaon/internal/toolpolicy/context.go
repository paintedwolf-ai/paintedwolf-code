package toolpolicy

import (
	"context"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/pkg/api"
)

// policyFacts lives for one listing or invocation, never across requests.
type policyFacts struct {
	workflow         WorkflowSnapshot
	postureRules     []string
	projectRootCount int
	overlayRootPaths []string
	planProgress     guidance.PlanProgress
}

// BuildEvalContext assembles fresh rule facts for one tool invocation.
func BuildEvalContext(ctx context.Context, deps EngineDeps, sess *api.Session, toolName string, args map[string]any) (rules.EvalContext, error) {
	facts, err := capturePolicyFacts(ctx, deps, sess)
	if err != nil {
		return rules.EvalContext{}, err
	}
	return facts.forTool(sess, toolName, args), nil
}

func capturePolicyFacts(ctx context.Context, deps EngineDeps, sess *api.Session) (policyFacts, error) {
	var facts policyFacts
	if frame, ok := coordinatorTurnFrameFromContext(ctx); ok {
		facts = policyFactsFromFrame(frame)
	} else {
		if deps.Workflows != nil && sess != nil {
			state, err := deps.Workflows(ctx, sess.ID)
			if err != nil {
				return facts, err
			}
			facts.workflow = state
		}
		facts.postureRules = postureRulesForSession(ctx, deps, sess)
		if deps.ProjectRootCount != nil && sess != nil {
			facts.projectRootCount = deps.ProjectRootCount(ctx, sess)
		}
		if deps.OverlayRootPaths != nil && sess != nil {
			facts.overlayRootPaths = deps.OverlayRootPaths(ctx, sess)
		}
	}
	if agents, ok := TaskSpawnAllowlistFromContext(ctx); ok {
		facts.workflow.AllowedAgents = agents
	}
	facts.workflow.AllowedAgents = slices.Clone(facts.workflow.AllowedAgents)
	facts.workflow.ManifestRules = slices.Clone(facts.workflow.ManifestRules)
	facts.postureRules = slices.Clone(facts.postureRules)
	facts.overlayRootPaths = slices.Clone(facts.overlayRootPaths)
	facts.workflow.Vars = jsonvalue.CloneMap(facts.workflow.Vars)
	if strings.TrimSpace(facts.workflow.PlanContent) != "" {
		flags := guidance.PlanEvalFlagsFromVars(facts.workflow.Vars)
		snap := guidance.DispatchFromVars(facts.workflow.Vars)
		facts.planProgress = guidance.ComputePlanProgress(facts.workflow.PlanContent, flags, snap)
		if sess != nil && strings.TrimSpace(facts.workflow.BlueprintPath) != "" {
			facts.planProgress.PlanPath = conditions.PlanPathForProject(sess.WorkspacePath, facts.workflow.BlueprintPath)
		}
	}
	return facts, nil
}

func policyFactsFromFrame(frame *inject.CoordinatorTurnFrame) policyFacts {
	state := WorkflowSnapshot{
		Phase: frame.RunContext.CurrentPhase, AllowedAgents: frame.RunContext.AllowedAgents,
		ManifestRules: frame.ManifestRules, Vars: frame.ScaffoldVars,
		RunID: frame.RunContext.RunID, WorkflowID: frame.RunContext.WorkflowID,
		RunStatus:        api.WorkflowRunStatus(frame.RunContext.RunStatus),
		ReviewLoopActive: frame.Runtime.PhaseExit != nil && frame.Runtime.PhaseExit.ReviewLoopKey != "",
	}
	if frame.Roster != nil {
		state.AllowedAgents = frame.Roster.Effective
	}
	if frame.Runtime.Blueprint != nil {
		state.BlueprintPath = frame.Runtime.Blueprint.Path
		state.PlanContent = frame.Runtime.BlueprintBody
	}
	return policyFacts{
		workflow: state, postureRules: frame.PostureRules,
		projectRootCount: frame.ProjectRootCount, overlayRootPaths: frame.OverlayRootPaths,
	}
}

func (f policyFacts) forTool(sess *api.Session, tool string, args map[string]any) rules.EvalContext {
	state := f.workflow
	eval := rules.NewEvalContext(sess, tool, args, state.Phase, state.BlueprintPath, state.PlanContent)
	eval.AllowedAgents = slices.Clone(state.AllowedAgents)
	eval.ManifestRules = slices.Clone(state.ManifestRules)
	eval.PostureRules = slices.Clone(f.postureRules)
	eval.ReviewLoopActive = state.ReviewLoopActive
	eval.WorkflowRunID = state.RunID
	eval.WorkflowID = state.WorkflowID
	eval.RunStatus = state.RunStatus
	eval.Vars = jsonvalue.CloneMap(state.Vars)
	eval.ProjectRootCount = f.projectRootCount
	eval.OverlayRootPaths = slices.Clone(f.overlayRootPaths)
	eval.PlanProgress = f.planProgress
	eval.PlanProgress.SkippedPhases = slices.Clone(f.planProgress.SkippedPhases)
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
