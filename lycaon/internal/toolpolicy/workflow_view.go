package toolpolicy

import (
	"context"
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowDomains binds the workflow facts read during tool policy evaluation.
type WorkflowDomains struct {
	Policy     WorkflowPolicy
	Blueprints WorkflowBlueprints
	Runs       WorkflowRuns
}
type WorkflowPolicy interface {
	CurrentPhase(context.Context, string) string
	ActivePhaseHasReviewLoop(context.Context, string) bool
	AllowedAgents(context.Context, string) []string
	ActiveManifest(context.Context, string) (workflowfacts.ActiveWorkflowManifest, bool)
	ScaffoldVarsForSession(context.Context, string) (map[string]any, error)
}
type WorkflowBlueprints interface {
	ActivePlan(context.Context, string) (string, string, bool)
}
type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}

// PostureRegistry resolves posture rule pack paths (session.PostureRegistry subset).
type PostureRegistry interface {
	RulesPaths(posture api.SessionPosture) ([]string, error)
}
