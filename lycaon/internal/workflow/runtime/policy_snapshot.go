package runtime

import (
	"context"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"slices"
	"strings"
)

// PolicySnapshot captures the active revision and its resolved policy once.
func (m *SessionPolicy) PolicySnapshot(ctx context.Context, sessionID string) (toolpolicy.WorkflowSnapshot, error) {
	if m == nil || sessionID == "" {
		return toolpolicy.WorkflowSnapshot{}, nil
	}
	run, vars, err := m.Runs.ActiveStateBySession(ctx, sessionID)
	if err != nil {
		return toolpolicy.WorkflowSnapshot{}, err
	}
	if run == nil {
		return toolpolicy.WorkflowSnapshot{AllowedAgents: spawn.AmbientAllowedAgents()}, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return toolpolicy.WorkflowSnapshot{}, err
	}
	out := toolpolicy.WorkflowSnapshot{Phase: run.CurrentPhase, AllowedAgents: m.RosterFor(run, manifest), ManifestRules: slices.Clone(manifest.Rules), Vars: vars, RunID: run.ID, WorkflowID: run.WorkflowID, RunStatus: run.Status, BlueprintPath: strings.TrimSpace(run.BlueprintPath)}
	if phase, ok := manifest.PhaseByID(run.CurrentPhase); ok {
		out.ReviewLoopActive = phase.ReviewLoop != nil
	}
	if m.Blueprints != nil {
		out.PlanContent, err = m.Blueprints.PolicyContent(ctx, run.ProjectID, out.BlueprintPath)
	}
	if err != nil {
		return toolpolicy.WorkflowSnapshot{}, err
	}
	return out, nil
}
