package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApplyMergedParams writes merged start parameters into scaffold vars.
func ApplyMergedParams(vars map[string]any, params map[string]string) map[string]any {
	vars = cloneVars(vars)
	for name, val := range params {
		if val != "" {
			vars = SetHostVar(vars, "params."+name, val)
		}
	}
	return vars
}

// RecordWorkflowArtifactParams stamps depth parameters onto artifact.<blueprint-id>.* for inject.
func RecordWorkflowArtifactParams(vars map[string]any, manifest workflowdef.Manifest) map[string]any {
	if manifest.Blueprint == nil {
		return vars
	}
	for name, spec := range manifest.Parameters {
		if spec.Type != "depth" {
			continue
		}
		if val, ok := DotPathString(vars, "params."+name); ok {
			vars = SetHostVar(vars, "artifact."+manifest.Blueprint.ID+"."+name, val)
		}
	}
	return vars
}

// ApplyAutoApproveEffects applies the auto_approve workflow effects.
func ApplyAutoApproveEffects(vars map[string]any, manifest workflowdef.Manifest) map[string]any {
	if !parameterIsTrue(vars, "auto_approve") {
		return vars
	}
	vars = SetHostVar(vars, "phase_skipped.approve", true)
	if bucket, ok := vars["user_feedback"].(map[string]any); ok {
		for _, def := range manifest.PhaseDefs {
			if len(def.Intake) > 0 {
				delete(bucket, def.ID)
			}
		}
	}
	for _, def := range manifest.PhaseDefs {
		if len(def.Intake) == 0 {
			continue
		}
		for _, key := range def.Intake {
			choice := autoApproveIntakeDefault(key)
			vars = setDecisionChoice(vars, key, choice, "")
			vars = SetHostVar(vars, "intake."+key, choice)
		}
	}
	// Stamp consulted gates for auto-approved phases.
	for _, def := range manifest.PhaseDefs {
		for _, g := range def.Gates {
			if strings.HasPrefix(g, "hitl_consulted:") {
				vars = stampHitlConsulted(vars, def.ID)
				break
			}
		}
	}
	return vars
}

func autoApproveIntakeDefault(key string) string {
	switch strings.TrimSpace(key) {
	case "change_size":
		return "medium"
	case "breaking_change":
		return "none"
	default:
		return "medium"
	}
}

func parameterIsTrue(vars map[string]any, name string) bool {
	raw, ok := DotPathString(vars, "params."+name)
	return ok && strings.EqualFold(strings.TrimSpace(raw), "true")
}

// StampDepthParamSkips records phases disabled by depth=none.
func StampDepthParamSkips(vars map[string]any, manifest workflowdef.Manifest) map[string]any {
	for _, def := range manifest.PhaseDefs {
		if def.DepthParam == "" {
			continue
		}
		vars = ResolveDepthSkip(vars, def.ID, def.DepthParam)
		if def.ReviewLoop != nil && conditions.DotPathTruthy(vars, "phase_skipped."+def.ID) {
			key := strings.TrimSpace(def.ReviewLoop.EvidenceKey)
			if key != "" {
				vars = SetGateSatisfied(vars, "evidence_passed:"+key, true)
			}
		}
	}
	return vars
}

// ApplyAutoApproveOnApprovePhase satisfies an auto-approved phase.
func ApplyAutoApproveOnApprovePhase(ctx context.Context, m *RunManager, run *api.WorkflowRun, manifest workflowdef.Manifest, def workflowdef.PhaseDef, vars map[string]any) (map[string]any, error) {
	if def.ID != "approve" || def.HumanApproval == nil || !parameterIsTrue(vars, "auto_approve") {
		return vars, nil
	}
	projectDir := ""
	if m != nil && m.Sessions != nil && run != nil {
		if sess, err := m.Sessions.Get(ctx, run.SessionID); err == nil && sess != nil {
			projectDir = sess.WorkspacePath
		}
	}
	content, err := ResolveBlueprintContent(ctx, m, run, projectDir, def.HumanApproval.Blueprint)
	if err != nil {
		return nil, fmt.Errorf("auto_approve blueprint: %w", err)
	}
	vars = refreshHumanApprovalReady(ctx, m.Registry, def, vars, run.BlueprintPath, projectDir)
	if !conditions.DotPathTruthy(vars, "human_approval.ready") {
		return vars, nil
	}
	vars = SetHumanApprovalIssued(vars, true)
	vars = SetHumanApprovalReady(vars, true)
	vars = SetHumanApprovalHash(vars, workflowdef.HashBlueprintContent(content))
	vars = SatisfyGateInVars(vars, "human_approval")
	return vars, nil
}
