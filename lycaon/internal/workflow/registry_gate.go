package workflow

import (
	"context"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/observability"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

var workflowGateLog = observability.LazyComponent("workflow_gate")

// RegistryGateEvaluator evaluates manifest complete_when via conditions.ConditionRegistry.
type RegistryGateEvaluator struct {
	Registry *conditions.ConditionRegistry
	Sessions SessionLookup
}

// SessionLookup loads session context for gate evaluation.
type SessionLookup interface {
	Get(ctx context.Context, sessionID string) (*api.Session, error)
}

func (e RegistryGateEvaluator) PhaseGateMet(ctx context.Context, manifest workflowdef.Manifest, run *api.WorkflowRun, vars map[string]any) (bool, workflowgates.GateCheckResult, error) {
	if run == nil {
		return true, workflowgates.GateCheckResult{}, nil
	}
	def, ok := manifest.PhaseForRun(run, run.CurrentPhase)
	if !ok {
		return true, workflowgates.GateCheckResult{}, nil
	}
	okPrimary, result, err := e.evaluatePrimaryGate(ctx, def, run, vars)
	if err != nil || !okPrimary {
		return okPrimary, result, err
	}
	return true, workflowgates.GateCheckResult{}, nil
}

func (e RegistryGateEvaluator) evaluatePrimaryGate(ctx context.Context, def workflowdef.PhaseDef, run *api.WorkflowRun, vars map[string]any) (bool, workflowgates.GateCheckResult, error) {
	cw := strings.TrimSpace(def.CompleteWhen)
	switch cw {
	case workflowdef.CompleteWhenGatesSatisfied:
		return e.evaluateGateList(ctx, def, run, vars, def.Gates, workflowdef.CompleteWhenGatesSatisfied)
	case "":
		return true, workflowgates.GateCheckResult{}, nil
	default:
		if strings.HasPrefix(cw, workflowdef.CompleteWhenGateSatisfied) {
			gate := strings.TrimPrefix(cw, workflowdef.CompleteWhenGateSatisfied)
			return e.evaluateGateList(ctx, def, run, vars, []string{gate}, cw)
		}
		return e.ExpressionMet(ctx, cw, def, run, vars)
	}
}

func (e RegistryGateEvaluator) evaluateGateList(ctx context.Context, def workflowdef.PhaseDef, run *api.WorkflowRun, vars map[string]any, gates []string, reason string) (bool, workflowgates.GateCheckResult, error) {
	reg := e.Registry
	if reg == nil {
		return false, workflowgates.GateCheckResult{Reason: reason, FailedGate: reason, FailedLeaves: gates}, nil
	}
	ec := e.buildEvalContext(ctx, def, run, vars)
	failed, err := collectFailedLeaves(reg, ec, gates)
	if err != nil {
		return false, workflowgates.GateCheckResult{}, err
	}
	if len(failed) > 0 {
		workflowGateLog.Debug("phase gates not satisfied",
			"session_id", ec.SessionID,
			"workflow_id", ec.WorkflowID,
			"phase", ec.Phase,
			"blueprint_path", ec.BlueprintPath,
			"failed_leaves", failed,
		)
		return false, workflowgates.GateCheckResult{
			Reason:       reason,
			FailedGate:   failed[0],
			FailedLeaves: failed,
		}, nil
	}
	return true, workflowgates.GateCheckResult{}, nil
}

func (e RegistryGateEvaluator) ExpressionMet(ctx context.Context, expr string, def workflowdef.PhaseDef, run *api.WorkflowRun, vars map[string]any) (bool, workflowgates.GateCheckResult, error) {
	reg := e.Registry
	if reg == nil {
		return false, workflowgates.GateCheckResult{Reason: expr, FailedGate: expr, FailedLeaves: []string{expr}}, nil
	}
	ec := e.buildEvalContext(ctx, def, run, vars)

	// A bare gate leaf parses to a one-node tree, so there is no second path.
	node, err := boolexpr.Parse(expr)
	if err != nil {
		return false, workflowgates.GateCheckResult{Reason: "invalid complete_when: " + expr, FailedGate: expr, FailedLeaves: []string{expr}}, nil //nolint:nilerr // parse failure becomes a structured gate-deny reason rather than a fatal error
	}
	env := func(name string) bool {
		ok, err := reg.Evaluate(name, ec)
		return err == nil && ok
	}
	if boolexpr.Eval(node, env) {
		return true, workflowgates.GateCheckResult{}, nil
	}
	failed, err := collectFailedLeavesFromExpr(reg, ec, node)
	if err != nil {
		return false, workflowgates.GateCheckResult{}, err
	}
	if len(failed) == 0 {
		failed = []string{expr}
	}
	return false, workflowgates.GateCheckResult{
		Reason:       expr,
		FailedGate:   failed[0],
		FailedLeaves: failed,
	}, nil
}

func (e RegistryGateEvaluator) buildEvalContext(ctx context.Context, def workflowdef.PhaseDef, run *api.WorkflowRun, vars map[string]any) conditions.EvalContext {
	var sess *api.Session
	if e.Sessions != nil && run != nil && run.SessionID != "" {
		sess, _ = e.Sessions.Get(ctx, run.SessionID)
	}
	ec := conditions.EvalContextFromRun(ctx, sess, run, vars)
	ec.BindTopologyStage = def.BindTopologyStage
	ec.BindParallelGroup = append([]string(nil), def.BindParallelGroup...)
	return ec
}

func collectFailedLeaves(reg *conditions.ConditionRegistry, ec conditions.EvalContext, leaves []string) ([]string, error) {
	var failed []string
	for _, leaf := range leaves {
		leaf = strings.TrimSpace(leaf)
		if leaf == "" {
			continue
		}
		ok, err := reg.Evaluate(leaf, ec)
		if err != nil && !conditions.IsUnknownCondition(err) {
			return nil, err
		}
		if !ok {
			failed = append(failed, leaf)
		}
	}
	return failed, nil
}

func collectFailedLeavesFromExpr(reg *conditions.ConditionRegistry, ec conditions.EvalContext, node boolexpr.Node) ([]string, error) {
	return collectFailedLeaves(reg, ec, boolexpr.CollectIdents(node))
}
