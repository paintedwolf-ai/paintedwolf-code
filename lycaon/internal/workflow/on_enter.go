package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// PhaseEnterRequest contains one phase-entry evaluation.
type PhaseEnterRequest struct {
	Sessions          session.Store
	SessionID         string
	Manifest          workflowdef.Manifest
	PhaseID           string
	Vars              map[string]any
	BlueprintPath     string
	Registry          *conditions.ConditionRegistry
	ReviewSpawnFilter func(context.Context, string, string, []string) []string
	// ChoiceEntry skips depth-derived evidence.
	ChoiceEntry bool
}

// ApplyPhaseOnEnter applies intake, phase hooks, and readiness stamps.
func ApplyPhaseOnEnter(ctx context.Context, req PhaseEnterRequest) (map[string]any, error) {
	def, ok := req.Manifest.PhaseByID(req.PhaseID)
	if !ok {
		return applyOnEnter(workflowdef.PhaseOnEnter{}, req.PhaseID, req.Vars)
	}
	enter := def.OnEnter
	if len(def.Intake) > 0 {
		var err error
		enter, req.Vars, err = applyIntakeOnEnter(def, enter, req.PhaseID, req.Vars)
		if err != nil {
			return nil, err
		}
	}
	vars, err := applyOnEnter(enter, req.PhaseID, req.Vars)
	if err != nil {
		return nil, err
	}
	if def.DepthParam != "" && !req.ChoiceEntry {
		vars = ResolveDepthSkip(vars, req.PhaseID, def.DepthParam)
		if def.ReviewLoop != nil {
			if conditions.DotPathTruthy(vars, "phase_skipped."+req.PhaseID) {
				key := strings.TrimSpace(def.ReviewLoop.EvidenceKey)
				if key != "" {
					vars = SetGateSatisfied(vars, "evidence_passed:"+key, true)
				}
			}
		}
	}
	if def.HumanApproval != nil {
		vars = StampHumanApprovalPhase(vars, def.HumanApproval, req.BlueprintPath)
		projectDir := ""
		if req.Sessions != nil && strings.TrimSpace(req.SessionID) != "" {
			if sess, err := req.Sessions.Get(ctx, req.SessionID); err == nil && sess != nil {
				projectDir = strings.TrimSpace(sess.WorkspacePath)
			}
		}
		vars = refreshHumanApprovalReady(ctx, req.Registry, def, vars, req.BlueprintPath, projectDir)
	}
	if def.ReviewLoop != nil && len(def.ReviewLoop.IfSpawnable) > 0 {
		roster := append([]string(nil), def.ReviewLoop.IfSpawnable...)
		if req.ReviewSpawnFilter != nil {
			roster = req.ReviewSpawnFilter(ctx, req.SessionID, def.CoordinatorSurface, roster)
		}
		vars = StampReviewIfSpawnable(vars, def.ID, roster)
	}
	vars = stampTerminalOrchestrationComplete(def, vars)
	return ApplyPhaseContentReviewVars(vars, def), nil
}

func applyIntakeOnEnter(def workflowdef.PhaseDef, enter workflowdef.PhaseOnEnter, phaseID string, vars map[string]any) (workflowdef.PhaseOnEnter, map[string]any, error) {
	if enter.RequestUserFeedback != nil {
		return enter, vars, fmt.Errorf("phase %q: intake cannot combine with request_user_feedback in YAML", phaseID)
	}
	return applyMultiIntakeOnEnter(def, enter, phaseID, vars)
}

func refreshHumanApprovalReady(ctx context.Context, reg *conditions.ConditionRegistry, def workflowdef.PhaseDef, vars map[string]any, blueprintPath, projectDir string) map[string]any {
	cfg := def.HumanApproval
	if cfg == nil {
		return vars
	}
	if reg == nil {
		return SetHumanApprovalReady(vars, false)
	}
	ec := conditions.EvalContext{
		Ctx:           ctx,
		Vars:          vars,
		Phase:         def.ID,
		BlueprintPath: strings.TrimSpace(blueprintPath),
		ProjectDir:    strings.TrimSpace(projectDir),
	}
	materialized, err := reg.Evaluate("blueprint_materialized", ec)
	if err != nil || !materialized {
		return SetHumanApprovalReady(vars, false)
	}
	readiness := strings.TrimSpace(cfg.Readiness)
	if readiness == "" {
		return SetHumanApprovalReady(vars, true)
	}
	ok, err := evaluateReadinessCondition(reg, ec, readiness)
	if err != nil || !ok {
		return SetHumanApprovalReady(vars, false)
	}
	return SetHumanApprovalReady(vars, true)
}

func evaluateReadinessCondition(reg *conditions.ConditionRegistry, ec conditions.EvalContext, readiness string) (bool, error) {
	if reg == nil {
		return false, nil
	}
	node, err := boolexpr.Parse(readiness)
	if err != nil {
		return false, err
	}
	env := func(name string) bool {
		ok, err := reg.Evaluate(name, ec)
		return err == nil && ok
	}
	return boolexpr.Eval(node, env), nil
}

func applyOnEnter(enter workflowdef.PhaseOnEnter, phaseID string, vars map[string]any) (map[string]any, error) {
	vars = cloneAskVars(vars)
	if sm := strings.TrimSpace(enter.SetPosture); sm != "" {
		if !profiles.ValidSessionPosture(sm) {
			return nil, fmt.Errorf("invalid set_posture %q on phase %q", sm, phaseID)
		}
	}
	if fb := enter.RequestUserFeedback; fb != nil {
		if fb.ResolvedResponseType().IsChoice() {
			vars = setDecisionPending(vars, phaseID, fb.Prompt, fb.Options)
		} else {
			vars = setFeedbackPending(vars, phaseID, fb.Prompt)
		}
	}
	if sem := strings.TrimSpace(enter.SetExecutionMode); sem != "" {
		var err error
		vars, err = stampExecutionModeVar(vars, phaseID, sem)
		if err != nil {
			return nil, err
		}
	}
	return vars, nil
}

// stampTerminalOrchestrationComplete closes terminal orchestration.
func stampTerminalOrchestrationComplete(def workflowdef.PhaseDef, vars map[string]any) map[string]any {
	if !def.Terminal || strings.TrimSpace(def.CompleteWhen) != "orchestration_complete" {
		return vars
	}
	vars = cloneVars(vars)
	vars["orchestration_complete"] = true
	return vars
}

// ApplyPhaseContentReviewVars stamps content_review policy into scaffold vars for a phase.
func ApplyPhaseContentReviewVars(vars map[string]any, def workflowdef.PhaseDef) map[string]any {
	vars = cloneVars(vars)
	if def.ContentReview == nil {
		delete(vars, "content_review")
		return vars
	}
	vars["content_review"] = map[string]any{
		"tools": append([]string(nil), def.ContentReview.Tools...),
		"paths": append([]string(nil), def.ContentReview.Paths...),
	}
	return vars
}

func setFeedbackPending(vars map[string]any, phaseID, prompt string) map[string]any {
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_feedback"] = bucket
	}
	bucket[phaseID] = map[string]any{
		"prompt":       prompt,
		"pending":      true,
		"requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return vars
}

func setDecisionPending(vars map[string]any, phaseID, prompt string, options []string) map[string]any {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_decision"] = bucket
	}
	bucket[phaseID] = map[string]any{
		"prompt":       prompt,
		"options":      append([]string(nil), options...),
		"pending":      true,
		"requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return vars
}

func cloneVars(vars map[string]any) map[string]any {
	if vars == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

// cloneAskVars deep-copies user_feedback / user_decision buckets so pending
// writers cannot mutate a shared nested map returned by the scaffold store.
func cloneAskVars(vars map[string]any) map[string]any {
	out := cloneVars(vars)
	for _, key := range []string{"user_feedback", "user_decision"} {
		bucket, _ := out[key].(map[string]any)
		if bucket == nil {
			continue
		}
		copied := make(map[string]any, len(bucket))
		for phaseID, raw := range bucket {
			if entry, ok := raw.(map[string]any); ok {
				entryCopy := make(map[string]any, len(entry))
				for ek, ev := range entry {
					entryCopy[ek] = ev
				}
				copied[phaseID] = entryCopy
			} else {
				copied[phaseID] = raw
			}
		}
		out[key] = copied
	}
	return out
}
